# ScyllaDB: глубокое погружение — стенды

Живые стенды к серии статей «ScyllaDB: глубокое погружение» на
[khorost.tech](https://khorost.tech). Этот каталог — завершённый cookbook
серии: 3-узловой кластер одного датацентра (`datacenter1`) для основных
стендов + отдельный multi-DC compose (2 ДЦ × 2 узла) для Стенда #5, общий
детерминированный датасет-телеметрия и 7 живых стендов на Go (+ Java-зеркало
в Стенде #6): #1 модель данных (good/bad/hot partition), #2 архитектура
(shard-per-core), #3 compaction/tombstones/repair, #4 consistency levels +
LWT, #5 tablets + multi-DC, #6 shard-aware драйверы (Go/Java), #7
эксплуатация (monitoring/backup/Alternator). Materialized views/secondary
indexes в серию не вошли — рассматривались изначально, но слот #4 достался
consistency/LWT (см. «Стенд #4» ниже); MV/SI остаются кандидатом для
отдельной будущей задачи, не частью текущего cookbook.

## Топология стенда (Task 1)

Сеть compose: `scylla-cookbook-net`.

| Сервис | Образ | Внутри сети | С хоста |
|---|---|---|---|
| ScyllaDB узел 1 | `scylladb/scylla:2026.2.0` | `scylla1:9042` | `localhost:9042` |
| ScyllaDB узел 2 | `scylladb/scylla:2026.2.0` | `scylla2:9042` | — (не опубликован) |
| ScyllaDB узел 3 | `scylladb/scylla:2026.2.0` | `scylla3:9042` | — (не опубликован) |

Один датацентр (`datacenter1`), RF=3 (`NetworkTopologyStrategy`), keyspace
`telemetry` создаётся `dataset/schema.cql`. Второй ДЦ (мульти-DC
гео-репликация) — отдельный compose следующей задачи серии, сюда НЕ входит.

Порт 9042 (CQL native) опубликован только у `scylla1` — этого достаточно для
control-connection, драйвер сам обнаруживает остальные узлы кластера
(`system.peers`). **Важно** (см. «Честные ограничения» ниже): загрузчики на
Go-стороне лучше запускать КОНТЕЙНЕРОМ на сети `scylla-cookbook-net`
(`scylla1:9042` и т.п.), а не с хоста через `localhost:9042` — шард-осведомлённый
драйвер ненадёжно устанавливает соединение через опубликованный порт Docker
на Windows/Docker Desktop.

## Версии (сверено живьём 2026-07-10/11, финальная сводка Task 9)

| Компонент | Версия | Как проверено |
|---|---|---|
| ScyllaDB (образ) | `scylladb/scylla:2026.2.0` | пин явный (не `latest`); `docker exec scylla1 scylla --version` внутри контейнера → `2026.2.0-0.20260618.ccb141ab3d0c` |
| ScyllaDB (CQL/release_version) | `3.0.8` | `SELECT release_version FROM system.local` (протокольная Cassandra-совместимая версия, не версия сервера) |
| gocql (шард-осведомлённый форк ScyllaDB) | `v1.18.3` | `dataset/go.mod`: `require github.com/gocql/gocql v1.18.3` + `replace github.com/gocql/gocql => github.com/scylladb/gocql v1.18.3` (см. ниже, почему через `replace`) |
| Go | `1.26` (`go.mod` всех 8 модулей) | сборка через образ `golang:1.26` |
| JDK (Java) | `25` (Task 7, первый Java-реактор серии) | `maven.compiler.release=25` в `java/pom.xml`; сборка через образ `maven:3.9-eclipse-temurin-25` |
| java-driver-core (шард-осведомлённый форк ScyllaDB) | `4.19.2.0` | `drivers/java/pom.xml` (наследует версию из `java/pom.xml` dependencyManagement); пин сверен живьём 2026-07-11 по `maven-metadata.xml` Maven Central (`<latest>4.19.2.0</latest>`) |
| Prometheus (Task 8, Стенд #7, `compose/monitoring.yml`) | `prom/prometheus:v3.7.3` | пин явный в `compose/monitoring.yml`; `api/v1/targets` живьём подтвердил 3 таргета `"health":"up"` |

**Почему `replace`, а не прямой `go get github.com/scylladb/gocql`:**
после объединения апстрима `gocql/gocql` и форка ScyllaDB репозиторий
`github.com/scylladb/gocql` (тег `v1.18.3`, шард-осведомлённый — умеет
роутить запросы напрямую на нужный шард ноды) в своём `go.mod` объявляет
модульный путь как `github.com/gocql/gocql` (канонический путь после
слияния), поэтому `go get github.com/scylladb/gocql@latest` падает с
ошибкой расхождения путей модуля. Рабочая связка — `require
github.com/gocql/gocql <версия>` + `replace github.com/gocql/gocql =>
github.com/scylladb/gocql <та же версия>` в `go.mod`, импорт в коде —
`github.com/gocql/gocql` (self-declared путь пакета). Прямой
`go get github.com/gocql/gocql@latest` (без replace) тоже не подходит:
последний тег в каноническом репозитории — `v1.7.0`, это версия ДО слияния
со шард-осведомлённым кодом ScyllaDB (проверено: в исходниках `v1.7.0` нет
ни одного упоминания "shard").

## Датасет

Один общий детерминированный датасет "телеметрия устройств" (`dataset/main.go`,
модуль `github.com/khorost-tech/digital-cookbook/scylladb/dataset`) —
используется всеми стендами серии, чтобы модели данных (good/bad/hot
partition), компакция, LWT и т.д. демонстрировались на идентичных данных.

```
device_id   text       — идентификатор устройства (dev-00000 .. dev-NNNNN)
day         date       — сутки замера (часть партиционного ключа good-модели)
event_time  timestamp  — момент замера (кластеризация DESC)
metric      text       — cpu | mem | temp | netio | disk
value       double     — значение метрики, [20.0, 80.0)
region      text       — eu-west | eu-east | us-east | ap-south (детерминирован по устройству)
status      text       — ok | warn | crit (по порогам value)
```

Генератор детерминирован: фиксированный `-seed` (по умолчанию `42`) →
байт-в-байт одинаковый вывод при повторных запусках, **не зависит от даты
запуска** (окно суток отсчитывается от зашитой в код опорной точки
`2026-07-01T00:00:00Z`, а не от `time.Now()`). Регион и распределение по
метрикам детерминированы по индексу устройства/замера, значение
`value` — единственный псевдослучайный компонент (из `-seed`).

По умолчанию (`-devices 500 -days 14 -per-day 96`) — **672 000** строк
(500 устройств × 14 суток × 96 замеров/сутки, каждые 15 минут).

### Запуск генератора

```bash
cd scylladb/dataset
go run . -devices 500 -days 14 -per-day 96 -out count   # печатает 672000
go run . -devices 500 -days 14 -per-day 96 -out csv > out.csv
```

CSV-вывод **не коммитится** (см. `.gitignore`) — воспроизводится
детерминированно по `-seed`.

### Загрузка в кластер

```bash
# ВНУТРИ compose-сети (рекомендуется, см. «Честные ограничения»):
docker run --rm --network scylla-cookbook-net \
  -v "$(pwd)/dataset:/app" -w /app golang:1.26 \
  sh -c 'go run . -devices 500 -days 14 -per-day 96 -load -hosts scylla1:9042,scylla2:9042,scylla3:9042'

# С хоста (может не работать на Windows/Docker Desktop, см. ограничения):
cd scylladb/dataset
go run . -devices 500 -days 14 -per-day 96 -load -hosts 127.0.0.1:9042
```

`loadReadings` — prepared `INSERT`, `gocql.LoggedBatch` СТРОГО в пределах
одной партиции `(device_id, day)` (Generate уже выдаёт строки смежными
блоками по партиции — группировка одним проходом, без промежуточной map).
Батч через границу партиции — сам по себе анти-паттерн, которого этот
загрузчик намеренно избегает (демонстрация анти-паттерна — отдельный стенд,
таблицы `readings_bad`/`readings_hot`).

## env-контракт для всех стендов серии

```
SCYLLA_HOSTS   CSV host:port контактных точек кластера, дефолт 127.0.0.1:9042
               (внутри compose-сети — scylla1:9042,scylla2:9042,scylla3:9042)
```

## Структура каталога (Go/Java)

```
scylladb/
  compose/
    compose.yml         # 3-узловой кластер одного ДЦ
    monitoring.yml       # Стенд #7 — Prometheus scrape ScyllaDB :9180/metrics
    multidc.yml           # Стенд #5 — 2 ДЦ × 2 узла (GossipingPropertyFileSnitch)
    prometheus.yml        # конфиг Prometheus (scrape targets) для monitoring.yml
  dataset/               # Go-генератор общего датасета + загрузчик (свой модуль)
    go.mod
    main.go
    schema.cql           # keyspace telemetry: readings (good) / readings_bad / readings_hot
  modeling/               # Стенд #1 — good/bad/hot partition (свой модуль)
    go.mod
    main.go
  architecture/           # Стенд #2 — shard-per-core, shard distribution + p50/p99 (свой модуль)
    go.mod
    main.go
  compaction/             # Стенд #3 — TWCS, tombstones/gc_grace, repair (свой модуль)
    go.mod
    main.go
  consistency-lwt/        # Стенд #4 — consistency levels + LWT/Paxos (свой модуль)
    go.mod
    main.go
  topology/               # Стенд #5 — tablets + multi-DC (свой модуль)
    go.mod
    main.go
  drivers/
    go/                   # Стенд #6 — shard-aware ВКЛ vs ВЫКЛ, gocql (свой модуль)
      go.mod
      main.go
    java/                 # Стенд #6 — shard-aware ВКЛ vs ВЫКЛ, java-driver (модуль реактора java/)
      pom.xml
      src/main/java/tech/khorost/scylla/DriversBench.java
  ops-stand/              # Стенд #7 — Alternator (DynamoDB API) smoke (свой модуль)
    go.mod
    main.go
  java/
    pom.xml               # родительский Maven-реактор (модуль drivers/java)
  ops/
    verify-static.sh     # статический гейт (build+vet+compose config+mvn package), без live-стендов
    repair-demo.sh       # Стенд #3 — repair вживую (stop/write/start/nodetool cluster repair)
    topology-demo.sh     # Стенд #5 — add/decommission + multi-DC failover
    ops-demo.sh           # Стенд #7 — backup/restore (snapshot/refresh) + nodetool сводки
  FIXTURES.md            # сводка реальных чисел всех 7 стендов (источник для Phase 2)
  README.md
  .gitignore
```

Каждый стенд — самостоятельный Go-модуль (Стенд #6 дополнен Java-зеркалом в
`drivers/java`, модуль реактора `java/`). Полный набор стендов и их числа
собраны ниже в разделе «Стенды серии»; `ops/verify-static.sh` (финальная
версия, Task 9) покрывает все 8 Go-модулей, Java-реактор и все 3
compose-комбинации.

## Как запускать

```bash
cd scylladb

# Docker Desktop/WSL2: по умолчанию fs.aio-max-nr слишком мал для 3 узлов
# ScyllaDB на одном хосте — см. «Честные ограничения», шаг обязателен на
# таких хостах, иначе scylla2/scylla3 падают в crash-loop с ошибкой AIO.
docker run --rm --privileged alpine sh -c "echo 1048576 > /proc/sys/fs/aio-max-nr"

docker compose -f compose/compose.yml up -d

# ScyllaDB стартует ~30-90с на узел, узлы входят последовательно — ждать
# кворума синхронно:
until docker exec scylla1 nodetool status 2>/dev/null | grep -cE '^UN' | grep -q 3; do sleep 5; done
docker exec scylla1 nodetool status

# схема
docker exec -i scylla1 cqlsh < dataset/schema.cql
docker exec scylla1 cqlsh -e "DESCRIBE KEYSPACE telemetry"

# после стенда
docker compose -f compose/compose.yml down -v
```

### Быстрая статическая проверка (без тяжёлых стендов)

```bash
bash ops/verify-static.sh
```

Гейт для CI/pre-push (финальная версия, Task 9 — покрывает всю серию): `bash
-n` всех `ops/*.sh`, `go build -o /dev/null ./... && go vet ./...` по ВСЕМ 8
Go-модулям (обнаруживаются автоматически через `find . -name go.mod`, а не
перечисляются вручную), `mvn -DskipTests package` по Java-реактору `java/`
(включает `../drivers/java`), `docker compose config` по всем 3 реальным
комбинациям compose-файлов серии (`compose/compose.yml`,
`compose/compose.yml compose/monitoring.yml`, `compose/multidc.yml`).
Требует только Docker, **не поднимает ни одного сервиса ScyllaDB** — живой
прогон (2026-07-11) занял **163 секунды** целиком (с холодными
`.gocache`/`.m2cache`; при тёплых кэшах заметно быстрее), против минут
live-прогона с ожиданием кворума на 3–4-узловом кластере.

## Стенды серии (7 живых стендов)

### Стенд #1 — модель данных: good vs bad vs hot partition

Таблицы `readings` (good, партиция `(device_id, day)`), `readings_bad`
(партиция только `device_id` — неограниченный рост партиции с числом суток),
`readings_hot` (партиция только `region` — 4 гигантские партиции) созданы
`dataset/schema.cql` (Task 1). Task 2 загружает тот же датасет
(`Generate(42,500,14,96)`, 672000 строк) в `readings_bad`/`readings_hot`,
измеряет реальные размеры партиций на живом кластере и ассертит вывод модели
данных программно (`scylladb/modeling/`).

#### Загрузка bad/hot (тем же генератором, `-table`)

`dataset/main.go` — один генератор/загрузчик на все три таблицы (флаг
`-table readings|readings_bad|readings_hot`; `readings` уже загружена в
Task 1, здесь грузятся только bad/hot). UNLOGGED-батчи по `batchChunkRows=200`
строк, батч НЕ пересекает границу партиции целевой таблицы:

```bash
export MSYS_NO_PATHCONV=1
docker run --rm --network scylla-cookbook-net \
  -v "$(pwd):/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app/dataset \
  -e SCYLLA_HOSTS=scylla1:9042,scylla2:9042,scylla3:9042 golang:1.26 \
  sh -c 'go run . -devices 500 -days 14 -per-day 96 -load -table readings_bad'

docker run --rm --network scylla-cookbook-net \
  -v "$(pwd):/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app/dataset \
  -e SCYLLA_HOSTS=scylla1:9042,scylla2:9042,scylla3:9042 golang:1.26 \
  sh -c 'go run . -devices 500 -days 14 -per-day 96 -load -table readings_hot'
```

Живой прогон (2026-07-11): `loaded 672000 rows into telemetry.readings_bad`
за ~23s, `loaded 672000 rows into telemetry.readings_hot` за ~24s.
`SELECT count(*)` подтвердил 672000 строк в обеих таблицах.

#### Реальные размеры партиций (`nodetool tablestats`, после `nodetool flush telemetry`)

| Таблица | Модель | Партиций (estimate) | Min bytes | Max bytes | Mean bytes | Space used (live) |
|---|---|---:|---:|---:|---:|---:|
| `readings` | good `(device_id, day)` | 7000 | 3974 | **4768** | 4768 | 12 842 925 |
| `readings_bad` | bad `device_id` | 500 | 51013 | **73457** | 63467 | 11 858 012 |
| `readings_hot` | hot `region` | **4** | 7 007 507 | **8 409 007** | 8 409 007 | 13 854 928 |

Источник — реальный вывод на живом кластере:

```
$ docker exec scylla1 nodetool flush telemetry
$ docker exec scylla1 nodetool tablestats telemetry.readings | grep -E "Space used|Number of partitions|Compacted partition"
Space used (live): 12842925
Number of partitions (estimate): 7000
Compacted partition minimum bytes: 3974
Compacted partition maximum bytes: 4768
Compacted partition mean bytes: 4768

$ docker exec scylla1 nodetool tablestats telemetry.readings_bad | grep -E "Space used|Number of partitions|Compacted partition"
Space used (live): 11858012
Number of partitions (estimate): 500
Compacted partition minimum bytes: 51013
Compacted partition maximum bytes: 73457
Compacted partition mean bytes: 63467

$ docker exec scylla1 nodetool tablestats telemetry.readings_hot | grep -E "Space used|Number of partitions|Compacted partition"
Space used (live): 13854928
Number of partitions (estimate): 4
Compacted partition minimum bytes: 7007507
Compacted partition maximum bytes: 8409007
Compacted partition mean bytes: 8409007
```

Вывод модели данных, подтверждённый реальными числами: **bad_max (73457) >
good_max (4768)** — 15.4×; **hot_max (8409007) > bad_max (73457)** — 114.5×;
**hot_partitions == 4 == len(regions)**. Ширина строки во всех трёх таблицах
близка (~50 байт/строку: 4768/96≈49.7, 73457/1344≈54.7, 8409007/168000≈50.1) —
разница в размере партиции почти целиком объясняется числом строк в ней, не
структурой таблицы.

`nodetool toppartitions telemetry readings_hot 5000` (Step 2 брифа) вернул
`Nothing recorded during sampling period` для обоих семплеров (READS/WRITES) —
честное ограничение: сэмплер считает трафик ТОЛЬКО в течение окна замера,
а в момент запуска кластер не обслуживал запросы (загрузка уже завершилась,
стенд `modeling` ещё не стартовал). `nodetool tablestats` (выше) — рабочий,
задокументированный в ambiguity resolution #2 брифа источник фактических чисел,
использован как основной.

#### `scylladb/modeling/` — сценарии, живые ассерты

`modeling/main.go` (свой Go-модуль, `-scenario partition-size|hot-partition|query-shape`,
`env SCYLLA_HOSTS`) НЕ грузит данные — измеряет уже загруженный кластер живыми
CQL-запросами (число строк на партицию как proxy размера — ширина строки
одинакова по всем трём таблицам, см. выше) и печатает реальные числа + ассерты.
Байтовые числа (`nodetool tablestats`) недоступны из этого контейнера (нет
доступа к docker-сокету хоста с сети `scylla-cookbook-net`) — раздел выше их
фиксирует отдельно.

```bash
docker run --rm --network scylla-cookbook-net \
  -v "$(pwd):/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app/modeling \
  -e SCYLLA_HOSTS=scylla1:9042,scylla2:9042,scylla3:9042 golang:1.26 \
  sh -c 'go run . -scenario partition-size'
```

**`-scenario partition-size`** (живой вывод, 2026-07-11):

```
-- readings (good model) -- max строк/партиция (по выборке): 96, mean: 96.0
-- readings_bad (bad model) -- партиций (устройств): 500, max строк/партиция: 1344, mean: 1344.0
-- readings_hot (hot-partition model) -- партиций (регионов): 4, max строк/партиция: 168000

OK: bad_max (1344 строк/партиция) > good_max (96 строк/партиция)
OK: hot_partitions == 4 (len(regions))
OK: hot_max (168000 строк/партиция) > bad_max (1344 строк/партиция) — hot-partition хуже bad
```

**`-scenario hot-partition`** — цена чтения гигантской партиции vs маленькой,
живой замер:

```
readings[dev-00000, 2026-07-01]: 96 строк за 4.85ms
readings_hot[region=eu-west]: 168000 строк за 218.6ms
OK: hot-скан партиции (218.6ms) дороже good-скана (4.85ms)  — 45×
```

Честная оговорка (напечатана самим стендом): на этом кластере 3 узла и RF=3 —
каждый узел реплицирует каждую партицию, поэтому hot-partition здесь НЕ создаёт
перекоса нагрузки МЕЖДУ узлами (это требует кластера, где узлов больше RF).
Что видно уже здесь — дороговизна чтения/компакции самой гигантской партиции
(latency полного скана, память под кеш индекса партиции) — не зависит от
размера кластера.

**`-scenario query-shape`** — типовой запрос «1 устройство за 1 сутки»:

```
readings (good): 96 строк за 3.6ms, ALLOW FILTERING не потребовался
readings_bad:    96 строк за 5.1ms, ALLOW FILTERING не потребовался
readings_hot без ALLOW FILTERING: ошибка CQL — Clustering column "device_id"
  cannot be restricted (preceding column "event_time" is restricted by a
  non-EQ relation)
readings_hot с ALLOW FILTERING:   96 строк за 28.7ms
```

good/bad удовлетворяют типовой запрос без `ALLOW FILTERING` (`device_id` в
нужной позиции ключа); `readings_hot` партиционирован по `region` — удобен
только для запроса «дай мне весь регион», любой более точный запрос требует
`ALLOW FILTERING` внутри гигантской партиции — CQL-подтверждение антипаттерна,
не только байты.

Все три сценария завершились с кодом `0` (все ассерты зелёные) в живом
прогоне 2026-07-11.

### Стенд #2 — архитектура shard-per-core (распределение по шардам + p50/p99 без GC-пауз)

`scylladb/architecture/` (свой Go-модуль, `-scenario shard-distribution|latency`,
`env SCYLLA_HOSTS`) не грузит данные — точечными чтениями по случайным полным
ключам (`device_id, day, event_time`) уже загруженной `readings` (672000 строк,
Task 1) показывает архитектурную точку серии: каждый узел ScyllaDB — не один
поток, а `--smp N` независимых шардов-реакторов (Seastar, shard-per-core), и
именно поэтому у ScyllaDB нет JVM-стиля stop-the-world GC-пауз — каждый шард
управляет своей памятью сам, без общей кучи и её паузы на весь процесс.

```bash
export MSYS_NO_PATHCONV=1
docker run --rm --network scylla-cookbook-net \
  -v "$(pwd):/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app/architecture \
  -e SCYLLA_HOSTS=scylla1:9042,scylla2:9042,scylla3:9042 golang:1.26 \
  sh -c 'go run . -scenario shard-distribution -n 20000'

docker run --rm --network scylla-cookbook-net \
  -v "$(pwd):/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app/architecture \
  -e SCYLLA_HOSTS=scylla1:9042,scylla2:9042,scylla3:9042 golang:1.26 \
  sh -c 'go run . -scenario latency -n 50000'
```

#### Как читаются метрики: доступ к `:9180/metrics` ИЗНУТРИ сети стенда

В отличие от `nodetool` (Task 1/2, требует docker-сокет хоста — недоступен
из контейнера на `scylla-cookbook-net`), Prometheus endpoint ScyllaDB
(`:9180/metrics`) слушает не на `127.0.0.1`, а на СЕТЕВОМ адресе контейнера
(проверено живьём: `docker exec scylla1 curl localhost:9180/metrics` —
connection refused, `curl http://172.22.0.2:9180/metrics` — 200 OK). Это
значит, что порт доступен по внутреннему DNS-имени узла (`scylla1:9180`,
`scylla2:9180`, `scylla3:9180`) с ЛЮБОГО контейнера той же compose-сети —
проверено `curlimages/curl` на `scylla-cookbook-net` (200 OK на всех трёх
узлах). Стенд `architecture` использует это напрямую (`net/http` внутри
Go, без обхода через хост) — более простой путь, чем split
docker-exec/контейнер из Task 2.

#### `-scenario shard-distribution` — живая нагрузка по шардам

Счётчик `scylla_database_total_reads{shard="N"}` (найден через `grep '^scylla_' | grep 'shard='`
на реальном выводе `:9180/metrics` — присутствует per-shard, `shard="0"` и
`shard="1"` при `--smp 2`) снимается ДО и ПОСЛЕ прогона 20000 точечных чтений
по случайным ключам, суммируется по всем `class=` (driver/system/streaming/...) —
считает нагрузку на ядро, не разбивку по классам обслуживания.

Живой прогон (2026-07-11), дельта (after − before) по шардам на всех трёх узлах:

```
scylla1:9180 shard=0: 79926 -> 87054 (delta=7128)
scylla1:9180 shard=1: 73237 -> 80092 (delta=6855)
OK: scylla1:9180 — 2/2 шардов приняли новые чтения (нагрузка распределена, не на одном шарде)
scylla2:9180 shard=0: 73055 -> 79889 (delta=6834)
scylla2:9180 shard=1: 61026 -> 67068 (delta=6042)
OK: scylla2:9180 — 2/2 шардов приняли новые чтения (нагрузка распределена, не на одном шарде)
scylla3:9180 shard=0: 71827 -> 78658 (delta=6831)
scylla3:9180 shard=1: 74220 -> 81687 (delta=7467)
OK: scylla3:9180 — 2/2 шардов приняли новые чтения (нагрузка распределена, не на одном шарде)
```

Оба шарда КАЖДОГО узла выросли примерно поровну (~6000-7500 чтений каждый) —
20000 запросов разошлись по обоим ядрам каждого узла почти 50/50, не легли
на один шард. Сумма дельт по узлу (~12900-14600) больше `n=20000`/3 —
ожидаемо: RF=3 + `Consistency: Quorum` требует ответа от 2 из 3 реплик на
каждое чтение, так что каждый запрос физически исполняется на ≥2 узлах.
Воспроизведено дважды подряд (второй прогон — те же пропорции, см.
`../scratchout/arch-shard-distribution*.txt`), оба раза `EXIT 0`.

#### `-scenario latency` — p50/p99/max клиентских латентностей

Клиентские латентности (`time.Since` вокруг каждого CQL round-trip, 50000
последовательных точечных чтений, один коннекшн) — живой прогон (2026-07-11):

```
Успешных чтений: 50000/50000 (ошибок: 0)

min:   582.593µs
p50:  1.546779ms
p90:  1.609271ms
p99:  1.711527ms
p999: 1.998652ms
max:  4.283256ms

OK: p99/p50 = 1.11 <= K=5
NOTE: max=4.283256ms < 200ms — ни одного выброса GC-масштаба (сотни мс) за весь прогон
```

Воспроизведено трижды подряд, реальный `p99/p50` стабильно в диапазоне
**1.09–1.13**, `max` — единицы миллисекунд (4.3–7.5ms), НИ РАЗУ не было
выброса даже близко к десяткам/сотням мс.

Серверная сторона — `nodetool proxyhistograms` на `scylla1` в тот же момент:

```
$ docker exec scylla1 nodetool proxyhistograms
Percentile       Read Latency (micros)
50%                    179.00
75%                    191.00
95%                    220.75
98%                    240.50
99%                    253.75
Min                    143.00
Max                    377.00
```

Серверный `p99/p50 = 253.75/179.00 ≈ 1.42` — тот же порядок, что и клиентский
(1.11). Клиентские числа выше серверных на ~1.3-1.4ms — это сетевой/Docker
Desktop оверхед моста `scylla-cookbook-net` + время самого CQL round-trip
клиента, не серверная задержка ScyllaDB.

#### Ассерт и честный выбор K

Брифом предписано не подгонять K под желаемый зелёный результат, а измерить
реальное соотношение и выбрать K по факту. Реальный `p99/p50` на этом
idle-кластере (никакой параллельной нагрузки, кроме собственного прогона) —
**1.09–1.13** стабильно на трёх независимых прогонах. Стенд ассертит
`p99 <= K*p50` с `K=5` (флаг `-k`, дефолт) — это НЕ подогнанный под факт
минимум (1.13), а осознанный запас: на кластере с фоновой компакцией/репаиром
(не этот стенд, но реалистичный прод-сценарий) хвост может уехать заметно
дальше идле-цифры, а K=5 всё ещё на два порядка строже того, что дал бы
JVM-стиль stop-the-world GC (там p99 обычно "выстреливает" в сотни мс —
т.е. p99/p50 порядка 50-500× при p50 в единицы мс, а не 1.1-5×). Прогон
(`go run . -scenario latency -n 50000`) во всех трёх повторах прошёл с
`EXIT 0` при этом K — инвариант держится с большим запасом, подгонка не
потребовалась.

#### Честная оговорка (специфика этого стенда)

- **Метод шардинга — `scylla_database_total_reads`, не `scylla_reactor_utilization`.**
  Бриф предлагал оба счётчика; `reactor_utilization` — gauge (мгновенная доля
  занятости CPU реактора), сильно шумит от фоновой активности кластера между
  снапшотами ДО/ПОСЛЕ и плохо подходит для чистого before/after-сравнения.
  `scylla_database_total_reads` — монотонный counter, дельта между двумя
  снапшотами однозначно равна числу обработанных чтений на этом шарде за
  интервал — выбран как более чистый сигнал.
- **Латентность — последовательная нагрузка, один коннекшн.** 50000
  запросов идут строго один за другим (не параллельно) — так измерение p50/p99
  отражает именно latency одного round-trip, без искажения от возможной
  очереди на клиенте. Это НЕ throughput-бенчмарк (пропускная способность при
  параллельной нагрузке — тема отдельного стенда серии, `../ops`/`../modeling`
  такое не покрывают).
- **`Consistency: Quorum`** (как во всех стендах серии, см. `connect()`) —
  латентность включает время ответа от ≥2 из 3 реплик, не от одной ноды.

### Стенд #3 — compaction, tombstones, repair (Scylla-специфика LSM)

`scylladb/compaction/` (свой Go-модуль, `-scenario twcs|tombstones`, `env
SCYLLA_HOSTS`) + `scylladb/ops/repair-demo.sh` — не generic LSM-компакция
(это отдельный стенд серии), а конкретно то, что у ScyllaDB устроено
по-своему: `TimeWindowCompactionStrategy` для time-series, накопление
tombstones + `gc_grace_seconds`, и repair на **tablet**-keyspace (не
классический vnode-repair). Создаёт `telemetry.readings_twcs` (та же
партиция `(device_id, day)`, что и `readings`, Task 1) с явной TWCS.

```bash
export MSYS_NO_PATHCONV=1
docker run --rm --network scylla-cookbook-net \
  -v "$(pwd):/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app/compaction \
  -e SCYLLA_HOSTS=scylla1:9042,scylla2:9042,scylla3:9042 golang:1.26 \
  sh -c 'go run . -scenario twcs'

docker run --rm --network scylla-cookbook-net \
  -v "$(pwd):/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app/compaction \
  -e SCYLLA_HOSTS=scylla1:9042,scylla2:9042,scylla3:9042 golang:1.26 \
  sh -c 'go run . -scenario tombstones'

bash ops/repair-demo.sh
```

#### Метрики читаются через REST `:10000`, не через docker-сокет хоста

`nodetool` внутри контейнера узла сам ходит на `localhost:10000` (его
REST API) — этот же порт слушает `0.0.0.0` (см. `SCYLLA_DOCKER_ARGS
--api-address 0.0.0.0` в конфиге образа) и поэтому доступен по внутреннему
DNS-имени узла (`scylla1:10000` и т.п.) с ЛЮБОГО контейнера сети
`scylla-cookbook-net` — тот же приём, что `:9180/metrics` в Стенде #2, без
необходимости в docker-сокете хоста. Стенд `compaction` использует
`POST /storage_service/keyspace_flush/{keyspace}?cf={table}` (синхронный
flush) и `GET /column_family/metrics/live_ss_table_count/{ks:table}`
напрямую из Go (проверено живьём: `curl` из контейнера на сети вернул
корректные значения на всех трёх узлах).

#### `-scenario twcs` — sstable по временным окнам, живая находка про WRITE-timestamp

**Критически важная живая находка** (обнаружена ПЕРВЫМ прогоном, ДО фикса):
TWCS группирует sstable в окна по **WRITE-timestamp мутации** (то же самое,
что `WRITETIME(col)` в CQL, и `max_timestamp` в `scylla sstable
dump-statistics`), а **НЕ** по значению какого-либо столбца данных. Первый
прогон грузил 14 суток телеметрии обычным `INSERT` без явного `USING
TIMESTAMP` — вся историческая телеметрия физически записывалась в течение
секунд реального времени, и TWCS положил **все 14 симулированных суток в
одно и то же окно** (окно текущего момента записи), несмотря на то что
`event_time` в данных был размазан на 14 дней. Подтверждено байтами:
```
$ scylla sstable dump-statistics <ранний sstable>
min_timestamp: 1783727192443968, max_timestamp: 1783727231918502
```
— разница `max-min` ≈ 39.5 **секунд** (реальное время загрузки), не 14
суток `event_time`. Фикс — `INSERT ... USING TIMESTAMP ?` с явным значением
`event_time.UnixMicro()` (`compaction/main.go`, `insertTWCSCQL`) — тот же
приём, которым реальные бэкафилы исторических time-series данных в
ScyllaDB/Cassandra заставляют TWCS группировать sstable по историческому
времени события, а не по времени миграции.

После фикса — живой прогон (2026-07-11, воспроизведено дважды подряд с
идентичным результатом), 500 устройств × 96 замеров/сутки, flush после
КАЖДЫХ суток, per-node REST `live_ss_table_count`:

```
день  0 (2026-07-01): scylla1/2/3: 0 -> 32 (delta=32)
день  1 (2026-07-02): scylla1/2/3: 32 -> 64 (delta=32)
...
день 13 (2026-07-14): scylla1/2/3: 416 -> 448 (delta=32)

Итог: scylla1/2/3 — окон с новым sstable 14/14, новых sstable всего 448
(avg/окно=32.00 РОВНО, без единого отклонения), финальный count=448
OK: twcs_sstables_per_window >= 1 на всех узлах
```

**Каждое окно (сутки) дало РОВНО 32 новых sstable** — стабильно, без
фонового слияния МЕЖДУ окнами (в отличие от первого прогона до фикса
timestamp, где фоновая компакция активно перемешивала счётчики между
измерениями — см. историю коммитов). Точное число 32 (не 1 и не
`--smp 2`×узлы) не диагностировано в рамках этого стенда — вероятно,
несколько промежуточных auto-flush памяти ДО явного REST-flush при заливке
48000 строк за раз; честно фиксируем факт (стабильно воспроизводимый,
дважды подряд байт-в-байт идентичный), не гадаем.

**Байтовое подтверждение "TWCS не смешивает окна"** — `scylla sstable
dump-statistics` по реальным файлам `readings_twcs-*` (не через
docker-сокет, а прямым `docker exec` в контейнер узла — host-side
операция, не из Go-стенда):

```
$ scylla sstable dump-statistics <первый по времени sstable>
min_timestamp: 1782864000000000, max_timestamp: 1782949500000000
→ 2026-07-01 00:00:00 UTC .. 2026-07-01 23:45:00 UTC   (ровно сутки 0)

$ scylla sstable dump-statistics <последний по времени sstable>
min_timestamp: 1783987200000000, max_timestamp: 1784072700000000
→ 2026-07-14 00:00:00 UTC .. 2026-07-14 23:45:00 UTC   (ровно сутки 13)
```

Ни один проверенный sstable не содержит данные ДВУХ разных суток — именно
это TWCS обещает и именно это проверяется байтами, не догадкой по числу
файлов.

#### `-scenario tombstones` — массовый DELETE, gc_grace, traced read

Живой прогон (2026-07-11, после ревью-фикса причины удвоения — см. ниже):

```
gc_grace_seconds (живьём из system_schema.tables): 864000 (10.0 суток)

Целевые сутки: 2026-07-06.
Строк на выборке из 20 партиций ДО удаления: 96 каждая

Массовый DELETE по clustering-диапазону [2026-07-06T00:00:00Z, 2026-07-06T12:00:00Z)
для ВСЕХ 500 партиций (device_id, day=2026-07-06)... 500/500, ошибок: 0

Строк на той же выборке ПОСЛЕ удаления: 48 каждая
OK: удаление подтверждено на всех 20 выборочных партициях (before-48 == after)

-- Traced read через удалённый диапазон (gocql.Tracer -> system_traces.events) --
Прочитано живых строк: 48 (ожидание 48)
   [source=172.22.0.2] Page stats: 1 partition(s) (1 live, 0 dead), 0 static row(s) (0 live, 0 dead),
     96 clustering row(s) (48 live, 48 dead), 2 range tombstone(s) and 384 cell(s)
     (192 live, 192 dead)
   [source=172.22.0.3] Page stats: ...(та же строка, ДРУГОЙ source — вторая реплика)

Page stats по репликам (по TraceEntry.Source), различных источников: 2
   source=172.22.0.2: dead=48
   source=172.22.0.3: dead=48

tombstones_scanned (сумма "dead" по ВСЕМ репликам traced read, QUORUM = 2 реплики × 48 dead) = 96
OK: tombstones_scanned > 0 (traced read реально прошёл через удалённые строки на каждой опрошенной реплике)
```

**Исправлено по ревью — причина удвоения (48 → 96) НЕ спекулятивное чтение.**
Изначально стенд и этот README ошибочно объясняли удвоение "спекулятивным
чтением / speculative execution". Это неверно и **проверено живьём**:
вендоренный `github.com/scylladb/gocql@v1.18.3` по умолчанию использует
`NonSpeculativeExecution` (0 дополнительных попыток), а стенд нигде не
вызывает `cluster.SpeculativeExecutionPolicy` / `Query.SetSpeculativeExecutionPolicy`
— спекулятивных чтений здесь физически нет. Настоящая причина: traced
SELECT выполняется на `session.Consistency = gocql.Quorum`, а keyspace
`telemetry` реплицирован с RF=3 → на QUORUM координатор опрашивает **2 из
3** реплик, и КАЖДАЯ опрошенная реплика независимо пишет свою собственную
строку `"Page stats: ..."` в `system_traces.events` — они различаются
полем `TraceEntry.Source` (адрес реплики, доступен в gocql, но раньше не
читался стендом). Живой прогон выше подтверждает это напрямую: **2
различных `Source`** (`172.22.0.2` и `172.22.0.3`), по 48 dead на каждый —
итого `tombstones_scanned = 96` означает **"48 dead cells × 2 реплики на
QUORUM"**, а НЕ 96 различных tombstone-ячеек. Реальное число
tombstone-ячеек на диапазон — 48 (столько же, сколько live-строк до
удаления половины окна), что и подтверждается любой ОДНОЙ строкой `Page
stats`.

**Честная находка**: и `nodetool tablestats telemetry.readings_twcs | grep
tombstone` ("Average/Maximum tombstones per slice"), и REST `GET
/column_family/metrics/tombstone_scanned_histogram/telemetry:readings_twcs`
остались на **0** во всех проверках (все 3 узла, до и после нескольких
подтверждённых чтений через диапазон с tombstones) — несмотря на
подтверждённые 48 dead-строк на чтение. Проверено многократно живьём, не
единичный сбой измерения. Рабочий, подтверждённый источник числа
tombstones — строка `"Page stats: ... clustering row(s) (X live, Y dead)"`
из `system_traces.events`, доступная ДВУМЯ независимыми путями:
host-side `cqlsh -e "TRACING ON; SELECT ..."` (см. брифом предписанный
пример — воспроизведён живьём, идентичная строка) и programmatically из
Go через `gocql.NewTracer(session)` + `Query.Trace(tracer)` +
`tracer.GetActivities(traceId)` (используется стендом для самого ассерта
`tombstones_scanned > 0`, без host-side шага; теперь дополнительно
группируется по `TraceEntry.Source` для честного объяснения удвоения).

Host-side `TRACING ON` (буквальный пример из брифа, воспроизведён
живьём):
```
$ docker exec scylla1 cqlsh -e "TRACING ON; SELECT device_id, day, event_time, metric, region, status, value FROM telemetry.readings_twcs WHERE device_id='dev-00000' AND day='2026-07-06';"
...
Page stats: 1 partition(s) (1 live, 0 dead), 0 static row(s) (0 live, 0 dead),
  96 clustering row(s) (48 live, 48 dead), 2 range tombstone(s) and 384 cell(s)
  (192 live, 192 dead) [shard 1/sl:default]
```

gc_grace_seconds=864000 (10 суток, дефолт) подтверждён живьём из
`system_schema.tables` — tombstones физически остаются в sstable (и
участвуют в чтениях/компакции) до истечения этого срока; в рамках одной
сессии стенда истечение не воспроизводится (10 суток), фиксируется только
само значение и факт присутствия tombstones на момент прогона.

#### `ops/repair-demo.sh` — repair вживую, честная находка про tablets

**Критически важная живая находка**: keyspace `telemetry` создан с
`tablets = {'enabled': true}` (дефолт для новых keyspace в ScyllaDB
`2026.2.0`, см. `DESCRIBE KEYSPACE telemetry` в «Честных ограничениях»
Task 1). Буквальная команда из брифа `nodetool repair -pr telemetry`
(написана для классического vnode-репликации) **не работает** на этом
кластере — падает с честной ошибкой самого `nodetool`:
```
$ docker exec scylla1 nodetool repair -pr telemetry
error processing arguments: nodetool repair repairs only vnode keyspaces!
To repair tablet keyspaces use nodetool cluster repair.
```
Рабочая замена, использованная скриптом — `nodetool cluster repair
--keyspace telemetry` (обходит ВСЕ таблицы keyspace одним вызовом, не
нужен `-pr`/`--partitioner-range` — за один прогон на ОДНОМ узле
достаточно для полного repair всех tablets всего кластера, см. `nodetool
cluster repair --help`). Ещё одна честная деталь: у старого vnode-`nodetool
repair` инкрементального режима нет вообще (`--full`: "note that repairs
on ScyllaDB are always full"), а у `nodetool cluster repair` **есть**
`--incremental-mode disabled|incremental|full` — проверено живьём, оба
нетривиальных значения (`incremental` и `full`) принимаются без ошибки.
Скрипт использует режим по умолчанию (полный обход, без флага) — честнее
для разового демонстрационного прогона без baseline repaired-состояния.

Живой прогон (`bash ops/repair-demo.sh`, 2026-07-11, воспроизведено дважды
подряд): останавливает `scylla3`, пишет 10000 строк (500 партиций × 20,
QUORUM=2/3 всё ещё выполним) НАПРЯМУЮ, пока `scylla3` down, поднимает узел
обратно, сразу (минимальная задержка) проверяет 3 выборочные партиции
НАПРЯМУЮ на `scylla3` (`CONSISTENCY ONE` — только его локальная реплика,
без координации с другими узлами), запускает `nodetool cluster repair
--keyspace telemetry`, проверяет те же партиции снова.

**Прогон A** (реальное расхождение поймано):
```
ДО repair (CONSISTENCY ONE на scylla3):
  dev-repair-demo-0001-00000: 20
  dev-repair-demo-0001-00250: 20
  dev-repair-demo-0001-00499: 0      <- расхождение: hint ещё не долетел

nodetool cluster repair --keyspace telemetry:
  readings_hot, readings_bad, readings, readings_twcs — 4 таблицы,
  каждая со своим task_id, суммарно ~35с

ПОСЛЕ repair (CONSISTENCY ONE на scylla3):
  dev-repair-demo-0001-00000: 20
  dev-repair-demo-0001-00250: 20
  dev-repair-demo-0001-00499: 20     <- repair синхронизировал
```

**Прогон B** (повтор, hinted handoff успел раньше проверки): все 3
партиции уже показывали `20/20/20` ДО explicit repair — на этом быстром
локальном docker-compose кластере hinted handoff иногда успевает
доставить весь объём ЕЩЁ ДО того, как скрипт успевает проверить состояние
(таймингом между рестартом узла и первой проверкой управлять точно
невозможно без штатной команды `nodetool disablehandoff` — в этой сборке
её нет вообще, см. полный список nodetool-команд). `nodetool cluster
repair` в обоих прогонах отработал синхронно и завершился реальной
сводкой (task_id + время старта/финиша на каждую таблицу) — repair
выполнил свою работу независимо от того, застало ли расхождение измерение
или нет (это ожидаемое поведение anti-entropy: repair — идемпотентная
синхронизация, "нечего чинить" — тоже валидный, быстро завершающийся
результат).

Кластер в обоих прогонах вернулся к `3xUN` (проверено `nodetool status` в
конце скрипта) — Task 5+ переиспользуют этот кластер без пересоздания.

#### Ассерты и живой результат

`compaction -scenario twcs`: `twcs_sstables_per_window >= 1` на всех 3
узлах — **14/14 окон** дали новый sstable, `EXIT 0`.
`compaction -scenario tombstones`: удаление подтверждено на 20 партициях
(`before-48 == after`) **и** `tombstones_scanned > 0` (=96, из живого
`gocql.Tracer`) — `EXIT 0`. Оба сценария воспроизведены дважды подряд с
идентичным результатом. `ops/repair-demo.sh` — синхронный, `nodetool
cluster repair --keyspace telemetry` завершился реальной сводкой в обоих
прогонах, кластер вернулся к `3xUN`.

### Стенд #4 — consistency levels + LWT (Paxos под капотом)

`scylladb/consistency-lwt/` (свой Go-модуль, `-scenario cl-latency|lwt`,
`env SCYLLA_HOSTS`) — не создаёт новых материализованных представлений
(этот слот серии переориентирован на consistency levels/LWT — тема,
которую бриф Task 5 ставит глубже, чем изначально планировавшиеся
materialized views/secondary indexes; последние остаются кандидатом для
более позднего стенда серии, если появится отдельная задача). Своя
таблица `telemetry.cl_bench` (латентность по CL) и `telemetry.counters_lwt`
(LWT) — `readings`, `readings_twcs` и остальные таблицы предыдущих
стендов не трогаются.

**Граница с серией «Транзакции и изоляция».** Та серия
(`digital-cookbook/transactions/kv-document/go/scylla.go`) уже показала
LWT на ScyllaDB под углом ГАРАНТИЙ изоляции: 16 воркеров бронируют одно
место, `INSERT ... IF NOT EXISTS` против обычного `INSERT` — ровно один
воркер получает `applied=true`, демонстрация линеаризуемости на
single-node кластере (RF=1). Этот стенд идёт ГЛУБЖЕ — не "что гарантирует
LWT", а "чем это стоит и как это работает изнутри": реальный 3-узловой
RF=3 кластер, клиентская латентность LWT против обычной записи (Paxos —
несколько раундов round-trip), поведение под конкуренцией за один
партиционный ключ, и что реально показывают серверные CAS-метрики Scylla.

```bash
export MSYS_NO_PATHCONV=1
docker run --rm --network scylla-cookbook-net \
  -v "$(pwd):/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app/consistency-lwt \
  -e SCYLLA_HOSTS=scylla1:9042,scylla2:9042,scylla3:9042 golang:1.26 \
  sh -c 'go run . -scenario cl-latency -n 20000'

docker run --rm --network scylla-cookbook-net \
  -v "$(pwd):/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app/consistency-lwt \
  -e SCYLLA_HOSTS=scylla1:9042,scylla2:9042,scylla3:9042 golang:1.26 \
  sh -c 'go run . -scenario lwt -n 5000 -contenders 16'
```

#### `-scenario cl-latency` — ONE/LOCAL_QUORUM/QUORUM/ALL, живые числа

Для каждого CL — 20000 записей + 20000 чтений в `cl_bench` (свежий ключ на
каждую операцию, читаем ровно тот ключ, который только что записали —
никакой гонки с чужими записями), клиентские p50/p99 отдельно на запись и
на чтение. Живой прогон (2026-07-11, idle-кластер — никакой параллельной
нагрузки):

```
CL              write_p50  write_p99   read_p50   read_p99 combined_p50 combined_p99
ONE                  1418       1508       1345       1506         1390         1507
LOCAL_QUORUM         1490       1718       1497       1726         1493         1722
QUORUM               1503       2011       1510       2084         1507         2047
ALL                  1497       1641       1502       1641         1500         1641
```

(микросекунды; полный вывод — `../scratchout/cl.txt`)

**Честно: на этом idle однохостовом кластере разница между CL — единицы
процентов, не порядки.** `combined_p50` растёт от `ONE` (1390µs) к `ALL`
(1500µs) — те же ~110µs (~8%), которые и предсказывает бриф ("на idle-
кластере разница может быть мала — если так, честно фиксировать малую
разницу, не преувеличивать"). Причина ожидаема: все 3 реплики физически
на одной машине за одним Docker-мостом — разница между "подождать 1
реплику" и "подождать все 3" на локальном loopback-по-сути соединении не
успевает вырасти до заметной величины (в отличие от кластера, разнесённого
по разным хостам/датацентрам, где RTT между узлами реален и заметен).
`LOCAL_QUORUM` и `QUORUM` дали ПОЧТИ идентичные числа (1493/1507µs
combined) — ожидаемо: keyspace `telemetry` в одном ДЦ (`datacenter1`,
Task 1), кворум из 3 реплик = 2 реплики что для `LOCAL_QUORUM`, что для
`QUORUM` — архитектурно один и тот же порог, разница в цифрах (14µs) —
чистый шум измерения.

**Ассерт `lat[ALL] >= lat[ONE]` (combined write+read p50) — держится
честно, без подгонки**: `1.500ms >= 1.390ms`, `EXIT 0`. Порядок ONE <
LOCAL_QUORUM ≈ QUORUM < ALL воспроизводится монотонно по всем 4 уровням —
дельты малы, но направление верное на каждом шаге.

#### `-scenario lwt` — LWT vs plain, contention, CAS-метрики

Живой прогон (2026-07-11, воспроизведено дважды подряд с идентичной
качественной картиной; полный вывод обоих прогонов — `../scratchout/lwt.txt`):

```
plain: p50=1.496ms p99=1.695ms ошибок=0
lwt:   p50=4.924ms p99=6.611ms ошибок=0 applied=5000/5000
ratio lwt_p50/plain_p50 = 3.29x

Contention: 16 горутин x 312 попыток = 4992 суммарных CAS-попыток на ключ "contended-key"
contention CAS: попыток=4992 (applied=312, failed=4680, ошибок-транспорта=0)
contention CAS latency: p50=78.85ms p99=91.24ms
contended_failed_fraction = 0.9375 (93.8%)
```

**LWT в ~3.3 раза дороже plain-записи** (4.92ms против 1.50ms, p50) — это
цена Paxos-раунда (prepare-propose-commit против одного write round-trip)
поверх того же кластера/сети, где сама разница между CL измеряется
единицами процентов (см. выше) — Paxos-накладные расходы НА ПОРЯДОК
заметнее, чем разница между consistency levels. Под искусственной
конкуренцией (16 горутин без backoff бьют в один и тот же партиционный
ключ read-then-CAS) латентность одной CAS-попытки выросла до ~79-91ms
(контраст с ~5ms latency LWT на бесконфликтных ключах) — Paxos на занятом
ключе сериализует конкурентов, а не параллелит их — и **93.75% попыток
завершились `applied=false`** (4680 из 4992): без retry-логики (её
намеренно нет в этом сценарии — цель показать сырую конкуренцию, а не
production-паттерн повторных попыток) абсолютное большинство "наивных"
CAS проигрывает гонку за один ключ.

**Важная оговорка (по ревью) — `93.75%` НЕ общая вероятность конфликта
LWT, это структурный артефакт КОНКРЕТНОГО contention-цикла.** Цикл
намеренно наивный: без jitter/backoff, все `-contenders` горутин читают
одно и то же текущее значение и почти синхронно шлют CAS одним
лок-степ-раундом — Paxos-сериализация на занятом ключе даёт РОВНО одного
победителя за раунд, откуда аналитически `failed_fraction ≈
(contenders-1)/contenders`: для `-contenders 16` это `15/16 = 0.9375` —
ровно наблюдаемое число. Формула проверена живьём с другим числом
конкурентов на этом же кластере: `-contenders 8` дал `175/200 = 7/8 =
0.8750`, `-contenders 4` дал `75/100 = 3/4 = 0.7500` — доля меняется по
формуле вместе с `-contenders`, а не остаётся константой `93.75%`. При
джиттере/backoff или несинхронном тайминге клиентов конкретный процент
был бы другим — это не архитектурное свойство LWT, а следствие того, как
именно сконструирован этот учебный цикл. **Вывод, который переживает эту
оговорку:** под высокой конкуренцией на ОДИН партиционный ключ LWT тратит
абсолютное большинство CAS-попыток впустую — это и есть реальная цена
конкуренции за Paxos; точный процент — функция числа конкурентов и
тайминга конкретного теста, не константа самого LWT.

**Асcерты `lwt_latency > plain_latency` и `contended_failed_fraction > 0`
— оба `OK`, `EXIT 0`**, воспроизведено дважды подряд с близкими числами
(ratio 3.27x/3.29x, failed fraction 93.75%/93.75%).

#### Серверные CAS-метрики `:9180/metrics` — живая находка, HELP-текст `cas_prune` вводит в заблуждение

**Правка по ревью: изначальная формулировка "исчерпывающий поиск нашёл
ровно ОДИН counter" была неточной** — `grep -i cas` находит СЕМЬ
метрик-семейств, из них ДВЕ (не одна) counter'а годятся для дельты
применений. Полный список, живьём (контейнер на сети
`scylla-cookbook-net`, `curl -s http://<container-ip>:9180/metrics |
grep -i cas` — на loopback/`localhost` внутри контейнера порт `9180` не
слушает, нужен собственный IP контейнера в сети):

```
scylla_storage_proxy_coordinator_cas_background          gauge      — "сколько сейчас выполняется" после ответа, не для before/after
scylla_storage_proxy_coordinator_cas_foreground           gauge      — "сколько ещё не завершилось", не для before/after
scylla_storage_proxy_coordinator_cas_prune                counter    — см. ниже, HELP вводит в заблуждение
scylla_storage_proxy_coordinator_cas_total_operations     counter    — HELP буквально точен, корроборирует cas_prune
scylla_storage_proxy_coordinator_cas_write_latency         histogram — латентность, не счёт операций
scylla_storage_proxy_coordinator_cas_write_latency_summary summary   — латентность, не счёт операций
```

(`replicas` в HELP-тексте несвязанных `view_updates`-метрик тоже содержит
подстроку `cas` — ложное совпадение `grep -i cas`, отфильтровано вручную,
не входит в список выше). Отдельного счётчика "CAS отклонён"/"condition
not met" в этой сборке по-прежнему НЕТ — ни `cas_prune`, ни
`cas_total_operations` не различают `applied=true` от `applied=false`
(проверено `grep -iE 'cas|applied|reject|condition'` по всем
`# HELP`-строкам — такого счётчика нет).

Из двух counter'ов, годных для дельты применений:

- **`scylla_storage_proxy_coordinator_cas_prune`** — официальный
  HELP-текст: *"how many times paxos prune was done after successful cas
  operation"* — по буквальному чтению должен считать только УСПЕШНЫЕ
  (`applied=true`) CAS.
- **`scylla_storage_proxy_coordinator_cas_total_operations`** — HELP-текст:
  *"number of total paxos operations executed (reads and writes)"* —
  БУКВАЛЬНО точное название для того, что здесь измеряется (число
  выполненных Paxos-раундов), в отличие от `cas_prune`. Живьём его сырое
  значение РОВНО совпадает с `cas_prune` на КАЖДОМ шарде по отдельности
  (не только в сумме по кластеру) — подтверждено двумя независимыми
  живыми прогонами `-scenario lwt` после фикса (`-contenders 8, n=200`:
  `cas_prune` delta `401` = `cas_total_operations` delta `401`;
  `-contenders 4, n=100`: `cas_prune` delta `201` = `cas_total_operations`
  delta `201`). Используется как КОРРОБОРИРУЮЩЕЕ подтверждение вывода про
  `cas_prune`, а не как независимый альтернативный источник данных — сам
  факт точного совпадения на каждом шарде означает, что оба counter'а
  инкрементируются в одном и том же месте кода Paxos-координатора.

**Живой прогон опровергает буквальное чтение HELP `cas_prune`.** Дельта
`cas_prune` (сумма по всем узлам/шардам, до/после сценария `lwt`) —
**`9993`**, что РОВНО совпадает с суммой ВСЕХ выполненных Paxos-раундов
за прогон, а не только успешных: `5000` (LWT `INSERT ... IF NOT EXISTS`,
applied=5000/5000) `+ 1` (инициализация `contended-key`) `+ 4992`
(попытки contention, из них applied=312 И failed=4680) `= 9993`.
Воспроизведено дважды подряд, оба раза точное совпадение (`9993 = 9993`).
**Вывод: `cas_prune` (и, корроборирующе, `cas_total_operations`)
инкрементируется на каждый ЗАВЕРШЁННЫЙ раунд Paxos (коммит произошёл —
неважно, применилось ли в итоге условие `IF`), а не только на успешные по
условию CAS** — Paxos всегда доводит раунд до коммита (в т.ч. коммитит
"не изменилось" при `applied=false`); HELP-текст `cas_prune` описывает
механизм неточно, HELP-текст `cas_total_operations` — точно.

Дополнительно сняты (не CAS-специфичные, но релевантные) счётчики
`scylla_cql_inserts{conditional="yes"}` и `scylla_cql_updates{conditional="yes"}`
— считают число CQL-запросов С условием (`IF`), пришедших на coordinator,
НЕЗАВИСИМО от исхода `applied`. За прогон: `delta(cql_inserts
conditional=yes) = 5001` (5000 LWT `INSERT` + 1 инициализация),
`delta(cql_updates conditional=yes) = 4992` (все contention-попытки,
`UPDATE ... IF val=?`) — оба совпадают РОВНО с ожидаемым числом запросов
по конструкции сценария, независимое подтверждение того же вывода.

#### Честные ограничения (специфика этого стенда)

- **Малые дельты между CL на idle однохостовом кластере — не архитектурный
  предел, а следствие топологии стенда.** Все 3 узла — контейнеры на одном
  Docker-хосте; RTT между "локальными" репликами исчезающе мал по сравнению
  с реальным multi-host/multi-DC развёртыванием, где разница между `ONE` и
  `ALL` (и тем более между `LOCAL_QUORUM`/`QUORUM` в реальной гео-
  репликации, Task 6) будет заметно больше. Числа этого стенда — честные
  измерения ИМЕННО этой топологии, не обобщение "разница между CL всегда
  мала".
- **`LOCAL_QUORUM` == `QUORUM` по конструкции этого стенда, не в общем
  случае.** Один ДЦ (`datacenter1`) → кворум из 3 реплик = 2 реплики что
  локально, что глобально. Разница проявится только при 2+ ДЦ (гео-
  репликация, Task 6 серии) — там `LOCAL_QUORUM` не ждёт удалённый ДЦ,
  `QUORUM`/`EACH_QUORUM` — ждут.
- **Contention-тест — без retry и без jitter/backoff намеренно, поэтому
  `contended_failed_fraction` — структурная, а не вероятностная величина.**
  Production-код с LWT почти всегда оборачивает CAS в retry-цикл
  (перечитать значение, повторить `IF`) и вносит случайный разброс по
  времени между попытками — здесь оба сознательно ИСКЛЮЧЕНЫ, чтобы
  показать сырую механику "один лок-степ-раунд — один победитель", а не
  замаскировать её под "в итоге всё равно применится через N попыток".
  Именно поэтому наблюдаемая доля — `≈(contenders-1)/contenders`, функция
  параметра `-contenders` и тайминга ЭТОГО теста (см. оговорку выше), а
  НЕ универсальная вероятность конфликта LWT.
- **`cas_prune` и `cas_total_operations` — рабочие, но не документированные
  как "исход" метрики.** Годятся для подтверждения "раунды Paxos реально
  происходят и их количество совпадает с ожиданием" (оба counter'а
  совпадают между собой 1:1 на каждом шарде), но НИ ОДИН не различает
  applied/failed на сервере — это по-прежнему только клиентский сигнал
  (см. `applied` из ответа `ScanCAS`/`MapScanCAS`).

### Стенд #5 — tablets + multi-DC (второй, отдельный кластер)

`scylladb/compose/multidc.yml` поднимает ВТОРОЙ, ОТДЕЛЬНЫЙ живой кластер —
2 датацентра (`DC1`, `DC2`) x 2 узла = 4 узла (`dc1a`,`dc1b`,`dc2a`,`dc2b`),
`GossipingPropertyFileSnitch` (`--endpoint-snitch GossipingPropertyFileSnitch
--dc DC1|DC2 --rack RACK1` — прямые CLI-флаги `scylla`, без монтирования
`cassandra-rackdc.properties`), сеть `scylla-multidc-net`, `--smp 1 --memory 2G
--overprovisioned 1` на узел. Оба сида (`--seeds=dc1a,dc2a`) перечислены у
ВСЕХ 4 узлов — иначе DC2 поднимается отдельным кластером, не сливается
gossip'ом с DC1 в один логический кластер с `NetworkTopologyStrategy` на 2 ДЦ.
`dc1a` опубликован на `localhost:9043` (не `9042` — не конфликтует с
одиночным ДЦ). **Одиночный ДЦ-кластер Task 1 (`compose/compose.yml`,
`scylla1/2/3`, сеть `scylla-cookbook-net`) в этой задаче НЕ трогается** — оба
кластера работают параллельно на одном хосте (62.8 GB RAM, `4x2GB` + уже
работающие `3x2GB` — укладывается с большим запасом).

`scylladb/topology/` (свой Go-модуль, `-scenario multidc|tablets`, `env
SCYLLA_HOSTS` — CSV ВСЕХ узлов multi-DC кластера) + `scylladb/ops/topology-demo.sh`.

```bash
export MSYS_NO_PATHCONV=1

docker run --rm --privileged alpine sh -c "echo 1048576 > /proc/sys/fs/aio-max-nr"
docker compose -f compose/multidc.yml up -d
until docker exec dc1a nodetool status 2>/dev/null | grep -cE '^UN' | grep -q 4; do sleep 5; done
docker exec dc1a nodetool status

docker run --rm --network scylla-multidc-net \
  -v "$(pwd):/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app/topology \
  -e SCYLLA_HOSTS=dc1a:9042,dc1b:9042,dc2a:9042,dc2b:9042 golang:1.26 \
  sh -c 'go run . -scenario multidc -n 10000'

bash ops/topology-demo.sh | tee scratchout/tablets.txt

docker compose -f compose/multidc.yml down -v
```

#### `nodetool status` — живьём, 4×UN в 2 ДЦ (2026-07-11)

```
Datacenter: DC1
UN 172.23.0.2 ... e37531df-efb8-4cb9-b4fd-0fb72dff7100 RACK1
UN 172.23.0.4 ... 133b5cdf-0ba0-41a7-848e-2c077f0d08ee RACK1
Datacenter: DC2
UN 172.23.0.3 ... 72b6fd09-1bc9-4a9a-aed6-208353956c73 RACK1
UN 172.23.0.5 ... 554f0500-85ee-4e91-b037-6e48fb40f808 RACK1
```

#### `-scenario multidc` — репликация DC1→DC2 + LOCAL_QUORUM vs QUORUM

Координатор ЖЁСТКО закреплён в нужном ДЦ через `cluster.HostFilter =
gocql.DataCenterHostFilter("DC1"|"DC2")` (драйвер вообще не открывает пул
соединений к узлам другого ДЦ — надёжнее, чем полагаться на политику выбора
хоста), плюс `gocql.DCAwareRoundRobinPolicy(dc,
gocql.HostPolicyOptionDisableDCFailover)`. Keyspace `telemetry_mdc`
(`{'class':'NetworkTopologyStrategy','DC1':2,'DC2':2}`). Живой прогон
(2026-07-11, `-n 10000`, полный вывод — `scratchout/multidc.txt`):

```
-- Фаза A: 10000 записей LOCAL_QUORUM (координатор DC1, ждёт 2/2 реплик DC1) --
Записано: 10000/10000 (ошибок: 0), LOCAL_QUORUM write p50=1.486479ms p99=1.624333ms

-- Фаза B: чтение записанных строк LOCAL_QUORUM с координатором DC2 --
Прочитано из DC2 (LOCAL_QUORUM): совпало=10000, расхождение=0, ошибок чтения=0

-- Фаза C: 10000 НОВЫХ записей QUORUM (координатор DC1, ждёт кворум ВСЕХ 4 реплик -- минимум 3 из 4, значит минимум 1 подтверждение из DC2) --
Записано: 10000/10000 (ошибок: 0), QUORUM write p50=1.56438ms p99=1.76864ms

CL              write_p50  write_p99
LOCAL_QUORUM       1486us     1624us
QUORUM             1564us     1768us

OK: dc1_to_dc2_replication — все 10000 строк, записанных LOCAL_QUORUM в DC1, видны LOCAL_QUORUM-чтением из DC2
OK: lat[QUORUM] p50=1.56438ms >= lat[LOCAL_QUORUM] p50=1.486479ms (QUORUM платит за ожидание удалённого DC2)
```

**Репликация DC1→DC2 подтверждена: все 10000 строк**, записанные
`LOCAL_QUORUM` в DC1 (ждёт только 2 реплики DC1), сразу видны при чтении
`LOCAL_QUORUM` из DC2 (ждёт только 2 реплики DC2, но данные там уже есть —
координатор рассылает запись всем 4 репликам одновременно, `LOCAL_QUORUM`
лишь не ЖДЁТ подтверждения от удалённого ДЦ, а не откладывает саму отправку).
`QUORUM` (тот же координатор DC1, но нужно 3 из 4 реплик кластера — минимум
1 подтверждение от DC2) дороже `LOCAL_QUORUM` на **~78µs (~5%) по p50**, на
**~144µs (~9%) по p99** — направление верное (`QUORUM >= LOCAL_QUORUM`),
воспроизведено с `EXIT 0`.

**Честная оговорка про малую дельту** — та же, что и в Стенде #4: оба ДЦ
физически на одном Docker-хосте (`scylla-multidc-net` — один L2-мост), а не
на реальных разнесённых площадках. Cross-DC "латентность" здесь измеряет
цену ожидания ВТОРОГО округления gossip/coordinator-протокола внутри одного
хоста, а не настоящий межрегиональный RTT (обычно единицы–десятки
миллисекунд между реальными ДЦ) — направление эффекта (QUORUM дороже
LOCAL_QUORUM) архитектурно верно и воспроизведено, но абсолютная величина
дельты (~5-9%) — артефакт локальной топологии стенда, не оценка реальной
гео-репликации.

#### DC failover — живая находка + реальное поведение

`ops/topology-demo.sh` останавливает ОБА узла DC2 (`dc2a`,`dc2b`) —
`docker compose -f compose/multidc.yml stop dc2a dc2b` — полный отказ
датацентра. Порядок шагов стенда выбран НЕ произвольно: DC failover
запускается ПЕРВЫМ, пока оба узла DC2 ещё живы (нужен настоящий "упал весь
ДЦ из 2 узлов", а не "упал единственный оставшийся узел" — decommission
позже НЕОБРАТИМО убирает один узел DC2, поэтому его нельзя ставить раньше).

Живой прогон (`scratchout/tablets.txt`, шаг 2/6):

```
-- LOCAL_QUORUM (DC1) при упавшем DC2 --
Consistency level set to LOCAL_QUORUM.
 id                    | val
-----------------------+-----
 failover-local-quorum |   1
(1 rows)
LOCAL_QUORUM write rc=0 read rc=0

-- EACH_QUORUM при упавшем DC2 --
<stdin>:1:NoHostAvailable: (..., Unavailable('Error from server: code=1000 [Unavailable exception]
  message="Cannot achieve consistency level for cl EACH_QUORUM. Requires 2, alive 0"
  info={'consistency': 'EACH_QUORUM', 'required_replicas': 2, 'alive_replicas': 0}'))
EACH_QUORUM write rc=2

OK: LOCAL_QUORUM (DC1) отработал при упавшем DC2 (write rc=0, read rc=0)
OK: EACH_QUORUM корректно отказал при упавшем DC2 (rc=2)
```

`LOCAL_QUORUM` в DC1 продолжает работать без единой ошибки, пока весь DC2
недоступен (записывает и читает `failover-local-quorum` за одну попытку).
`EACH_QUORUM` (требует кворум **КАЖДОГО** ДЦ, включая мёртвый DC2) падает с
буквальной серверной ошибкой `Unavailable exception ... required_replicas:
2, alive_replicas: 0` — сервер сам называет причину, не тайм-аут. После
`docker compose -f compose/multidc.yml start dc2a dc2b` кластер синхронно
дожидается 4×UN (до 3 минут по циклу) — восстановление подтверждено живым
`nodetool status`.

#### tablets — распределение и живая миграция при decommission

> **Осознанный выбор кластера.** Демо миграции tablets намеренно прогоняется
> на этом _разовом_ multi-DC-кластере (а не на постоянном одиночном
> `scylla1/2/3`), потому что `nodetool decommission` физически выводит узел из
> кольца — на постоянном стенде это разрушило бы данные, нужные остальным
> стендам серии. Одиночный кластер за всё время стенда #5 не изменяется
> (проверено: после teardown он остаётся `3×UN`, `readings`=672000).

**Критически важная живая находка**: попытка `nodetool decommission dc2b`
на кластере, где keyspace `telemetry_mdc` объявлен с `DC2:2` (2 реплики в
ДЦ), **отказывает** — после ухода `dc2b` в DC2 остался бы 1 узел, а
keyspace требует 2 реплики там же:

```
$ docker exec dc2b nodetool decommission
error executing POST request ...: std::runtime_error (Decommission failed.
  See earlier errors (Unable to find new replica for tablet ... when draining
  {2dfe6fe5-...}. Consider adding new nodes or reducing replication factor.
  (nodes [b09e3956-...], replicas [2dfe6fe5-...:0, b09e3956-...:0, 4ee7d9a5-...:0, c3dc1123-...:0])).
```

Кластер после отказа остался ЦЕЛЫМ (4×UN, никакого частичного состояния) —
ScyllaDB сам защищает от decommission, ломающего заявленную репликацию.
Честный, реалистичный обходной путь (та же операция, которую в проде
делают перед выводом предпоследнего узла ДЦ) — **сперва понизить RF
затронутого ДЦ**:

```
ALTER KEYSPACE telemetry_mdc WITH replication = {'class':'NetworkTopologyStrategy','DC1':2,'DC2':1};
```

после чего `nodetool decommission dc2b` завершается успешно (`rc=0`).
Отдельный keyspace, специально созданный ПОД этот стенд, —
`telemetry_mdc_tablets` (`{'class':'NetworkTopologyStrategy','DC1':1,'DC2':1}`
— сразу RF=1/ДЦ, чтобы сама демонстрация decommission не требовала ALTER),
таблица `tbench`, 5000 детерминированных строк. Распределение читается
ЖИВЬЮ через `system.tablets` (схема этой сборки — live-проверено, `DESCRIBE
TABLE system.tablets`, см. `topology/main.go`): `replicas` — `list<frozen<
tuple<uuid,int>>>`, по одному элементу `(host_id, shard)` на реплику
tablet-диапазона (`last_token`); `host_id` каждого узла спрошен НАПРЯМУЮ у
самого узла (`SELECT host_id FROM system.local`, отдельная
`WhiteListHostFilter`-сессия на узел — без риска спутать источник ответа).
`nodetool tablets`/`nodetool cluster` из брифа в этой сборке (2026.2.0) НЕ
существуют (`nodetool help` не содержит `tablets`; `nodetool cluster` знает
только `repair|cleanup|snapshot`) — рабочий источник живьём, задокументированный
здесь по факту, `system.tablets` (тот же, что уже нашёл Стенд #3 для
`repair-demo.sh`).

Живой прогон (`scratchout/tablets.txt`, шаги 4/6 и 6/6, воспроизведено
дважды подряд — независимо ещё раз в `scratchout/tablets-before.txt` /
`tablets-after.txt` на отдельном прогоне ДО пересоздания кластера, с ДРУГИМИ
`host_id` и тем же качественным результатом):

```
ДО decommission (table_id=225c81a0-..., tablet_count=32, tablet-строк в system.tablets: 32):
   dc1a  : 16 tablet-реплик
   dc1b  : 16 tablet-реплик
   dc2a  : 16 tablet-реплик
   dc2b  : 16 tablet-реплик
   ИТОГО: 64 (= 32 tablet-строк x RF 2)

nodetool decommission dc2b: rc=0

ПОСЛЕ decommission (тот же table_id, 3 живых узла):
   dc1a  : 16 tablet-реплик
   dc1b  : 16 tablet-реплик
   dc2a  : 32 tablet-реплик
   ИТОГО: 64 (= 32 tablet-строк x RF 2)

OK: decommission dc2b завершился успешно (rc=0)
```

**Все 16 tablet-реплик, ранее лежавших на `dc2b`, мигрировали на `dc2a`** —
`dc2a` вырос ровно с 16 до 32 (было 50/50 между двумя узлами DC2 при
`DC2:1`, стало 100% на единственном оставшемся). `dc1a`/`dc1b` не тронуты
(DC1 decommission не затрагивал). Сумма реплик (64) неизменна ДО и ПОСЛЕ —
ни потерь, ни дублей, чистая миграция.

#### Ассерты и живой результат

`topology -scenario multidc`: `dc1_to_dc2_replication` (10000/10000
реплицировано) и `lat[QUORUM] >= lat[LOCAL_QUORUM]` (1.564ms >= 1.486ms) —
оба `OK`, `EXIT 0`. `ops/topology-demo.sh`: `4×UN в 2 ДЦ` подтверждено
дважды (до и после failover-цикла), `LOCAL_QUORUM доступен при упавшем DC2`
(`OK`), `EACH_QUORUM корректно отказал` (`OK`), `tablets_after_decommission
перераспределены` (`dc2a`: 16→32, `dc2b`: 16→0, сумма 64→64, `OK`). Все
живые прогоны — реальные числа этого запуска, ни один не подогнан.

#### Честные ограничения (специфика этого стенда)

- **Cross-DC "латентность" — не настоящий межрегиональный RTT.** Оба ДЦ на
  одном Docker-хосте, разница QUORUM/LOCAL_QUORUM (~5-9%) отражает
  дополнительный раунд внутри локальной сети, не реальную гео-задержку
  между площадками — направление эффекта архитектурно верно, абсолютная
  величина — нет (см. выше, та же оговорка, что и в Стенде #4 для CL).
- **RF telemetry_mdc понижен по ходу стенда (DC2: 2→1).** Это НЕ баг
  измерения, а честно задокументированная живая находка: decommission узла
  ДЦ несовместим с RF этого ДЦ, равным числу узлов ДО удаления — реальное
  защитное поведение ScyllaDB, не искусственное ограничение стенда.
- **Единый порядок шагов обязателен.** DC failover (нужны 2 живых узла DC2)
  должен идти ДО decommission (необратимо убирает один из них) — см.
  комментарий в начале `ops/topology-demo.sh`.
- **`nodetool tablets`/`nodetool cluster` из брифа не существуют в сборке
  2026.2.0** — рабочий источник, использованный стендом, `system.tablets`
  напрямую (та же находка, что уже была в Стенде #3).
- **Ресурсы: `--smp 1 --memory 2G` на узел (не 2/2G, как у одиночного ДЦ).**
  Экономия под параллельную работу с уже запущенным кластером Task 1 —
  латентности этого стенда (LOCAL_QUORUM/QUORUM в единицы мс) сопоставимы
  по порядку величины с CL-числами Стенда #4 (тот же хост, тот же класс
  контейнеров), несмотря на меньший `--smp`.
- **Multi-DC кластер полностью одноразовый.** По завершении демонстрации —
  `docker compose -f compose/multidc.yml down -v` (выполнено, подтверждено
  живьём: `docker ps` не показывает `dc1a/dc1b/dc2a/dc2b`, одиночный ДЦ-
  кластер Task 1 остаётся `3xUN`, `telemetry.readings=672000` без изменений).

### Стенд #6 — shard-aware драйверы (Go bench + Java зеркало)

> **Правка нумерации.** Заголовки «Стенд #6»/«Стенд #7» в предыдущей
> редакции README были перепутаны местами (черновой skeleton Task 1 не
> совпадал с финальным планом серии: `docs/superpowers/plans/
> 2026-07-11-scylladb-cookbook.md`, где Task 7 = Стенд #6 shard-aware
> драйверы, Task 8 = Стенд #7 эксплуатация). Заголовки приведены в
> соответствие плану здесь, в Task 7; тема эксплуатации ждёт заполнения в
> Task 8 (см. заглушку «Стенд #7» ниже).

#### Переработка дизайна: почему НЕ два драйвера в одном бинаре

Исходная идея стенда — импортировать upstream `github.com/gocql/gocql` И
форк `github.com/scylladb/gocql` под алиасами в одном Go-бинаре (аналогично
в Java: upstream `com.datastax.oss:java-driver-core` и форк
`com.scylladb:java-driver-core` на одном classpath) — **физически
невозможна**, и это подтверждено живьём, а не предположено:

- **Go.** Форк ScyllaDB слит с апстримом и самодекларирует ТОТ ЖЕ module
  path `github.com/gocql/gocql` (`go.mod` форка объявляет себя как
  `module github.com/gocql/gocql`, не `github.com/scylladb/gocql`) —
  подключается исключительно через `replace github.com/gocql/gocql =>
  github.com/scylladb/gocql v1.18.3` в `go.mod` (см. `drivers/go/go.mod`,
  тот же паттерн, что и во ВСЕХ Go-модулях серии начиная с `dataset/`). Два
  модуля с ОДИНАКОВЫМ module path не могут сосуществовать в одном `go.mod`
  — `go mod tidy` не различит upstream-версию и форк-версию, у обеих один
  и тот же путь импорта `github.com/gocql/gocql`.
- **Java.** Живая проверка (2026-07-11): `unzip -l
  java-driver-core-4.19.2.0.jar` (форк, скачан с Maven Central через
  прокси) показывает **единственный** top-level Java-пакет внутри —
  `com/datastax/...`. Ни одного класса под `com/scylladb/...`. Форк —
  патченный апстримный драйвер, опубликованный под СВОИМИ Maven-
  координатами (`groupId=com.scylladb`), но с ТЕМИ ЖЕ именами классов
  (`com.datastax.oss.driver.api.core.CqlSession` и т.д.), что и апстримный
  `com.datastax.oss:java-driver-core`. Положить оба jar на один classpath —
  гарантированный конфликт дублирующихся классов (какой jar реально
  загрузится для `CqlSession`, зависит от порядка classpath, а не от кода).
  Та же коллизия и в конфиг-неймспейсе: `reference.conf` форка объявляет
  корень `datastax-java-driver { ... }` — ТОТ ЖЕ, что у апстрима; Typesafe
  Config сливает `reference.conf`-ресурсы с одинаковым именем со всего
  classpath, так что конфиги форка и апстрима тоже не разъезжаются.

Оба случая — не архитектурная случайность, а следствие способа, которым
ScyllaDB поддерживает форки: патч поверх апстрима, публикуемый под другим
groupId/module path верхнего уровня, но с идентичным внутренним API/пакетами
— миграция «переключить драйвер» для пользователя тривиальна (замена
координаты зависимости, ноль изменений кода), но именно поэтому одновременно
на одном classpath/в одном go.mod оба сосуществовать не могут.

**Поэтому контраст «shard-aware vs нет» сделан ВНУТРИ одного (форкового)
драйвера через конфиг**, не через два драйвера — см. ниже.

#### Go bench (`drivers/go/`) — `HostSelectionPolicy` aware vs naive

`drivers/go/main.go`, флаг `-mode aware|naive`, ОДИН драйвер
`github.com/gocql/gocql` (= форк через `replace`, как и `dataset/`). N
точечных чтений случайных партиций `telemetry.readings` (`WHERE
device_id=? AND day=? LIMIT 1`, полный partition key good-модели Стенда
#1) — не мутирует `readings`:

- `-mode aware`: `gocql.PoolConfig.HostSelectionPolicy =
  gocql.TokenAwareHostPolicy(gocql.RoundRobinHostPolicy())` — ЭТО ЖЕ дефолт
  форка без явной настройки (`session.go:183` форка). `TokenAware`
  прокидывает `Token` запроса в `hostConnPool.Pick(token, qry)`
  (`connectionpool.go`) — форковый `connPicker` использует токен не только
  чтобы выбрать правильный узел (это умеет и апстримный token-aware), но и
  КОНКРЕТНОЕ shard-aware TCP-соединение на этом узле (соединения
  устанавливаются через shard-aware порт, каждое привязано к одному
  CPU-шарду сервера) — клиент бьёт прямо в шард, владеющий партицией.
- `-mode naive`: `gocql.RoundRobinHostPolicy()` — БЕЗ токена, узел выбирается
  по кругу; coordinator почти всегда вынужден пересылать запрос внутренним
  RPC на владеющий shard (свой или чужого узла) — тот же межшардовый
  hop, что описывал Стенд #2.

```bash
export MSYS_NO_PATHCONV=1
docker run --rm --network scylla-cookbook-net -v "$(pwd)/drivers/go:/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app \
  -e SCYLLA_HOSTS=scylla1:9042,scylla2:9042,scylla3:9042 golang:1.26 \
  sh -c "go run . -mode aware -n 100000" | tee scratchout/drv-aware.txt
docker run --rm --network scylla-cookbook-net -v "$(pwd)/drivers/go:/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app \
  -e SCYLLA_HOSTS=scylla1:9042,scylla2:9042,scylla3:9042 golang:1.26 \
  sh -c "go run . -mode naive -n 100000" | tee scratchout/drv-naive.txt
```

**Живой результат (2026-07-11, `-n 100000`, 1000 warmup-чтений не в
замере, последовательные точечные чтения, один клиент):**

| mode | throughput | p50 | p99 |
|---|---:|---:|---:|
| `aware` | **653.3 rows/s** | 1.520ms | **1.708ms** |
| `naive` | 639.7 rows/s | 1.569ms | 1.743ms |

`ratio throughput[aware]/throughput[naive] = 653.3/639.7 = 1.021` (aware
быстрее на **~2.1%** по throughput, p99 ниже на **~2.0%**). Повторный
независимый прогон (`-n 30000`, другой `-seed 99`, порядок наоборот — сперва
naive, потом aware — чтобы исключить смещение по порядку) подтвердил ТО ЖЕ
направление и тот же порядок величины: `aware` 655.3 rows/s / p50=1.520ms /
p99=1.636ms против `naive` 637.3 rows/s / p50=1.573ms / p99=1.748ms (ratio
1.028, ~2.8%). Направление устойчиво и не зависит от порядка запуска.

#### Java зеркало (`drivers/java/`, `DriversBench`) — shard-awareness ВКЛ/ВЫКЛ

`scylladb/java/pom.xml` — родительский реактор (`packaging=pom`, JDK 25,
модуль `../drivers/java`), ПЕРВЫЙ Java-реактор серии (Task 1-6 — только Go).
`DriversBench` — ОДИН драйвер `com.scylladb:java-driver-core:4.19.2.0`
(пин сверен живьём 2026-07-11 по `maven-metadata.xml` Maven Central, latest
на момент сборки).

**Реальный тумблер shard-awareness этой версии** (найден в распакованном
`reference.conf` форка, живьём): `advanced.connection.
advanced-shard-awareness.enabled` — по умолчанию `true` (дефолт форка,
секция добавлена ScyllaDB поверх апстримного дерева опций; "Overridable in a
profile: no" — общий на сессию, поэтому ВКЛ/ВЫКЛ реализованы как ДВЕ разные
`CqlSession`, не два профиля одной сессии).

> **Правка ревью (2026-07-11): typed-константа ЕСТЬ, предыдущая редакция
> ошибалась.** Первая версия этого README и javadoc `DriversBench`
> утверждали, что у ключа "нет typed-константы в
> `TypedDriverOption`/`DefaultDriverOption`". Ревьюер дизассемблировал живой
> `java-driver-core-4.19.2.0.jar` (`javap` по распакованным `.class`) и
> нашёл ОБЕ константы: `DefaultDriverOption.CONNECTION_ADVANCED_SHARD_AWARENESS_ENABLED`
> и одноимённую `TypedDriverOption<Boolean>` — тот же путь
> `advanced.connection.advanced-shard-awareness.enabled`. Переключатель
> переписан на типобезопасный программный билдер:
> `DriverConfigLoader.programmaticBuilder().withBoolean(DefaultDriverOption
> .CONNECTION_ADVANCED_SHARD_AWARENESS_ENABLED, enabled).build()` — вместо
> сырого HOCON-пути через `DriverConfigLoader.fromString(...)`. Заодно и
> лучше по коду (компилируемая проверка имени опции), и честнее по факту
> (константа реально есть и теперь используется).

Сборка (maven-assembly-plugin, `jar-with-dependencies`, `mainClass
tech.khorost.scylla.DriversBench`):

```bash
export MSYS_NO_PATHCONV=1
docker run --rm --network scylla-cookbook-net -v "$(pwd):/app" -v "$(pwd)/.m2cache:/root/.m2" \
  -w /app/java maven:3.9-eclipse-temurin-25 mvn -q -DskipTests package
```

**Правка пути запуска брифа.** `task-7-brief.md` даёт `-w
/app/java/drivers/java` — не существует (модуль лежит `scylladb/drivers/java`,
не `scylladb/java/drivers/java`, см. секцию Files брифа: `../drivers/java`
относительно `scylladb/java/`). Рабочая команда — `-w /app/drivers/java`
(при `-v "$(pwd):/app"`, `$(pwd)`=`scylladb/`):

```bash
docker run --rm --network scylla-cookbook-net -v "$(pwd):/app" -v "$(pwd)/.m2cache:/root/.m2" \
  -e SCYLLA_HOSTS=scylla1:9042 -w /app/drivers/java maven:3.9-eclipse-temurin-25 \
  sh -c 'java -jar target/*-with-dependencies.jar -mode on  -n 20000 -warmup 3000' | tee scratchout/drv-java-on.txt
docker run --rm --network scylla-cookbook-net -v "$(pwd):/app" -v "$(pwd)/.m2cache:/root/.m2" \
  -e SCYLLA_HOSTS=scylla1:9042 -w /app/drivers/java maven:3.9-eclipse-temurin-25 \
  sh -c 'java -jar target/*-with-dependencies.jar -mode off -n 20000 -warmup 3000' | tee scratchout/drv-java-off.txt
```

#### Честная находка: JVM-порядок бьёт сигнал сильнее самого shard-awareness

Первая версия `DriversBench` запускала ВКЛ и ВЫКЛ последовательно ВНУТРИ
ОДНОГО процесса (`java -jar ... ` без `-mode`, оставлено как режим `-mode
both` для демонстрации самой находки). Живой прогон (`-n 20000, -warmup
3000`) дал throughput[ON]=3258 rows/s < throughput[OFF]=4028 rows/s — ON
ХУЖЕ на 19%. Диагностика (`-swap`, тот же прогон с обратным порядком: сперва
OFF, потом ON) дала ПРОТИВОПОЛОЖНЫЙ результат: throughput[OFF]=3175 < throughput[ON]=4001
— **ВТОРОЙ по счёту прогон систематически быстрее ПЕРВОГО, независимо от
режима** (JIT ещё дозревает/буферы и соединения ещё не прогреты на первом
прогоне процесса) — swing ~26-30%, что на порядок больше самого
shard-awareness эффекта (эффект утоплен в шуме конкретно этой методики).
_(Эти конкретные числа — из первой версии стенда, до правки Finding 2 про
consistency level ниже: тогда бенч ещё наследовал дефолт драйвера
`LOCAL_ONE`, не `QUORUM`. Оставлены как есть — сама находка про JIT-порядок
от CL не зависит, качественный эффект "второй прогон быстрее" тот же на
любом CL.)_

**Исправление**: `-mode on` и `-mode off` — ДВА НЕЗАВИСИМЫХ процесса
(`java -jar` запускается заново на каждый режим), как у Go-бенча (`-mode
aware|naive`, два `go run`). Каждый процесс стартует с холодным JIT
одинаково — порядок запуска (какой контейнер выполнился первым) больше не
влияет на то, какой РЕЖИМ оказался быстрее.

**Живой результат (изолированные процессы, 2026-07-11, `-n 20000`, 3000
warmup, `CL=QUORUM` — см. Finding 2 ниже, число обновлено после правки
ревью):**

| mode | throughput | p50 | p99 |
|---|---:|---:|---:|
| `ON`  (shard-aware) | 2122.0 rows/s | 451us | 721us |
| `OFF` (наивно) | **2141.7 rows/s** | 447us | **694us** |

`ratio throughput[ON]/throughput[OFF] = 2122.0/2141.7 = 0.991` (OFF
быстрее ON на **~0.9%**). Повторный независимый прогон (`-seed 99`, те же
изолированные процессы): `OFF` 2152.8 rows/s / p50=444us / p99=686us против
`ON` 2130.3 rows/s / p50=450us / p99=710us (ratio 0.990, OFF быстрее на
**~1.1%**). Направление устойчиво в ОБОИХ независимых прогонах — но теперь
устойчиво в ПОЛЬЗУ OFF, не ON.

**Почему направление у Java перевернулось по сравнению с первой редакцией
этого README.** Первая редакция мерила ON/OFF на дефолтном CL драйвера
(`LOCAL_ONE`) и получала ON быстрее OFF на 0.7-1.2%. После правки Finding 2
(ниже) бенч ставит `CL=QUORUM` явно — как у Go, чтобы нагрузки были реально
идентичны, а не только "похожи". На `QUORUM` каждый запрос ждёт ответа от
большинства реплик (кросс-узловой round-trip), что само по себе на порядок
дороже одного internal shard-hop — тот самый ~1% выигрыш от shard-aware
маршрутизации, который был виден на `LOCAL_ONE`, на `QUORUM` тонет в
дисперсии кросс-узловых round-trip'ов и даже разворачивается в
противоположную сторону (в пределах шум-масштаба, не статистически значимо
при `n=20000`, но воспроизводимо по знаку в обоих прогонах). Это ЧЕСТНЫЙ
результат честного фикса, а не регрессия стенда — и он сам по себе
показательный: consistency level, а не только shard-routing, доминирует над
итоговой латентностью там, где кросс-узловой round-trip есть в принципе.

#### Сводная таблица и честная оценка эффекта

| | Go aware/ON | Go naive/OFF | ratio | Java ON | Java OFF | ratio |
|---|---:|---:|---:|---:|---:|---:|
| throughput (rows/s) | 653.3 | 639.7 | 1.021 | 2122.0 | 2141.7 | 0.991 |
| p50 | 1.520ms | 1.569ms | — | 451us | 447us | — |
| p99 | 1.708ms | 1.743ms | — | 721us | 694us | — |

**Честно: эффект на `--smp 2` (2 shard/узел, 3 узла) МОДЕСТНЫЙ и у Java, и
у Go, но теперь в РАЗНЫХ направлениях.** Go (тоже `CL=QUORUM`) показывает
устойчивое ~2-3% преимущество aware-режима (воспроизведено в 2 независимых
прогонах с разными seed и порядком). Java, теперь тоже на `CL=QUORUM`
(после правки Finding 2), показывает устойчивый, но противоположный сдвиг
~0.9-1.1% В ПОЛЬЗУ OFF — в обоих независимых прогонах OFF обгоняет ON.
Абсолютная величина в обоих языках — единицы процентов, то есть эффект
shard-aware маршрутизации на `--smp 2` В ЛЮБОМ случае модестный: промах
мимо своего shard'а на узле с ВСЕГО 2 shard'ами стоит недорого (один
internal RPC-hop до соседнего из двух шардов, или между узлами кластера,
вероятность которого тоже невелика при равномерном round-robin по 3
узлам), а на `QUORUM` эта разница вообще тонет в кросс-узловом round-trip
самого CL. На production-кластерах с 8-32+ shard'ами на узел промах мимо
shard'а статистически куда вероятнее (1 верный shard из N, а не из 2), и
при менее строгом CL (`LOCAL_ONE`/`LOCAL_QUORUM` без кросс-DC round-trip)
ожидаемый эффект от shard-aware маршрутизации пропорционально БОЛЬШЕ, чем
измеренные здесь 1-3%. Этот стенд намеренно НЕ подгоняет цифры под
ожидаемо-эффектный результат — воспроизводит, что реально измеряется на
доступном 2-shard/node стенде при честно одинаковой (`QUORUM`) нагрузке
между Go и Java, и объясняет, почему направление и величина здесь скромные
и (для Java) отличаются от Go.

**Ассерт (обновлён после Finding 2).** `aware_throughput >= naive_throughput`
(Go, `CL=QUORUM`) выполняется на реальных числах в обоих независимых
прогонах. Для Java на `CL=QUORUM` симметричный ассерт `throughput[ON] >=
throughput[OFF]` **НЕ выполняется** — оба независимых прогона показывают
`OFF` немного быстрее `ON` (~0.9-1.1%). Это НЕ подгонялось: до правки CL
(на `LOCAL_ONE`) знак был обратный (ON быстрее). Честный вывод — на `--smp
2` направление shard-awareness эффекта у Java при `CL=QUORUM`
неустойчиво/отрицательно и находится в пределах шум-масштаба, у Go
(тот же `CL=QUORUM`) направление устойчиво положительное. Разница по
языкам — реальное наблюдение этого стенда, не расхождение методики (обе
нагрузки, включая CL, теперь идентичны).

#### Честные ограничения (специфика этого стенда)

- **Эффект на `--smp 2` модестный (0.9-3%) и после правки Finding 2 (CL
  честно = `QUORUM` у обоих языков) у Java НАПРАВЛЕНИЕ развернулось.** Go
  устойчиво показывает aware/ON быстрее (~2-3%, 2 независимых прогона).
  Java на `QUORUM` устойчиво показывает OFF немного быстрее ON (~0.9-1.1%,
  2 независимых прогона) — обратно направлению, которое было видно на
  дефолтном `LOCAL_ONE` до правки CL. Оба направления — реальные измерения
  на доступном 2-shard/node стенде, не подгонка; см. разбор в «Сводная
  таблица и честная оценка эффекта» выше про то, почему CL доминирует над
  shard-routing эффектом при `--smp 2`. Это ОЖИДАЕМО согласно самому брифу
  задачи (модестный эффект на малом числе shard'ов), не недостаток
  измерения.
- **Java single-JVM-процесс — JVM-порядок бьёт сигнал сильнее самого
  shard-awareness.** Первая версия `DriversBench` (режим `-mode both`,
  оставлен в коде для демонстрации находки) мерила ВКЛ/ВЫКЛ подряд в ОДНОМ
  процессе — swing от порядка запуска (~26-30%) на порядок превысил
  реальный эффект (~1%). Канонические числа README — из `-mode on`/`-mode
  off`, ДВУХ независимых процессов (как у Go). У Go этой проблемы в принципе
  нет — бинарь компилируется заранее, нет JIT-прогрева рантайма.
- **Правка ревью: Java-бенч раньше молча наследовал `LOCAL_ONE`, Go —
  `QUORUM` явно, что делало заявление «нагрузка идентична» неточным.**
  `../go/main.go` ставит `cluster.Consistency = gocql.Quorum` явно, а
  `DriversBench` (до этой правки) вообще не трогал consistency level —
  наследовал дефолт драйвера `LOCAL_ONE`. Исправлено: каждый `BoundStatement`
  теперь получает `.setConsistencyLevel(DefaultConsistencyLevel.QUORUM)`, как
  у Go. Побочный эффект честного фикса — направление ON/OFF у Java
  развернулось (см. выше); это ожидаемо и объяснено, не скрыто.
- **Java driver выдаёт рантайм-WARNING про `sun.misc.Unsafe`/native-access
  (JDK 25).** `io.netty.util.internal.PlatformDependent0` и
  `com.kenai.jffi.internal.StubLoader` используют terminally-deprecated /
  restricted API — ожидаемо для нативно-интеропных библиотек этого
  поколения на новых JDK, не ошибка сборки/подключения (`BUILD SUCCESS`,
  все чтения успешны, `errs=0` в каждом прогоне).
- **`java-driver-core` при первом контакте предупреждает "You specified
  datacenter1 as the local DC, but some contact points are from a
  different DC" / "Unable to determine broadcast RPC port".** Живая
  проверка: результат чтений (`errs=0` во всех прогонах) не пострадал —
  предупреждение относится к метаданным ДО завершения полного discovery
  топологии (единственная нода-контакт `scylla1:9042` ещё не отдала
  `host_id`/DC на первом RPC), сама сессия дальше работает штатно.
- **Путь `-w` в `task-7-brief.md` для Java-запуска не совпадает с реальной
  структурой каталогов.** Бриф: `/app/java/drivers/java`; фактически
  модуль `scylladb/drivers/java` (сиблинг `scylladb/java/`, не потомок) —
  рабочий путь `/app/drivers/java` (см. пример выше, поправлено здесь).
- **Заголовки «Стенд #6»/«Стенд #7» в README были перепутаны местами
  до этой задачи** (см. правку в начале раздела) — унаследовано от
  чернового skeleton Task 1, не от текущего плана серии.

### Стенд #7 — эксплуатация (nodetool/monitoring/backup/Alternator)

Последний контентный стенд серии: три эксплуатационных среза поверх ТОГО ЖЕ
основного кластера (`scylla1/2/3`, keyspace `telemetry`, 672000 строк
`readings` из Task 1) — Prometheus-мониторинг, backup/restore через
`nodetool snapshot`/`nodetool refresh` и DynamoDB-совместимый доступ
(Alternator). Все три демо СПРОЕКТИРОВАНЫ так, чтобы НИ РАЗУ не пересоздать
`scylla1/2/3` и не тронуть `readings` — подробности ниже в каждом разделе.

Файлы: `ops-stand/` (Go, `-scenario alternator`), `compose/monitoring.yml` +
`compose/prometheus.yml`, `ops/ops-demo.sh` (backup/restore + запуск
Alternator-сценария).

#### Monitoring — Prometheus скрейпит `:9180/metrics` со всех трёх узлов

`compose/monitoring.yml` добавляет ОДИН сервис (`prometheus`, образ
`prom/prometheus:v3.7.3`) к УЖЕ СУЩЕСТВУЮЩЕЙ сети `scylla-cookbook-net`
(`external: true` — сеть уже создана `compose/compose.yml`). Поднимать
СТРОГО таргетированно:

```bash
docker compose -f compose/compose.yml -f compose/monitoring.yml up -d prometheus
```

Живая проверка (2026-07-11): `docker ps` ДО и ПОСЛЕ показывает одинаковые
`Up N hours` у `scylla1/2/3` — команда добавила только `scylla-prometheus`,
никого не пересоздала.

Число `scylla_*`-метрик на узел (через `curlimages/curl` на
`scylla-cookbook-net`, ИЗНУТРИ сети — см. находку Стенда #2 про
`127.0.0.1`/connection-refused, тот же паттерн у `:9180`):

```
scylla1: 4360 scylla_* metrics
scylla2: 4352 scylla_* metrics
scylla3: 4100 scylla_* metrics
```

Prometheus `api/v1/targets` — все три таргета `"health":"up"` сразу после
подъёма (первый скрейп прошёл раньше первого запроса к API).

Ключевые значения (scylla1, живой прогон):

| Метрика | Значение |
|---|---|
| `scylla_database_total_reads{...}` (сумма по всем `class=`/`shard=`) | 880862 |
| `scylla_database_total_writes{...}` (сумма) | 297740 |
| `scylla_compaction_manager_backlog{shard=0\|1}` | 0.0 (idle-кластер — честно, backlog реально нулевой, не «не считается») |
| `scylla_storage_proxy_coordinator_cas_total_operations{...}` (сумма, накопленное к моменту снятия метрик) | 3820 |

`scylla_compaction_manager_backlog=0` — не ошибка сбора: между стендами
серии кластер успевает докомпактиться, backlog у Scylla считается per-shard
и на idle-нагрузке легитимно нулевой (см. Стенд #3 про TWCS/compaction —
там backlog ненулевой именно ПОД активной записью).

#### Backup/restore — `nodetool snapshot` + `nodetool refresh` на scratch-таблице

`ops/ops-demo.sh`, часть A. Работает ИСКЛЮЧИТЕЛЬНО с отдельной таблицей
`telemetry.ops_backup_demo` (50 строк, создаётся и удаляется самим
скриптом) — `readings` НИ РАЗУ не упоминается в DML/DDL этой части.

Цикл: `CREATE` + загрузка 50 строк → `nodetool flush` → `nodetool snapshot
--keyspace-table-list telemetry.ops_backup_demo -t backupdemo1` →
`TRUNCATE` (симуляция потери данных) → копирование sstable-файлов снапшота
в `upload/` той же табличной директории → `nodetool refresh --keyspace
telemetry --table ops_backup_demo` → проверка count.

Живой прогон (2026-07-11):

```
ops_backup_demo: загружено, count=50 (ожидание: 50)
cf_id: b1741790-7ce0-11f1-bd90-8158da91d790
snapshot path: data/telemetry/ops_backup_demo-b17417907ce011f1bd908158da91d790/snapshots/backupdemo1/
snapshot files: 245
snapshot size:  1.1M
после TRUNCATE: count=0 (ожидание: 0)
скопировано файлов в upload/: 243
после restore: count=50
ASSERT OK: restore_count == original_count (50)
telemetry.readings count=672000 (ожидание: 672000, неизменно)
```

**Restore-механизм — честно, что именно сработало и почему НЕ
`sstableloader`.** `nodetool refresh` — штатная nodetool-команда «загрузить
sstable без рестарта из `upload/`»: снапшот физически лежит в ТОЙ ЖЕ
табличной директории (`data/telemetry/<table>-<cf_id>/snapshots/<tag>/`),
поэтому восстановление «из своего же снапшота на том же узле» сводится к
копированию файлов на один уровень выше (`upload/`) и `nodetool refresh`.
Это ЛЕГИТИМНЫЙ документированный путь restore ScyllaDB/Cassandra для
single-node сценария, но НЕ единственный и не универсальный:
`sstableloader` — отдельный инструмент именно для восстановления/миграции
sstable МЕЖДУ узлами/кластерами через полноценный streaming-протокол (нужен,
если снапшот переносится на другой узел/кластер, а не восстанавливается на
месте) — вне рамок этого демо-стенда (single-node восстановление на месте
достаточно демонстрирует сам принцип snapshot→restore).

**Живая находка: `DROP TABLE` не освобождает директорию данных
немедленно.** Прогон стенда несколько раз подряд оставил на диске
`ops_backup_demo-<старый-cf_id>` директории от предыдущих запусков (видно
живьём: `ls /var/lib/scylla/data/telemetry/` после нескольких прогонов —
несколько `ops_backup_demo-*` каталогов одновременно). ScyllaDB не
гарантирует немедленную физическую очистку dropped-table директории (она
уходит по внутреннему GC/compaction расписанию, не синхронно с DDL) — по
этой причине скрипт определяет актуальную табличную директорию
АВТОРИТЕТНО, через `system_schema.tables.id` (текущий `cf_id` живой
таблицы), а не через `ls .../telemetry/ | grep ops_backup_demo-` — второе
совпало бы с несколькими каталогами и сломало путь (поймано живьём на
первом прогоне скрипта, исправлено до финальной версии в этом README).

#### Alternator — DynamoDB-совместимый API на отдельном транзитном узле

`ops-stand/main.go`, `-scenario alternator`: AWS SDK for Go v2
(`service/dynamodb`) с кастомным `BaseEndpoint` и dummy static-credentials
(`"dummy"/"dummy"`, регион `us-east-1` — Alternator не проверяет
AWS-подпись содержательно, но SDK v2 требует непустые значения).

**Почему НЕ на основном кластере.** `--alternator-port` — флаг ЗАПУСКА узла,
не runtime-toggle; `scylla1/2/3` подняты БЕЗ него, а правка `command` в
compose с последующим `up -d` означала бы recreate контейнеров = потеря
672000 строк `readings` (у `compose.yml` нет именованного volume для
данных). Поэтому Alternator демонстрируется на ОТДЕЛЬНОМ, ТРАНЗИТНОМ,
однонодовом контейнере на ТОЙ ЖЕ сети:

```bash
docker run -d --name scylla-alt --network scylla-cookbook-net \
  scylladb/scylla:2026.2.0 \
  --alternator-port 8000 --alternator-write-isolation always \
  --smp 1 --memory 2G --overprovisioned 1 --api-address 0.0.0.0
```

`ops/ops-demo.sh` часть B поднимает `scylla-alt`, ждёт готовности порта
(проверено ОТДЕЛЬНЫМ `curlimages/curl`-контейнером на сети — тот же
`127.0.0.1`-connection-refused паттерн, что и у `:9180/metrics`), гоняет
Go-сценарий В КОНТЕЙНЕРЕ (`golang:1.26` на `scylla-cookbook-net`,
`ALTERNATOR_ENDPOINT=http://scylla-alt:8000`) и сносит `scylla-alt` в конце.

Живой прогон (2026-07-11):

```
Alternator готов (2ms)
-- CreateTable -- CreateTable: OK, ждём ACTIVE... таблица ACTIVE
-- PutItem -- PutItem: device_id=dev-alternator-0001 event_time=...534Z value=42.50 — OK
-- GetItem -- GetItem: device_id=dev-alternator-0001 event_time=...534Z value=42.5
RESULT scenario=alternator endpoint=http://scylla-alt:8000 table=ops_alternator_demo put_ok=true get_ok=true match=true
ОК: get item == put item — тот же ScyllaDB отвечает по DynamoDB-совместимому протоколу (Alternator).
```

**Живая находка: DynamoDB-тип `N` не сохраняет литеральную строку числа.**
`PutItem` со значением `"42.50"` читается обратно `GetItem` как `"42.5"` —
Alternator (как и настоящий DynamoDB) хранит `N` как decimal-значение, не
байт-в-байт строку, и убирает незначащий нуль при сериализации ответа. Это
задокументированное поведение самого протокола DynamoDB, не баг
Alternator/этого демо — ассерт `get == put` в `ops-stand/main.go` поэтому
сравнивает ЧИСЛОВОЕ значение (`strconv.ParseFloat` + `==`), не литеральную
строку; побайтовое сравнение строк было бы нечестным ассертом для
протокола, который сам не гарантирует сохранность формата литерала.

#### Ассерты и живой результат

| Ассерт | Результат |
|---|---|
| `scylla_metrics_count > 0` | 4360/4352/4100 на scylla1/2/3 — OK |
| `restore_count == original_count` | 50 == 50 — OK |
| `alternator get == put` (числовое сравнение) | match=true — OK |
| `readings` не тронут | 672000 до и после — OK |
| Кластер остался 3×UN | OK (uptime `scylla1/2/3` не прерывался за весь Task 8) |

#### Заготовка карты выбора для статьи #7 (Scylla vs Cassandra vs DynamoDB vs Postgres/ClickHouse)

Не бенчмарк, а ось выбора по РЕЗУЛЬТАТАМ живых наблюдений всей серии (Стенды
#1–#7) — для статьи #7 серии:

- **ScyllaDB vs Cassandra.** Тот же CQL-протокол и модель данных (партиция +
  кластеризация, LSM, RF/CL, LWT/Paxos — Стенд #4), но shard-per-core
  архитектура на C++/Seastar (Стенд #2: p50/p99 без GC-пауз, шард-aware
  routing драйвера — Стенд #6) и tablets вместо ручного расчёта vnodes
  (Стенд #5) — ScyllaDB выигрывает там, где важны предсказуемые хвостовые
  латентности НА том же операционном профиле (nodetool/repair/snapshot —
  этот стенд), а не иная модель данных.
- **ScyllaDB vs DynamoDB (managed).** Alternator даёт СОВМЕСТИМЫЙ API
  (доказано этим стендом — тот же движок ScyllaDB отвечает по DynamoDB
  Query/PutItem/GetItem), но остаётся self-hosted операционной
  ответственностью (backup/restore, repair, мониторинг — всё out this
  README, не «нажать кнопку в консоли облака»). Путь миграции ОТ DynamoDB
  (или к нему) без переписывания клиентского кода — главный практический
  довод «за» Alternator, не производительность как таковая.
- **ScyllaDB/Cassandra-семья vs Postgres.** Разная модель согласованности
  (tunable CL + LWT-Paxos на уровне партиции, не MVCC-транзакции — см.
  серию «Транзакции и изоляция», `../transactions/`) и разная модель данных
  (партиция-первична, JOIN нет — см. Стенд #1 good/bad/hot partition).
  Выбор Scylla оправдан write-heavy/time-series нагрузкой с предсказуемым
  паттерном доступа по партиционному ключу (телеметрия этой серии — типовой
  случай), не заменой реляционной модели там, где нужны произвольные JOIN
  и multi-row ACID.
- **ScyllaDB/Cassandra-семья vs ClickHouse.** Ортогональные профили: Scylla
  — OLTP-подобный точечный/диапазонный доступ по партиции (частые
  point-read/write, как в этой серии), ClickHouse — колоночный OLAP
  (агрегации/сканы по столбцам, см. `../clickhouse/`). Телеметрия из этой
  серии типично живёт в ОБОИХ: горячий слой в Scylla (последние
  показания/алерты), холодный аналитический слой в ClickHouse (агрегаты за
  период) — не взаимоисключающий выбор.

#### Честные ограничения (специфика этого стенда)

- **`scylla_compaction_manager_backlog=0` на момент снятия метрик.**
  Идентификатор idle-кластера между стендами серии — компакция успела
  догнать запись. Не свидетельствует о том, что метрика «не работает»:
  Стенд #3 (TWCS/compaction) уже показал ненулевой backlog ПОД активной
  записью — эта метрика реагирует на нагрузку, просто данный прогон снят на
  спокойном кластере.
- **`DROP TABLE` не освобождает директорию данных немедленно** (см. раздел
  Backup/restore выше) — `ops-demo.sh` устойчив к этому (ищет `cf_id` через
  `system_schema.tables`, не через `ls`/`grep`), но на диске узла
  накапливаются небольшие (~1 МБ) осиротевшие `ops_backup_demo-*`
  директории от повторных прогонов демо в рамках этой задачи — безвредно
  для `readings`/кластера, отмечено честно, не подчищено намеренно (не
  входит в контракт задачи, ГК за автором при желании).
- **DynamoDB-тип `N` не сохраняет литеральную строку числа** (см. раздел
  Alternator выше) — ассерт сравнивает числовое значение, не строку;
  задокументированное поведение протокола, не находка-баг конкретно этого
  стенда.
- **`sstableloader` НЕ прогонялся живьём.** Восстановление в этом стенде —
  «на месте», через `nodetool refresh` из `upload/` (см. выше, почему это
  достаточный и легитимный путь для single-node сценария). Восстановление
  МЕЖДУ узлами/кластерами через `sstableloader` — вне рамок этой задачи,
  честно не заявлено как проверенное.
- **Grafana не поднималась** — задача просила «Prometheus (и опционально
  Grafana)»; для демонстрации «метрики собираются» PromQL API (`/api/v1/targets`,
  `/metrics`) живьём подтверждает сбор без визуализации; дашборд — за
  автором при желании (не входит в контракт задачи).

## Честные ограничения

- **`fs.aio-max-nr` на хосте (Docker Desktop/WSL2).** Дефолтное значение
  ядра (`65536`) хватает ровно на один узел ScyllaDB — при попытке поднять
  все три узла на одном хосте второй и третий падают в crash-loop:
  `Could not initialize seastar: std::runtime_error (Your system does not
  satisfy minimum AIO requirements. Set /proc/sys/fs/aio-max-nr to at least
  67590...)`. Обнаружено живьём при первом прогоне: `scylla1` поднялся и
  захватил доступные AIO-контексты, `scylla2`/`scylla3` не смогли
  инициализироваться. Фикс — поднять лимит ДО старта кластера (значение
  общее для всех контейнеров хоста, sysctl не namespaced):
  `docker run --rm --privileged alpine sh -c "echo 1048576 > /proc/sys/fs/aio-max-nr"`,
  затем `docker compose ... restart` (или изначально `up -d`) упавших узлов.
  На хостах с нативным Linux-ядром (не Docker Desktop/WSL2) лимит обычно уже
  выше дефолта — шаг может не понадобиться, но безвреден.
- **Шард-осведомлённый gocql через опубликованный Docker-порт на Windows.**
  Загрузка датасета (`-load -hosts 127.0.0.1:9042`) с хоста (Windows,
  Docker Desktop) стабильно падала на первом же подключении
  (`unable to discover protocol version: dial tcp 127.0.0.1:9042:
  connectex: ...`), хотя сырой TCP-коннект на этот же порт (`Test-NetConnection`,
  `/dev/tcp`) проходил успешно. Причина — шард-осведомлённая маршрутизация
  gocql полагается на клиентский исходящий порт для выбора шарда сервера,
  что ломается за NAT/port-forward (Docker Desktop на Windows публикует порт
  именно через такой NAT). Рабочий вариант, подтверждённый живьём: запускать
  загрузчик КОНТЕЙНЕРОМ на сети `scylla-cookbook-net`, подключаясь по
  внутренним DNS-именам (`scylla1:9042,scylla2:9042,scylla3:9042`) — без NAT
  между клиентом и кластером. Это же соглашение, которое уже использует
  каркас `../clickhouse/` и `../kafka/` для Go/Java-стендов (одноразовые
  контейнеры на сети cookbook), так что отклонения от паттерна серии нет.
- **`RF-rack-valid` предупреждение при создании keyspace.** `CREATE KEYSPACE
  telemetry ... replication_factor:3` на кластере с тремя узлами в ОДНОЙ
  раке (`rack1` у всех трёх — `--rack` в compose не задан, используется
  дефолт) даёт CQL-предупреждение `Keyspace 'telemetry' is not
  RF-rack-valid: the replication factor doesn't match the rack count`.
  Не ошибка (`cqlsh` вернул `EXIT: 0`, keyspace создан) — для учебного
  стенда одна рака достаточна; в проде RF=3 обычно сопровождается тремя
  разными раками.
- **Полный `SELECT count(*)` на 672 000 строк.** Прогнан живьём (не
  урезан) — вернулся быстро благодаря скромному объёму Task 1; для больших
  объёмов в следующих задачах серии полный count может быть намеренно
  тяжёлым (честная демонстрация анти-паттерна `count(*)` в Cassandra-подобных
  СУБД).
- **Лицензирование ScyllaDB (2024+).** С 2024 года ScyllaDB перешла на
  годовые unified/source-available релизы (модель `ScyllaDB Source Available
  License`) — это НЕ классическая permissive/copyleft OSS-лицензия
  предыдущих мажорных версий проекта (Apache 2.0 у более старых веток).
  Этот стенд честно пинуется к конкретному образу
  **`scylladb/scylla:2026.2.0`**, версия и условия лицензирования которого
  актуальны на дату написания серии (2026-07). Номер версии (`2026.2.0`) —
  годовой (`YYYY.N`), не semver проекта дооктябрьской эпохи — при
  воспроизведении на другой версии образа поведение (особенно
  tablets-специфика Стенда #5, Alternator Стенда #7, набор метрик Стендов
  #2/#3/#7) сверять по официальным release notes соответствующего года; это
  README не претендует на универсальную применимость ко всем прошлым/будущим
  версиям ScyllaDB, только к зафиксированной здесь.
- **Что НЕ воспроизведено живьём в рамках этого cookbook (честно, по
  стендам).** `sstableloader` (межузловой/межкластерный restore, Стенд #7 —
  сделан только restore «на месте» через `nodetool refresh`); Grafana-
  дашборд поверх Prometheus (Стенд #7 — собранность метрик подтверждена
  через PromQL API, визуализация не поднималась, не входила в контракт
  задачи); реальный межрегиональный RTT между ДЦ (Стенды #4/#5 — оба ДЦ на
  одном Docker-хосте, направление CL-эффекта воспроизведено, абсолютная
  величина — нет); `nodetool disablehandoff` (в этой сборке команды нет
  вообще — прогон B `ops/repair-demo.sh` иногда ловит, а иногда не ловит
  расхождение из-за скорости hinted handoff, задокументировано как честная
  особенность демо, не баг). Все остальные заявленные в брифах серии живые
  прогоны — выполнены и подтверждены реальными числами (см. `FIXTURES.md`).

## Проверено живьём (2026-07-11)

Итоговая сводка Task 9 — все 7 стендов серии прогнаны на реальном кластере,
числа зафиксированы (не изобретены) в соответствующих разделах README и
консолидированы в `FIXTURES.md`:

- **Стенд #1** (модель данных) — good/bad/hot partition подтверждён байтами
  `nodetool tablestats` (bad_max/good_max=15.4×, hot_max/bad_max=114.5×) и
  тремя живыми сценариями (`partition-size`/`hot-partition`/`query-shape`),
  все ассерты `EXIT 0`.
- **Стенд #2** (shard-per-core) — распределение по шардам почти 50/50 на всех
  3 узлах (20000 чтений), клиентский `p99/p50 ≈ 1.09–1.13` на трёх
  независимых прогонах, ни одного GC-масштабного выброса.
- **Стенд #3** (compaction/tombstones/repair) — TWCS-находка про
  WRITE-timestamp (не значение столбца), 14/14 окон TWCS ровно по 32
  sstable, `nodetool cluster repair` (не классический `-pr`, живая находка
  про tablet-keyspace) поймал и исправил реальное расхождение.
- **Стенд #4** (consistency + LWT) — CL-градиент воспроизведён монотонно
  (ONE < LOCAL_QUORUM ≈ QUORUM < ALL), LWT в 3.29× дороже plain, contention
  93.75% failed на 16 конкурентах (формула подтверждена на 8/4 конкурентах),
  живая находка про `cas_prune`/`cas_total_operations`.
- **Стенд #5** (tablets + multi-DC) — 4×UN в 2 ДЦ, репликация DC1→DC2
  подтверждена на 10000/10000 строк, DC failover (`LOCAL_QUORUM` работает,
  `EACH_QUORUM` честно отказывает), миграция 16 tablet-реплик при
  decommission (64→64 суммарно, без потерь).
- **Стенд #6** (shard-aware драйверы) — Go: aware быстрее naive на ~2.1-2.8%
  (2 независимых прогона, устойчиво); Java: OFF быстрее ON на ~0.9-1.1%
  (устойчиво, но противоположно Go после честного выравнивания CL=QUORUM) —
  оба направления реальные, эффект на `--smp 2` модестный в обоих случаях.
- **Стенд #7** (эксплуатация) — Prometheus собрал 4360/4352/4100 метрик с
  3 узлов, backup/restore цикл (snapshot→truncate→refresh) восстановил
  50/50 строк без потери `readings` (672000 неизменно), Alternator
  PutItem/GetItem через DynamoDB-совместимый API — match=true.
- **`ops/verify-static.sh`** — полный статический гейт (8 Go-модулей + Java
  реактор + 3 compose-комбинации) прошёл **ВСЁ ЗЕЛЁНОЕ ✓** за 163с (первый
  прогон с холодными кэшами).
- **Основной кластер** (`scylla1/2/3`, keyspace `telemetry`, 672000 строк
  `readings`) пережил Стенды #1–#8 без единого пересоздания — `3×UN` на
  протяжении всей серии, все данные byte-for-byte неизменны там, где стенды
  их не трогали намеренно (проверено явными `count(*)` до/после в нескольких
  стендах).

Phase 1 (живые стенды) на этом завершена — кластер снесён (см. команды ниже),
Phase 2 (статьи серии) использует `FIXTURES.md` и разделы README как
источник фактических чисел.
