# Гарантии доставки и идемпотентность — стенд

Живой стенд к статье [«Гарантии доставки и идемпотентность»](https://khorost.tech/architecture/delivery-guarantees-idempotency/)
на [khorost.tech](https://khorost.tech).

Показывает главный практический вывод темы: **at-least-once доставка + идемпотентный
консьюмер = effectively-once**. Ровно-однократной доставки в распределённой системе
не бывает — но её можно получить как эффект, если приёмник устроен правильно.

## Что демонстрирует

Источник эмитит события списания кислорода из бака `T-1` со **стабильным** `event_id`
(`OxygenConsumed{tank, event_id, amount, remaining}`). «Брокер» имитирует at-least-once —
доставляет каждое третье событие **дважды**. Один и тот же поток доставок обрабатывают
три консьюмера:

| Вариант | Стратегия | Что с дублями | Итог |
|---|---|---|---|
| **(a) наивный дельта** | `balance -= amount` на каждую доставку | списывает повторно | **НЕВЕРНО** (задвоение) |
| **(b) dedup-ключ** | в одной транзакции: записать `event_id` в таблицу ключей; новый — применить эффект, иначе пропустить | отбрасываются | ВЕРНО |
| **(c) естественная идемпотентность** | событие несёт снимок `remaining`; `balance = remaining` (UPSERT) | безвредны без хранилища ключей | ВЕРНО |

Ключевые моменты кода (`go/main.go`):

- **(b)** — атомарность: запись ключа и применение эффекта коммитятся **одной
  транзакцией**. `INSERT ... ON CONFLICT DO NOTHING` + проверка `RowsAffected()`
  решает «ключ новый или дубль» без гонок. Падение между записью ключа и эффектом
  не разъедет состояние — либо оба, либо ничего.
- **(c)** — идемпотентность «по построению»: событие несёт не дельту, а итоговое
  значение. Повторное применение того же снимка — no-op по значению, поэтому
  таблица обработанных ключей вообще не нужна. Работает при at-least-once с
  сохранением порядка (дубль приходит сразу за оригиналом); при переупорядочивании
  для «последний побеждает» понадобился бы монотонный номер версии в `WHERE`.

Ассерты в коде **падают** (`log.Fatalf`) при расхождении: у (b) и (c) итог обязан
равняться сумме уникальных списаний; у (a) итог обязан быть **неверным** (иначе
задвоение не воспроизвелось и демонстрация бессмысленна). Число отброшенных (b)
дублей сверяется с числом инъецированных.

## Версии (сверено живьём 2026-07-08)

| Компонент | Версия | Как проверено |
|---|---|---|
| PostgreSQL (образ) | `postgres:16` | `docker compose up -d`, `pg_isready` healthcheck, прогон клиента |
| pgx | `v5.10.0` | `go.mod` / Go module proxy |
| Go | `1.25.0` (`go.mod`), прогон в образе `golang:1.26` | `go build ./...`, `go vet ./...`, `go run .` — чисто |
| JDBC-драйвер | `org.postgresql:postgresql:42.7.4` | `pom.xml`, `mvn compile` в образе `maven:3.9-eclipse-temurin-21` |
| Java | JDK 21 (`maven.compiler.release=21`), прогон в образе `maven:3.9-eclipse-temurin-21` | `mvn -q compile` — чисто; `mvn compile exec:java` — прогон в compose-сети |

## Как поднять

```bash
cd idempotency/compose
docker compose up -d          # Postgres 16 на host-порту 5450, container idem-postgres

cd ../go
go run .                      # схему создаёт и сбрасывает сам клиент при старте
```

Строку подключения можно переопределить через `DATABASE_URL` (по умолчанию
`postgres://idem:idem@localhost:5450/idem`). Повторные `go run .` воспроизводимы —
`setupSchema` сбрасывает балансы и таблицу ключей к старту.

### Java-порт

`java/` повторяет тот же сценарий на чистом JDBC (тот же поток доставок, те же три
консьюмера и ассерты; вывод совпадает с Go-версией). Требуется JDK 21 и Maven —
либо в контейнере (host-инструменты не нужны):

```bash
cd idempotency/compose
docker compose up -d          # Postgres 16 на host-порту 5450

# Компиляция + прогон в compose-сети (клиент ходит на idem-postgres:5432):
cd ../java
docker run --rm --network compose_default \
  -e DATABASE_URL=jdbc:postgresql://idem-postgres:5432/idem \
  -e PGUSER=idem -e PGPASSWORD=idem \
  -v "$(pwd)":/app -w /app maven:3.9-eclipse-temurin-21 \
  mvn -q compile exec:java
```

С локально установленными JDK 21 и Maven (Postgres на host-порту 5450):

```bash
cd idempotency/java
mvn -q compile exec:java      # DATABASE_URL по умолчанию jdbc:postgresql://localhost:5450/idem
```

Строку подключения переопределяет `DATABASE_URL` (JDBC-URL), пользователя и пароль —
`PGUSER` / `PGPASSWORD` (по умолчанию `idem` / `idem`). Схему создаёт и сбрасывает
сам клиент при старте, прогоны воспроизводимы. Только сборка без прогона —
`mvn -q compile`.

Остановить стенд:

```bash
cd idempotency/compose && docker compose down -v
```

## Ожидаемый вывод

Прогон 2026-07-08 (клиент — образ `golang:1.26` в compose-сети, `DATABASE_URL`
на `idem-postgres:5432`):

```
Бак T-1: старт 1000, уникальных событий 8, доставок (с дублями) 10, инъецировано дублей 2
Сумма уникальных списаний 385 → корректный итог должен быть 615

Итоговые балансы:
  (a) наивный дельта          balance=505  (ожидаемо НЕВЕРНО, задвоено на 110)
  (b) dedup-ключ              balance=615  (отброшено дублей: 2)
  (c) естественная идемпот.   balance=615  (хранилище ключей не нужно)

OK: (b) dedup-ключ и (c) естественная идемпотентность дают effectively-once
    поверх at-least-once; (a) наивный дельта задваивает списание на дублях.
```

Наивный вариант (a) занижен на 110 = сумма двух задвоенных списаний (события #3 и #6,
`70 + 40`). Варианты (b) и (c) дают корректные 615 несмотря на дубли.

## Структура

```
idempotency/
  compose/compose.yml   # Postgres 16, host-порт 5450, container idem-postgres
  go/
    go.mod              # module tech.khorost/idempotency-cookbook, pgx v5
    main.go             # источник + at-least-once брокер + три консьюмера + ассерты
  java/
    pom.xml             # tech.khorost:idempotency-cookbook-java, JDBC 42.7.4, JDK 21
    src/main/java/tech/khorost/idempotency/Main.java  # тот же сценарий на чистом JDBC
  README.md
```
