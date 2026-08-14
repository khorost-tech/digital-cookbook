# CQRS на практике — стенд

Живой пример к статье [«CQRS на практике»](https://khorost.tech/architecture/cqrs-in-practice/)
на [khorost.tech](https://khorost.tech).

Домен — заказы. Стенд показывает **read-сторону** CQRS: разделение чтения и записи через
проекции, поломку и обход read-your-writes, лаг проекции как метрику и blue-green
пересборку read-модели. Write-сторона намеренно простая — append-only лог событий как
источник проекций (event store со снапшотами разбирает соседний стенд `event-sourcing/`,
здесь мы его не дублируем).

## Что демонстрирует

| # | Механика | Где в коде |
|---|----------|------------|
| write-модель | команды `createOrder`/`payOrder` только добавляют события в append-only лог `order_events`; текущее состояние — свёртка (fold) лога | `writemodel.go` |
| async-проекция | отдельный проектор строит денормализованную `orders_read` (индекс по `user_id` — заточка под запрос «заказы пользователя»), применяет лог идемпотентно (UPSERT) и двигает чекпоинт **атомарно** с проекцией | `projector.go` |
| read-your-writes: поломка | сразу после записи читаем свой список из проекции, а она отстаёт → «не вижу свою запись» | `main.go` (1) |
| read-your-writes: приём №1 | собственную свежую запись читаем с write-стороны (свёртка лога, без лага); чужие — из проекции | `main.go` (2) |
| read-your-writes: приём №2 | ждём по токену-позиции, пока проекция догонит нужный `seq`, затем читаем из неё | `main.go` (3), `waitForProjection` |
| лаг проекции | метрика = хвост лога (`max(seq)`) − чекпоинт проектора | `projectionLag` |
| blue-green rebuild | пересобираем `orders_read_v2` полным реплеем без остановки чтения, атомарно меняем таблицы местами (транзакционный `RENAME` в PostgreSQL) | `rebuild.go` |

Инварианты закреплены ассертами (`log.Fatalf` при расхождении): проекция после догона ==
свёртка write-стороны; приём read-your-writes возвращает свежие данные; проекция после
rebuild == онлайн-состоянию.

## Структура

```
cqrs/
  compose/compose.yml   # PostgreSQL 16, host-порт 5453, container_name cqrs-postgres
  go/
    go.mod              # module tech.khorost/cqrs-cookbook, pgx/v5
    writemodel.go       # write-сторона: лог событий + команды + свёртка
    projector.go        # read-сторона: проекция orders_read, чекпоинт, лаг, ожидание по токену
    rebuild.go          # blue-green пересборка orders_read_v2 + атомарный switch
    main.go             # сценарий + ассерты
  java/                 # тот же сценарий на чистом JDBC (JDK 21, package tech.khorost.cqrs)
    pom.xml             # postgresql:42.7.4, maven.compiler.release=21
    src/main/java/tech/khorost/cqrs/
      WriteModel.java   # write-сторона: лог событий + команды + свёртка
      Projector.java    # read-сторона: проекция orders_read, чекпоинт, лаг, ожидание по токену
      Rebuild.java      # blue-green пересборка orders_read_v2 + атомарный switch
      Order.java        # денормализованное представление заказа (read-модель)
      Main.java         # сценарий + ассерты
```

## Версии (сверено живьём 2026-07-08)

| Компонент | Версия | Как проверено |
|---|---|---|
| PostgreSQL (образ) | `postgres:16` → фактически `16.13` | `docker logs cqrs-postgres` (`starting PostgreSQL 16.13`) |
| Go | `go.mod` — `1.25.0`; прогон в контейнере `golang:1.25`; локально собирается и на `go1.26.3` | `go build ./...` + `go run .` внутри контейнера |
| pgx | `github.com/jackc/pgx/v5 v5.10.0` | `go.mod` |
| Java | JDK 21 (`maven.compiler.release=21`); сборка/прогон в контейнере `maven:3.9-eclipse-temurin-21` | `mvn -q compile` (чисто) + `mvn -q compile exec:java` внутри контейнера |
| JDBC | `org.postgresql:postgresql 42.7.4` | `pom.xml` |

## Как поднять и прогнать

```bash
# 1. Поднять Postgres (изолированный проект -p cqrs — чтобы не задевать соседние стенды репо)
docker compose -p cqrs -f cqrs/compose/compose.yml up -d

# 2. Прогнать сценарий
cd cqrs/go && go run .

# 3. Снести
docker compose -p cqrs -f cqrs/compose/compose.yml down -v
```

Строка подключения по умолчанию — `postgres://cqrs:cqrs@localhost:5453/cqrs`, переопределяется
через `DATABASE_URL`.

> **Windows-хост:** в этом окружении подключение с хоста к `localhost:5453` временами
> блокируется на уровне ОС/файрвола (та же особенность отмечена в стенде `kafka/`). Тогда
> прогоняйте Go-клиент контейнером на compose-сети, обращаясь к Postgres по имени сервиса:
>
> ```bash
> docker run --rm --network cqrs_default -v "$PWD/cqrs/go:/app" -w /app \
>   -e DATABASE_URL='postgres://cqrs:cqrs@postgres:5432/cqrs' golang:1.25 go run .
> ```

Порт `5453` выбран свободным на момент создания стенда (в репозитории заняты
5432/5433/5440–5442/5450/5452).

## Ожидаемый вывод

```
=== (0) затравка: пишем команды и один раз прогоняем проектор ===
применено событий: 4, лаг проекции: 0

=== (1) read-your-writes: поломка (читаем из отстающей проекции) ===
создан заказ #1003 (token seq=5), лаг проекции теперь: 1
проекция вернула Alice 1 заказ(ов): [#1002(new,300)]
read-your-writes НАРУШЕН: заказа #1003 в проекции нет (лаг=1)

=== (2) приём №1: свои свежие заказы читаем с write-стороны (свёртка лога) ===
write-сторона вернула Alice 2 заказ(ов): [#1002(new,300) #1003(new,999)]
read-your-writes ВОССТАНОВЛЕН: заказ #1003 виден сразу (write-сторона без лага)
заказы Bob (чужие для Alice) — из проекции: [#1000(paid,100) #1001(new,250)]

=== (3) приём №2: ждём, пока проекция догонит токен, и читаем из неё ===
после догона (лаг=0) проекция вернула Alice: [#1002(new,300) #1003(new,999)]

=== (4) инвариант: догнавшая проекция == свёртка write-стороны ===
совпадает: проекция догнала лог и равна авторитетной свёртке

=== (5) blue-green rebuild: реплей в orders_read_v2 + атомарный switch ===
реплейнуто событий в v2: 5, лаг после switch: 0
совпадает: пересобранная проекция идентична онлайн-состоянию и свёртке

ВСЕ АНСЕРТЫ ПРОЙДЕНЫ ✔
```

## Java

Порт на JDK 21 (`cqrs/java/`, package `tech.khorost.cqrs`) — тот же сценарий и те же ассерты
на чистом JDBC. Даёт байт-в-байт тот же вывод, что и Go-версия (проверено живьём 2026-07-08).

Host-`mvn` в этом окружении нет — собираем и прогоняем контейнером `maven:3.9-eclipse-temurin-21`.

```bash
# 1. Поднять Postgres (тот же стенд, что и для Go)
docker compose -p cqrs -f cqrs/compose/compose.yml up -d

# 2. Прогнать Java-сценарий контейнером на compose-сети (Postgres по имени сервиса)
docker run --rm --network cqrs_default \
  -v "$PWD/cqrs/java:/app" -w /app \
  -e DATABASE_URL='jdbc:postgresql://postgres:5432/cqrs' \
  -e PGUSER=cqrs -e PGPASSWORD=cqrs \
  maven:3.9-eclipse-temurin-21 mvn -q compile exec:java

# 3. Снести
docker compose -p cqrs -f cqrs/compose/compose.yml down -v
```

Только компиляция (без Postgres) — тоже контейнером:

```bash
docker run --rm -v "$PWD/cqrs/java:/app" -w /app \
  maven:3.9-eclipse-temurin-21 mvn -q -DskipTests compile
```

Строка подключения — переменная `DATABASE_URL` (JDBC-URL, по умолчанию
`jdbc:postgresql://localhost:5453/cqrs`); креды — `PGUSER`/`PGPASSWORD` (по умолчанию `cqrs`).
При прямом запуске с Windows-хоста действует та же оговорка про `localhost:5453`, что и для
Go, — тогда прогоняйте контейнером на `cqrs_default`, как в примере выше.
