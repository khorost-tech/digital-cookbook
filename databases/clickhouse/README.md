# ClickHouse: глубокое погружение — стенды

Живые стенды к серии статей «ClickHouse: глубокое погружение» на
[khorost.tech](https://khorost.tech). Этот каталог — фундамент (Task 1):
single-node ClickHouse + PostgreSQL 16 (базовая пара для сравнений), общий
детерминированный датасет событий и каркас Go/Java для следующих стендов
серии (MergeTree, бенчмарк драйверов, matviews+Kafka, распределённый
кластер+Keeper, эксплуатация, S3-тиринг, карта выбора CH/Timescale/DuckDB).

## Топология стенда (Task 1)

Сеть compose: `clickhouse-cookbook-net`.

| Сервис | Образ | Внутри сети | С хоста |
|---|---|---|---|
| ClickHouse (HTTP) | `clickhouse/clickhouse-server:26.6.1.1193` | `clickhouse:8123` | `localhost:18123` |
| ClickHouse (native) | тот же | `clickhouse:9000` | `localhost:19000` |
| PostgreSQL | `postgres:16` | `postgres:5432` | `localhost:15432` |

БД `demo` создаётся автоматически при первом старте контейнера ClickHouse
(`CLICKHOUSE_DB=demo` в `compose/compose.yml`) и явно создана в PostgreSQL
(`POSTGRES_DB=demo`).

Порты `18123/19000/15432` выбраны свободными на момент создания стенда
(2026-07-09) — в репозитории уже заняты 5432/5433/5440/5441/5442/5450/5452/
5453/5455 (postgres других стендов) и 19092-19094/18080/18083 (kafka, см.
`../kafka/compose/compose.yml`). При конфликте на другой машине — поменяйте
порты в `compose/compose.yml`.

Распределённый кластер (шардинг + репликация + ClickHouse Keeper) — отдельный
`compose/cluster.yml` (стенд #5, готово — см. секцию «Проверено живьём (distributed)» ниже),
в базовую топологию Task 1 не входит.

## Версии (консолидировано по всем 8 стендам, сверено живьём 2026-07-10)

| Компонент | Версия | Как проверено |
|---|---|---|
| ClickHouse (образ) | `clickhouse/clickhouse-server:26.6.1.1193` | `docker pull ...:latest` разрешился в этот тег (digest совпадает с `26.6.1.1193` в registry); `clickhouse-client -q "SELECT version()"` внутри контейнера → `26.6.1.1193`. Та же версия во ВСЕХ 8 стендах (Task 1–9), включая кластер стенда #5 |
| ClickHouse Keeper (образ) | `clickhouse/clickhouse-keeper:26.6.1.1193` | `clickhouse-keeper --version` внутри контейнера → `ClickHouse keeper version 26.6.1.1193 (official build)` (стенд #5, `compose/cluster.yml`) |
| PostgreSQL (образ) | `postgres:16.14` | `postgres --version` внутри контейнера → `PostgreSQL 16.14 (Debian 16.14-1.pgdg13+1)`; пин зафиксирован явно (`compose/compose.yml: image: postgres:16.14`, не плавающий тег `16`) |
| TimescaleDB (образ) | `timescale/timescaledb:2.28.2-pg16` | `pg_extension.extversion` → `2.28.2` (стенд #8, `compose/decision.yml`) |
| DuckDB (embedded engine) | `v1.4.1` | `SELECT version()` внутри процесса (стенд #8), через Go-биндинг `github.com/marcboeker/go-duckdb/v2` `v2.4.3` (`decision/go.mod`) — без отдельного контейнера/сервера |
| Kafka (образ) | `apache/kafka:4.3.1` | single-node KRaft (стенд #4, `compose/kafka.yml`), тот же образ, что серия [«Kafka: глубокое погружение»](../kafka/) |
| MinIO (образ) | `minio/minio:RELEASE.2025-09-07T16-13-09Z` | бандлит `mc` (стенд #7, `compose/minio.yml`) |
| clickhouse-go v2 | `v2.47.0` | Go module proxy `@latest`, `go/go.mod` (то же во всех Go-стендах: `when-olap`, `go/mergetree`, `drivers/go`, `materialized-views`, `distributed`, `ops-stand`, `s3`, `decision`) |
| ch-go | `v0.73.0` | Go module proxy `@latest`; `// indirect` в `go/go.mod` (Task 1, транзитивная зависимость clickhouse-go v2), прямая зависимость в `drivers/go/go.mod` с Task 4 (низкоуровневый API стенда #3) |
| franz-go | `v1.21.5` | `materialized-views/go.mod` (продюсер Kafka→CH пайплайна стенда #4), та же мажорная версия, что серия «Kafka: глубокое погружение» |
| franz-go/pkg/kadm | `v1.18.0` | `materialized-views/go.mod` (пересоздание топика перед подключением `ENGINE=Kafka`) |
| github.com/jackc/pgx/v5 | `v5.10.0` | `decision/go.mod` (COPY-загрузка в PostgreSQL/TimescaleDB стенда #8) |
| github.com/shopspring/decimal | `v1.4.0` | `s3/go.mod`, `ops-stand/go.mod`, `materialized-views/go.mod` (побайтовая сверка `Decimal(10,2)` без потери точности) |
| Go | `1.25.0` (`go.mod`) | сборка через образ `golang:1.25` |
| clickhouse-jdbc | `0.9.0` | Maven Central `solrsearch` latest, `java/pom.xml`. v2-драйвер (`com.clickhouse.jdbc.ClickHouseDriver`, HTTP-интерфейс) — в 0.9.0 отдельного classifier `http` больше нет (был в 0.8.x), обычная зависимость без classifier уже включает HTTP-провайдер |
| client-v2 | `0.9.0` | Maven Central `solrsearch` latest, `java/pom.xml` (стенды #2/#3 — native-клиент; смоук использует только clickhouse-jdbc) |
| JDK (Java) | 25 | `maven.compiler.release=25`, сборка через `maven:3.9-eclipse-temurin-25` |

Content-note: `compose/compose.yml` уже пинует PostgreSQL точным тегом
`postgres:16.14` (не плавающим `postgres:16`) — эта таблица (Task 10)
исправляет расхождение: предыдущая версия документации указывала `postgres:16`
хотя фактический пин в compose-файле точнее. Остальные образы/пакеты во всей
серии — уже точные версии, сверено по исходным compose/go.mod/pom.xml файлам
(см. столбец «Как проверено» выше).

## Схема датасета

Один общий детерминированный датасет "события" — используется стендами #1
(OLAP vs PostgreSQL), #3 (бенчмарк драйверов) и #8 (карта выбора), чтобы
сравнения были яблоки-к-яблокам на идентичных данных.

```
event_time  DateTime                — момент события
user_id     UInt64                  — идентификатор пользователя
event_type  LowCardinality(String)  — тип события
url         String                  — путь на сайте
duration_ms UInt32                  — длительность обработки/визита, мс
country     LowCardinality(String)  — страна (ISO alpha-2)
revenue     Decimal(10,2)           — выручка (0.00 кроме checkout/purchase)
```

Генератор: `dataset/main.go` (модуль `tech.khorost/clickhouse-cookbook/dataset`,
самодостаточный, без внешних зависимостей — только stdlib). Детерминирован:
фиксированный `-seed` (по умолчанию 42) → байт-в-байт одинаковый вывод при
повторных запусках, **не зависит от даты запуска** (окно событий отсчитывается
от зашитой в код опорной точки `2026-07-01T00:00:00Z`, а не от `time.Now()`).

Распределения (документированы и проверены живьём на прогоне 200k строк,
см. «Проверено живьём» ниже):

- **event_type** (веса из 1000): `page_view` 550, `click` 200, `search` 100,
  `add_to_cart` 60, `checkout` 40, `purchase` 30, `signup` 10, `logout` 10.
- **country** (веса из 1000, длинный хвост): `RU` 350, `US` 150, `DE` 80,
  `GB` 60, `FR` 50, `IN` 50, `BR` 40, `CN` 40, `JP` 30, `KZ` 30, ещё 10 стран
  с меньшими весами (полный список — `dataset/main.go`, `var countries`).
- **user_id**: равномерно из пула `500000` пользователей (пул фиксирован
  независимо от `-rows` — при большем объёме растёт число событий на одного
  пользователя, что реалистичнее, чем рост числа разных пользователей).
- **url**: один из 14 шаблонов пути (`/`, `/catalog`, `/product/%d`,
  `/blog/%d`, ... — `dataset/main.go`, `var urlTemplates`), числовые id
  подставляются равномерно из `[1, 100000]`.
- **duration_ms**: равномерно в диапазоне, специфичном для `event_type`
  (напр. `page_view` 200–8000мс, `click` 50–500мс — короче; `dataset/main.go`,
  `var eventTypes`).
- **revenue**: `0.00` для всех типов кроме `checkout` (0.00–300.00, значение
  корзины) и `purchase` (5.00–500.00, фактическая покупка).
- **event_time**: равномерно в окне 90 дней перед `2026-07-01T00:00:00Z`.

### Запуск генератора

```bash
# из каталога clickhouse/
docker run --rm -v "$(pwd)/dataset:/app" -w /app golang:1.25 \
  go run . -rows=2000000 -seed=42 -out=/app/out/events.csv
```

- `-rows` — по умолчанию `2000000` (2М, для быстрой проверки). Полный прогон
  стендов серии — `-rows=20000000` (20М).
- `-seed` — по умолчанию `42`.
- `-out` — путь к CSV (`-` — stdout). Вывод — `CSV` с заголовком
  (`event_time,user_id,event_type,url,duration_ms,country,revenue`),
  совместим с `FORMAT CSVWithNames` в ClickHouse и
  `COPY ... WITH (FORMAT csv, HEADER)` в PostgreSQL.

Сгенерированные CSV **не коммитятся** (см. `.gitignore`) — воспроизводятся
детерминированно по `-seed`.

## Каркас Go/Java

```
clickhouse/
  compose/compose.yml   # CH single-node + PG16
  dataset/               # Go-генератор общего датасета (самостоятельный модуль)
    go.mod
    main.go
  go/                    # Go-стенды (module tech.khorost/clickhouse-cookbook)
    go.mod               # clickhouse-go v2 + ch-go
    smoke/                # Task 1: живая проверка коннекта (native-протокол)
  java/                  # Java-стенды (parent pom, packaging=pom)
    pom.xml              # clickhouse-jdbc + client-v2 (пин), JDK 25
    smoke/                # Task 1: живая проверка коннекта (JDBC/HTTP)
  README.md
  .gitignore
```

Следующие задачи серии добавляют подкаталоги-стенды: `when-olap/` (готово),
`go/mergetree/`+`java/mergetree/` (готово), `drivers/go/`+`drivers/java/`
(готово — стенд #3, СВОЙ каркас `drivers/{go,java}/`, вне `go/`/`java/`),
`materialized-views/` (готово — стенд #4, СВОЙ каркас
`materialized-views/`, + `compose/kafka.yml`), `distributed/` (готово —
стенд #5, СВОЙ каркас `distributed/`, + `compose/cluster.yml` (ОТДЕЛЬНЫЙ,
самостоятельный кластерный compose — 4 CH-ноды + Keeper, своя сеть
`clickhouse-cluster-net`, НЕ поверх `compose/compose.yml`) + `config/*.xml`
(remote_servers/macros/keeper)), `ops-stand/` (готово — стенд #6, СВОЙ
каркас `ops-stand/`, + `config/backups.xml` для BACKUP TABLE ... TO
File(...), см. секцию «Проверено живьём (ops, ...)» ниже),
`s3/`+`compose/minio.yml` (готово — стенд #7, СВОЙ каркас `s3/`, +
`config/storage.xml` для storage_configuration s3-диска, см. секцию
«Проверено живьём (s3, ...)» ниже), `decision/`+`compose/decision.yml`
(готово — стенд #8, финальный, СВОЙ каркас `decision/` (CH+PG+Timescale
через сеть + DuckDB embedded прямо внутри Go-бинаря через `go-duckdb`,
без отдельного контейнера/compose-сервиса), + `compose/decision.yml` для
TimescaleDB, см. секцию «Проверено живьём (decision, ...)» ниже),
`ops/*.sh` (оркестрация демо-сценариев, по образцу `../kafka/ops/`).

## Как запускать

```bash
cd clickhouse
docker compose -f compose/compose.yml up -d
# дождаться healthy: docker compose -f compose/compose.yml ps

# ClickHouse
docker exec -it clickhouse-cookbook clickhouse-client -q "SELECT version()"

# PostgreSQL
docker exec -it clickhouse-cookbook-postgres psql -U postgres -d demo -c "SELECT version();"

# после стенда
docker compose -f compose/compose.yml down -v
```

Go/Java-стенды запускаются как одноразовые контейнеры на сети
`clickhouse-cookbook-net` (см. докстринги `go/smoke/main.go` и
`java/smoke/src/.../Main.java` — точные команды сборки/запуска).

### Быстрая статическая проверка (без тяжёлых стендов)

```bash
bash ops/verify-static.sh
```

Гейт для CI/pre-push: `bash -n` всех `ops/*.sh`, `go build -o /dev/null ./... && go vet ./...`
по всем Go-модулям, `mvn -DskipTests package` Java-реактора, `docker compose config` по всем
комбинациям compose-файлов. Требует только Docker, **не поднимает ни одного сервиса** и не
трогает 20M-датасеты — секунды-минуты вместо полного live-прогона.

## Проверено живьём (2026-07-09)

- `docker pull clickhouse/clickhouse-server:latest` → digest совпал с тегом
  `26.6.1.1193`; `docker pull postgres:16` → `postgres --version` внутри
  контейнера → `PostgreSQL 16.14 (Debian 16.14-1.pgdg13+1)`.
- `docker compose -f compose/compose.yml up -d` → оба контейнера `healthy`
  (healthcheck CH `wget http://localhost:8123/ping`, PG `pg_isready`).
- `clickhouse-client -q "SELECT version()"` → `26.6.1.1193`; `SHOW DATABASES`
  подтвердил автосоздание `demo` (через `CLICKHOUSE_DB=demo`).
- `pg_isready -U postgres -d demo` → `accepting connections`.
- Go-смоук (`go/smoke`, clickhouse-go v2, нативный протокол, `golang:1.25`,
  на сети `clickhouse-cookbook-net`): `smoke OK: connected to clickhouse:9000,
  SELECT 1 = 1, server version = 26.6.1.1193`.
- Java-смоук (`java/smoke`, clickhouse-jdbc 0.9.0 v2-драйвер, HTTP-интерфейс,
  `maven:3.9-eclipse-temurin-25`, на той же сети): `smoke OK: connected to
  jdbc:ch://clickhouse:8123/demo, SELECT 1 = 1, server version = 26.6.1.1193`.
- Генератор (`dataset/main.go`, `-rows=200000 -seed=42`): сгенерировал
  200000 строк; загружен в ClickHouse (`INSERT ... FORMAT CSVWithNames`) и в
  PostgreSQL (`\COPY ... WITH (FORMAT csv, HEADER)`) — `SELECT count()`
  совпал в обеих СУБД: **200000 == 200000**. Проверено распределение:
  `event_type` (page_view 55.0%, click 20.2%, search 9.9%, add_to_cart 6.0%,
  checkout 3.9%, purchase 3.0%, signup 1.0%, logout 1.0% — совпадает с
  заявленными весами в пределах случайного отклонения), `country` (RU 35.0%,
  US 15.0%, DE 8.0%, GB 6.0%, FR 5.0% — топ-5 совпал с весами), `event_time`
  в диапазоне `[2026-04-02, 2026-06-30]` (90-дневное окно перед опорной
  `2026-07-01`), `sum(revenue) = 2668979.11` (ненулевая выручка есть).
- `docker compose -f compose/compose.yml down -v` — стенд остановлен, тома
  удалены, тестовые CSV/JAR-артефакты не коммитятся.

## Проверено живьём (when-olap, 2026-07-09)

Стенд #1 (`when-olap/`, Go, module `tech.khorost/clickhouse-cookbook/when-olap`)
— когда OLAP (ClickHouse), а когда обычной OLTP-таблицы PostgreSQL достаточно.
Общий датасет (`dataset/main.go -rows=20000000 -seed=42`) загружен ОДИНАКОВО
в CH (`demo.events`, `MergeTree ORDER BY (event_time, user_id) PARTITION BY
toYYYYMM(event_time)`) и в PG (`events` + btree-индексы на `event_time` и
`country`, `PRIMARY KEY(id)`), суррогатный `id` — общий для честных
point-lookup/mutation-сравнений. Запуск — по фазам (`-phase=schema|load|size|
aggregate|point|mutate`, эквивалент `-phase=all` в `ops/when-olap-demo.sh`),
внутри `golang:1.25` на сети `clickhouse-cookbook-net`, все программные
ассерты (fail-loud) прошли.

**Объём:** 20 000 000 строк в обеих СУБД (полный датасет, без урезания — PG
осилил bulk-загрузку через `COPY`).

**Загрузка** (`clickhouse-go/v2` `PrepareBatch`, батч 1 000 000 строк; PG —
`pgx` `CopyFrom`/COPY-протокол, НЕ построчный `INSERT`):

```
[load] CH: 20000000 rows in 34.780105796s (575041 rows/s)
[load] PG: 20000000 rows in 2m27.529520569s (135566 rows/s)
```

CH почти в 4.3x быстрее на загрузке того же объёма тем же способом (batched
bulk на обеих сторонах) — ожидаемо: колоночный формат + сжатие пишутся
дешевле, чем строковые heap-страницы + два btree-индекса PG.

**Размер на диске** (детерминировано, не host-зависимо):

```
[size] CH: 464.20 MiB on disk, 15 active parts, 20000000 rows (system.parts)
[size] PG: 2.54 GiB total (1.64 GiB table + 921.10 MiB indexes), 20000000 rows
[size] compression ratio PG/CH = 5.60x
```

CH (колоночное хранение + `LowCardinality`/`Decimal` компактное кодирование,
дефолтный `LZ4`) занимает **в 5.6 раза меньше**, чем PG-таблица с двумя
btree-индексами на том же датасете.

**Аналитический агрегат** (`SELECT country, toDate(event_time), count(),
sum(revenue) ... GROUP BY`, median по 5 прогонам, «характерный прогон» —
host-зависимо, Docker Desktop/Windows):

```
[aggregate] CH median over 5 runs: 99.884439ms
  (durations: [107.00386ms 121.602881ms 96.012286ms 92.322267ms 99.884439ms])
[aggregate] PG (с индексами event_time/country) median over 5 runs: 4.26652176s
  (durations: [4.290634581s 4.193674547s 4.212179498s 4.286252414s 4.26652176s])
[aggregate] PG (без индексов) median over 5 runs: 4.356532189s
  (durations: [4.356532189s 4.268493223s 4.373822179s 4.342176233s 4.385548903s])
```

CH быстрее PG в **42.71x** (median CH 99.9ms vs PG-с-индексами 4.27s).
Индексы на `event_time`/`country` PG **не помогают** этому запросу (4.27s с
индексами практически равно 4.36s без — разница в пределах шума): агрегат по
GROUP BY на некоррелирующих с индексами колонках всё равно требует полного
скана (`Seq Scan`/`Parallel Seq Scan`), индекс тут не отбирает узкое
подмножество строк. Планы (сводка ключевых узлов, времена — из EXPLAIN ANALYZE):

- PG EXPLAIN ANALYZE (с индексами): `Finalize GroupAggregate` → `Gather Merge`
  (2 воркера) → `Sort` → `Partial HashAggregate` → `Parallel Seq Scan on
  events` (`rows=20000000`, `Execution Time: 2008.664 ms` — это отдельный
  EXPLAIN-прогон, параллельный план; не совпадает по времени с
  non-EXPLAIN-медианой 4.27s, т.к. EXPLAIN-прогон случайно попал на
  параллельный план с воркерами, а замеряемые прогоны — на последовательный,
  см. второй план ниже).
- PG EXPLAIN ANALYZE (без индексов): `Sort` → `HashAggregate` → `Seq Scan on
  events` (`rows=20000000`, `Execution Time: 5293.338 ms`, без параллельных
  воркеров — соответствует порядку величины замеренной медианы).
- CH EXPLAIN: `Expression (Project names)` → `Sorting` → `Expression` →
  `Aggregating` → `Expression` → `ReadFromMergeTree (demo.events)` — один
  проход по колонкам `country`/`event_time`/`revenue` без чтения остальных 4
  колонок (колоночное хранение), без индекс-поиска (агрегат читает почти всю
  таблицу в обеих СУБД — разница в том, ВО СКОЛЬКО дешевле этот полный
  проход в CH).

**Точечный SELECT по PK/id** (там, где PG выигрывает — `id` НЕ входит в CH
`ORDER BY (event_time, user_id)`, значит для CH это скан по всей колонке
`id`, без разреженного индекса, который мог бы пропустить гранулы):

```
[point] PG SELECT ... WHERE id=10000000: 433.913µs (Index Scan using events_pkey, Execution Time: 0.080 ms)
[point] CH SELECT ... WHERE id=10000000: 7.742567ms (found=true, read_rows=2490368 из 20000000)
```

PG — `Index Scan using events_pkey`, доли миллисекунды. CH прочитал
**2 490 368 строк** (12.5% таблицы, по гранулам `index_granularity=8192`,
т.к. `id` не в `ORDER BY`) — на **~18x** медленнее PG на этом конкретном
запросе. Честный контраст: CH выигрывает на агрегатах по всей таблице,
проигрывает на точечных выборках по неотсортированной колонке.

**Точечный UPDATE/DELETE** (главный анти-паттерн CH — Step 3 брифа):

```
[mutate] PG UPDATE ... WHERE id=5000001: 2.925796ms (синхронно)
[mutate] PG DELETE ... WHERE id=5000002: 439.192µs (синхронно)
[mutate] CH ALTER ... UPDATE WHERE id=5000001: submit=6.48824ms (выглядит мгновенно),
  completion=1.023825123s (parts_to_do at submit=15) — РЕАЛЬНАЯ стоимость
[mutate] CH ALTER ... DELETE WHERE id=5000002: submit=5.693265ms (выглядит мгновенно),
  completion=820.630404ms (parts_to_do at submit=15) — РЕАЛЬНАЯ стоимость
```

PG `UPDATE`/`DELETE` по PK — обычный синхронный точечный вызов, доли
миллисекунды, никакого разрыва между "отправлено" и "применено". CH `ALTER
TABLE ... UPDATE/DELETE` — асинхронная **мутация**: клиент получает
управление обратно за ~6ms (выглядит мгновенно), но реальное применение
(переписывание parts, `system.mutations.is_done`) занимает **~1s/~0.8s** —
в **350x/1870x дороже**, чем эквивалентная PG-операция, потому что CH
переписывает целые parts, а не правит одну строку на месте. Итоговое число
строк после мутаций совпало на обеих сторонах: **19 999 999** (PG и CH).

**Ассерты** (все прошли, fail-loud): CH rows == PG rows после загрузки;
CH-размер >= 2.0x меньше PG (факт 5.60x); CH-агрегат >= 1.5x быстрее
PG-полного-скана (факт 42.71x); CH point-select читает >10% таблицы (факт
12.5%, читает много без сортировки по `id`); PG point-select использует
Index Scan; CH-мутация дороже эквивалентной PG-операции (факты 350x/1870x).

**Найденные и исправленные баги стенда** (during живого прогона):
`aggregate.go` — `toDate(event_time)` в CH возвращает тип `Date`, сканирование
в `*string` не поддерживается драйвером `clickhouse-go/v2` (`converting Date
to *string is unsupported`) — исправлено на `time.Time`. `pointops.go` —
`system.mutations.parts_to_do` в CH — `Int64`, а не `UInt64`, сканирование в
`*uint64` падало (`converting Int64 to *uint64 is unsupported`) — исправлено
на `int64`. Оба бага — типовые ошибки маппинга Go-типов на типы ClickHouse
(не логические ошибки сценария), после фикса все фазы прошли без ассертов-
провалов.

**Версии в прогоне:** ClickHouse `26.6.1.1193` (`clickhouse-client -q "SELECT
version()"` → `26.6.1.1193`), PostgreSQL `16.14` (`SHOW server_version` →
`16.14 (Debian 16.14-1.pgdg13+1)`).

Content-note: throughput загрузки и латентность агрегата/точечных операций —
host-зависимы (замерено внутри compose-сети на Docker Desktop/Windows,
абсолютные числа не переносимы на другое железо/облако). Размер на диске,
коэффициент сжатия, число прочитанных строк (`read_rows`), планы запросов —
детерминированы для этого датасета/версий и воспроизводимы.

## Проверено живьём (mergetree, 2026-07-09)

Стенд #2 (`go/mergetree/` + `java/mergetree/`) — MergeTree из Go и Java:
разреженный первичный индекс/гранулы, батч vs построчная вставка
(анти-паттерн), `async_insert`, Java-эквивалент (JDBC batch + client-v2
native). DDL — общий шаблон (`schema.go` / `Main.DDL`), идентичный в Go и
Java:

```sql
CREATE TABLE demo.<table>
(
    event_time  DateTime,
    user_id     UInt64,
    event_type  LowCardinality(String),
    url         String,
    duration_ms UInt32,
    country     LowCardinality(String),
    revenue     Decimal(10,2)
)
ENGINE = MergeTree
ORDER BY (event_time, user_id)
PARTITION BY toYYYYMM(event_time)
SETTINGS index_granularity = 8192
```

Запуск — `ops/mergetree-demo.sh` (генерирует выделенный CSV `-rows=5000000`,
запускает Go `-phase=all`, затем Java-стенд), либо по фазам (`-phase=schema|
load|granules|antipattern|async`). Все программные ассерты (fail-loud) на
обоих прогонах (Go, Java) прошли.

**Объём:** `demo.mergetree_events` — 5 000 000 строк (выделенный CSV,
`dataset/main.go -rows=5000000 -seed=42`), загружено батчем 100k
(`clickhouse-go/v2` `PrepareBatch`/`Append`/`Send`) за **10.45s (478 592
rows/s)**, 24 active parts (system.parts) сразу после загрузки.

**Гранулы/пропуск** (Step 1 брифа — фильтр по префиксу `ORDER BY
(event_time, ...)`, окно 1 сутки внутри 90-дневного датасета):

```
[granules] filtered query: 46.572073ms, count()=55297, read_rows=163840 (из 5000000 total, 3.28%)
[granules] full-scan query: 9.845914ms, count()=5000000, read_rows=5000000 (из 5000000 total, 100.00%)
```

`EXPLAIN indexes=1` подтверждает пропуск на уровне PrimaryKey: `Parts: 8/8,
Granules: 20/204` (индексный поиск бинарным алгоритмом отобрал 20 гранул из
204 в затронутых partitions) — Min-Max/Partition секции дополнительно
отсекли 16/24 parts ещё до PrimaryKey. Итог: **read_rows читает 3.28% от
всей таблицы** (163 840 из 5 000 000) вместо полного скана — детерминировано
для этого датасета/версии, не host-зависимо. Ассерт: `filtered.readRows <
totalRows/10` — прошёл с большим запасом (3.28% << 10%).

**Батч vs построчная вставка** (Step 2 брифа, apples-to-apples N=2000 строк,
row-by-row с `SYSTEM STOP MERGES` во время вставки, чтобы зафиксировать parts
ДО фонового merge):

```
[antipattern] row-by-row: 2000 rows in 2m1.052132363s (16.5 rows/s), 2000 active parts
[antipattern] batch (1 PrepareBatch/Send, N=2000): 2000 rows in 12.46602ms (160436.1 rows/s), 3 active parts
[antipattern] batch faster than row-by-row (same N=2000): 9710.6x
```

Построчная вставка (отдельный синхронный `INSERT ... VALUES` на каждую из
2000 строк, без батчинга) — **9710x медленнее** батча того же объёма и
создаёт **2000 active parts** (буквально по одному на вставку — MergeTree не
умеет "дописать" в существующий part) против **3 parts** у батча (один part
на каждую из затронутых `toYYYYMM(event_time)`-партиций, не один на вставку).
Это ровно источник "too many parts" в проде — каждый part требует
метаданных/файлового дескриптора, фоновый merge вынужден непрерывно их
схлопывать; при устойчивом потоке построчных вставок фон не успевает,
ClickHouse начинает бросать `Too many parts`. Throughput кэпнут на ~2000
строк намеренно (не миллионы) — анти-паттерн уже нагляден на этом объёме,
полный построчный прогон на 5M строк занял бы часы без дополнительного
контента к демонстрации.

**Async inserts** (Step 3 брифа, `async_insert=1`):

```
[async] concurrent async_insert=1/wait_for_async_insert=1: 2000 однострочных INSERT (concurrency=50, async_insert_busy_timeout_ms=200) за 47.07088914s, 2000 rows, 4 active parts
[async-vis] отправлено 500 INSERT (wait_for_async_insert=0, async_insert_busy_timeout_ms=5000) за 322.7131ms; сразу после — видно 80/500 rows (частичная видимость)
[async-vis] после ожидания 6.5s (>= async_insert_busy_timeout_ms=5000ms) — видно 500/500 rows (буфер сброшен, eventual consistency)
```

2000 конкурентных (50 горутин) однострочных `INSERT` с `async_insert=1` —
сервер скоалесил их в **4 active parts** (вместо потенциальных 2000 при
синхронной построчной вставке того же объёма) — серверная буферизация даёт
тот же эффект, что клиентский батчинг, когда сам клиент не может собрать
батч (много независимых мелких писателей). С `wait_for_async_insert=0`
клиент не ждёт флаша — видна **частичная видимость** сразу после отправки
(80 из 500 строк — буфер ещё не сброшен) и **полная** после ожидания
`async_insert_busy_timeout_ms` (eventual consistency, а не "мгновенно
как обычный INSERT").

**Java** (`java/mergetree/`, зеркало той же DDL/датасета, 100 000 строк,
эквивалент батча Go):

```
[jdbc] mergetree_events_java_jdbc: 100000 rows in 0.788s (126827 rows/s), 3 active parts
[client-v2] mergetree_events_java_client: 100000 rows in 0.139s (717644 rows/s), 3 active parts
[granules] filtered query (7-day window): count()=7970, read_rows=16384 (из 100000 total, 16.38%)
```

`clickhouse-jdbc` (`PreparedStatement.addBatch`/`executeBatch`) — 126 827
rows/s; `client-v2` (`Client.insert`, CSV-поток одним вызовом, без JDBC-
обёртки) — 717 644 rows/s, в разы быстрее JDBC-слоя на этом же объёме (тот же
паттерн, что batch-загрузка в Go: массовый native-путь дешевле построчного
JDBC-протокола поверх HTTP). Оба — по 3 active parts (одна затронутая
партиция на каждый прогон, не 1 part на строку — тот же принцип, что в Go).
Гранульный чек на JDBC-таблице (100k строк, окно 7 суток вместо 1 суток —
пропорционально меньшей таблице): `read_rows=16384` из 100 000 (16.38%),
ассерт `< total/5` прошёл.

**Ассерты** (все прошли, fail-loud, Go + Java): загруженные rows == expected
(5M bulk, 2000 row-by-row, 2000 batch-small, 2000/500 async, 100k JDBC, 100k
client-v2); `read_rows` при индексном фильтре `<< total` (Go `< total/10`,
факт 3.28%; Java `< total/5`, факт 16.38%); full-scan `read_rows >= total`;
батч кратно (>=3x, факт 9710x) быстрее построчной на одинаковом N; batch/
JDBC/client-v2 insert — `<=6` active parts (один на затронутую партицию, не
один на строку); row-by-row parts `> batch_parts*10` (факт 2000 > 30);
async concurrent parts `< total/2` (факт 4 << 1000, серверная буферизация);
async-vis: частичная видимость сразу (`< total`), полная после busy_timeout
(`== total`).

**Content-note (почему построчная вставка — анти-паттерн, для тела
статьи):** MergeTree — LSM-подобный движок с иммутабельными parts: каждый
`INSERT`-вызов (даже на одну строку) создаёт новый part на диске, part
нельзя дописать. Фоновый merge непрерывно схлопывает parts, но при высокой
частоте построчных вставок фон не успевает — растёт число открытых
файловых дескрипторов/метаданных, до `Too many parts` (сервер начинает
отклонять вставки). Решение — клиентский батчинг (Step 2, `PrepareBatch`/
`executeBatch`/CSV-поток) там, где клиент может собрать батч, либо
`async_insert=1` (Step 3) там, где не может (много независимых мелких
писателей) — сервер сам коалесит вставки в один part по таймауту/размеру
буфера.

**Версии в прогоне:** ClickHouse `26.6.1.1193` (то же, что Task 1/#1),
clickhouse-go `v2.47.0`, clickhouse-jdbc/client-v2 `0.9.0` (`java/pom.xml`).

Content-note: throughput вставок/запросов — host-зависим (Docker Desktop/
Windows, compose-сеть). `read_rows`/гранулы/число `active_parts` —
детерминированы для этого датасета/версии CH и воспроизводимы.

## Проверено живьём (drivers, 2026-07-10)

Стенд #3 (`drivers/go/` + `drivers/java/`, `ops/drivers-bench.sh`) — бенчмарк
6 драйверов ClickHouse: единый сценарий (батч-INSERT `M` строк одного
выделенного CSV-датасета в СВОЮ таблицу на драйвер + один и тот же
аналитический SELECT) прогнан через **ch-go** (низкоуровневый колоночный,
нативный протокол, ручное заполнение `proto.Col*`), **clickhouse-go native**
(`PrepareBatch`/`Append`/`Send`), **clickhouse-go database/sql** (`sql.DB` +
`Exec` в транзакции — задокументированный batch-паттерн драйвера: `Begin` →
`Prepare` → N×`Exec` → `Commit`), **raw HTTP** (`net/http` POST на
`clickhouse:8123`, без единой ClickHouse-библиотеки), **clickhouse-jdbc**
(`PreparedStatement.addBatch`/`executeBatch`) и **client-v2**
(`Client.insert`, CSV-поток). Все программные ассерты (fail-loud) прошли.

**Объём:** `M = 1 000 000` строк общего датасета (`dataset/main.go
-rows=1000000 -seed=42 -out=dataset/out/events-drivers.csv`, отдельный
выделенный CSV — общий для всех 6 драйверов, чтобы вставки были
байт-в-байт идентичны). Батч-размер — `100 000` строк на вызов (Go-драйверы
и JDBC), одинаковый для всех.

**Throughput вставки** (rows/s, «характерный прогон», host-зависимо —
Docker Desktop/Windows, замерено внутри compose-сети):

```
raw HTTP (net/http)                    901925 rows/s (1.109s)
ch-go (ручные proto.Col*)              541300 rows/s (1.847s)
clickhouse-go native (PrepareBatch)    458855 rows/s (2.179s)
clickhouse-go database/sql (tx+Exec)   430047 rows/s (2.325s)
client-v2 (Java, CSV-поток)            270621 rows/s (3.695s)
clickhouse-jdbc (Java, addBatch)       129829 rows/s (7.702s)
```

Ранжирование throughput вставки: **raw HTTP > ch-go > clickhouse-go native >
clickhouse-go database/sql > client-v2 (Java) > clickhouse-jdbc (Java)**.

**Честная оговорка про raw HTTP:** это НЕ apples-to-apples с остальными
5 драйверами буквально — `httpInsert` (`drivers/go/httpdriver.go`) шлёт весь
файл `M` строк ОДНИМ POST-запросом (`INSERT ... FORMAT CSVWithNames`, тело —
сырой CSV), а не 10 чанками по 100k, как остальные. Один round-trip без
клиентского кодирования/парсинга строк — валидный и типичный способ
использовать HTTP-интерфейс для bulk-загрузки (именно так его обычно и
используют: curl/скрипт заливает файл целиком), но число выше не потому что
"HTTP быстрее протокола" в расчёте на строку — а потому что здесь один
round-trip и вся работа по разбору CSV на сервере, а не 10 отдельных
батчей + клиентское кодирование типов на Go/Java стороне. `ch-go` — самый
быстрый из батчированных (10×100k) путей, что ожидаемо: минимальный
клиентский оверхед (ручные колоночные буферы, никакого reflection/ORM).

**Латентность SELECT** (аналитический агрегат `GROUP BY country`, 1M строк
→ 20 групп, «характерный прогон»):

```
raw HTTP                     6.9ms
clickhouse-go native         10.8ms
ch-go                        12.5ms
client-v2 (Java)             15ms
clickhouse-jdbc (Java)       19ms
clickhouse-go database/sql   34.1ms
```

**Ассерты и сверка результата (Step 4 брифа):** каждый драйвер после
вставки проверен на `rows == M` (и через собственный insert-ответ, и
авторитетно через `system.parts`). Аналитический SELECT (`SELECT country,
count() AS c, toInt64(sum(revenue) * 100) AS cents FROM demo.<table> GROUP
BY country ORDER BY country` — явный `toInt64(sum(revenue) * 100)` фиксирует
целочисленный тип результата одинаково для всех 6 драйверов, минуя
"плавающую" ширину `Decimal` у разных клиентов) даёт **побайтово идентичный
результат у всех 6 таблиц**: `groups=20, totalRows=1000000,
totalCents=1344564577, checksum=0da6fca6` (CRC32-IEEE канонической строки
`"country|c|cents\n"` по группам — Go `hash/crc32.ChecksumIEEE` и Java
`java.util.zip.CRC32` реализуют один полином, чексуммы сравнимы побайтово
между языками). Финальная фаза `drivers/go -phase=verify` — авторитетная
межъязыковая/междрайверная сверка: ОДНО административное соединение
(`clickhouse-go/v2` native) выполняет тот же SELECT над ВСЕМИ 6 таблицами
подряд и сравнивает чексуммы — исключает гипотезу "разные клиенты
по-разному декодируют один и тот же результат", проверяет именно данные на
диске. Живой прогон: все 6 — `checksum=0da6fca6`, ассерты прошли.

**Живой баг находки и исправления во время прогона:** ch-go SELECT изначально
падал `unexpected type "LowCardinality(String)" (got) instead of "String"
(has)` — `GROUP BY` по `LowCardinality(String)`-колонке сохраняет обёртку
`LowCardinality` в результате ДАЖЕ через `toString(country)` (identity-
оптимизация не снимает её) — SQL упрощён до `country` без `toString`,
ch-go-декодер country переведён на `LowCardinality(String)`
(`new(proto.ColStr).LowCardinality()`). `clickhouse-jdbc`
`PreparedStatement.clearBatch()` в 0.9.0 не очищает внутренний буфер между
`executeBatch()`-вызовами на одном Statement — второй `executeBatch()` на
"очищенном" батче переслал ВСЕ строки с начала (10 чанков по 100k дали
5 500 000 строк в `system.parts` вместо 1 000 000 — ровно `100k×(1+2+…+10)`);
обход — новый `PreparedStatement` на каждый чанк (`drivers/java/.../Main.java`
`insertJdbcBatch`), тот же приём, что `database/sql`-драйвер Go (свежий
`Prepare` на транзакцию).

**Сводная таблица драйверов** (throughput/латентность из прогона выше;
эргономика/фичи — качественная оценка по факту написания стенда):

| Драйвер | Throughput INSERT | SELECT latency | Эргономика | Типы/фичи |
|---|---|---|---|---|
| ch-go (Go) | 541 300 rows/s | 12.5ms | низкая — ручное заполнение `proto.Col*`, явные `Decimal`-алиасы, максимальный контроль | колоночный, полный доступ к нативному протоколу, минимальный оверхед |
| clickhouse-go native (Go) | 458 855 rows/s | 10.8ms | высокая — `PrepareBatch`/`Append`/`Send`, типы Go напрямую | batch API, стриминг результатов, пул соединений |
| clickhouse-go database/sql (Go) | 430 047 rows/s | 34.1ms | высокая — стандартный `database/sql`, знаком любому Go-разработчику | совместим с обвязкой database/sql (миграторы, ORM), батч через транзакцию |
| raw HTTP (net/http) | 901 925 rows/s* | 6.9ms | низкая для типизированных данных (текстовые форматы, ручной парсинг ответа), высокая для bulk-заливки CSV | работает без клиента вообще — curl/прокси/LB/язык без биндинга |
| clickhouse-jdbc (Java) | 129 829 rows/s | 19ms | высокая — стандартный JDBC, совместим с пулами (HikariCP)/ORM/миграторами | универсальный SQL-интерфейс, но HTTP+JDBC-слой поверх — заметный оверхед на батч |
| client-v2 (Java) | 270 621 rows/s | 15ms | средняя — свой API (`Client.insert`/`queryAll`), CSV/бинарные форматы напрямую | без JDBC-обёртки, `GenericRecord` для типобезопасного чтения, быстрее JDBC на этом объёме в ~2x |

\* raw HTTP throughput не apples-to-apples с батчированными (10×100k)
драйверами — один POST на весь файл, см. оговорку выше.

**Python (`clickhouse-connect`) / Rust (`clickhouse-rs`) — обзорно, НЕ
прогонялись** (осознанное scope-решение, см. Global Constraints серии):

| Драйвер | Протокол | Статус в этом стенде | Что известно (документация/экосистема) |
|---|---|---|---|
| `clickhouse-connect` (Python) | HTTP | не прогонялся | официальный клиент ClickHouse Inc, поддерживает pandas/numpy/arrow DataFrame напрямую, async через `clickhouse-connect[async]`; типично медленнее нативных Go/Java-клиентов на построчных операциях (Python overhead), но batch-insert через columnar API (`client.insert(..., column_names=...)`) сопоставим по throughput с HTTP-путём этого стенда (тот же HTTP-транспорт) |
| `clickhouse-rs` (Rust) | нативный | не прогонялся | community-клиент (не официальный ClickHouse Inc), нативный протокол через async (tokio), сериализация через `Row`-derive (serde-подобная); ожидаемо сопоставим с ch-go/clickhouse-go native по throughput (тот же класс — нативный протокол, компилируемый язык без GC-пауз), не измерено в этом стенде |

**Карта выбора «задача → драйвер»:**

- **Bulk-загрузка данных (ETL/миграция), объём известен заранее** → raw
  HTTP/`clickhouse-connect` с columnar-API — простота и один round-trip
  важнее микрооптимизации протокола.
- **Высоконагруженный сервис на Go, вставка событий потоком** →
  clickhouse-go native (`PrepareBatch`) — баланс эргономики и throughput,
  стриминг результатов из коробки.
- **Существующая Go-кодовая база на `database/sql` (миграторы, ORM,
  observability-обвязка завязаны на интерфейс `sql.DB`)** → clickhouse-go
  database/sql — жертвует частью throughput/latency ради совместимости.
- **Экстремальный throughput/минимальная задержка, кастомный протокол
  (напр. собственный ingestion-пайплайн)** → ch-go — цена: ручная работа с
  колонками, знание нативного протокола ClickHouse.
- **Java-сервис с существующей JDBC-инфраструктурой (HikariCP, Spring
  Data, миграторы Flyway/Liquibase)** → clickhouse-jdbc — совместимость
  важнее throughput (в ~2x медленнее client-v2 на этом объёме).
- **Java-сервис без JDBC-требования, чувствителен к throughput** →
  client-v2 — быстрее JDBC, `GenericRecord`/типобезопасные модели без
  накладных расходов JDBC-слоя.
- **Проксирование/балансировщик/скрипт без языка-биндинга (curl,
  observability-экспортёр, serverless-функция)** → HTTP-интерфейс
  напрямую — работает без единой ClickHouse-библиотеки.

**Версии в прогоне:** ClickHouse `26.6.1.1193` (то же, что предыдущие
стенды), clickhouse-go `v2.47.0`, ch-go `v0.73.0` (теперь прямая зависимость
`drivers/go/go.mod`, не `// indirect`), clickhouse-jdbc/client-v2 `0.9.0`.

Content-note: throughput вставок/латентность SELECT — host-зависимы (Docker
Desktop/Windows, compose-сеть, «характерный прогон»). Результат
аналитического SELECT (группы/суммы/чексумма) — детерминирован для этого
датасета и воспроизводим независимо от драйвера/языка — именно это
проверяет `-phase=verify`.

## Проверено живьём (materialized-views, 2026-07-10)

Стенд #4 (`materialized-views/`, `compose/kafka.yml`, `ops/matviews-demo.sh`)
— материализованные представления как триггер на вставку, real-time
предагрегация (`AggregatingMergeTree`/`SummingMergeTree`), проекции, TTL на
партициях, и живой Kafka → ClickHouse пайплайн (`apache/kafka:4.3.1`,
single-node KRaft, franz-go продюсер). Все программные ассерты (fail-loud)
прошли на реальном прогоне (`-phase=all`, 1 000 000 строк основного
датасета + 50 000 Kafka-событий).

### MV как триггер на вставку (Step 1 брифа)

DDL (имена таблиц с префиксом `mv_`, чтобы не пересекаться с `demo.events`
when-olap и `demo.mergetree_events` стенда #2 — общая БД `demo`):

```sql
CREATE TABLE demo.mv_events
(
    event_time  DateTime,
    user_id     UInt64,
    event_type  LowCardinality(String),
    url         String,
    duration_ms UInt32,
    country     LowCardinality(String),
    revenue     Decimal(10,2)
)
ENGINE = MergeTree
ORDER BY (event_time, user_id)
PARTITION BY toYYYYMM(event_time)
SETTINGS index_granularity = 8192;

CREATE TABLE demo.mv_events_agg
(
    country       LowCardinality(String),
    hour          DateTime,
    revenue_state AggregateFunction(sum, Decimal(10,2)),
    users_state   AggregateFunction(uniq, UInt64),
    count_state   AggregateFunction(count)
)
ENGINE = AggregatingMergeTree
ORDER BY (country, hour);

CREATE MATERIALIZED VIEW demo.mv_events_to_agg_mv TO demo.mv_events_agg AS
SELECT
    country,
    toStartOfHour(event_time) AS hour,
    sumState(revenue) AS revenue_state,
    uniqState(user_id) AS users_state,
    countState() AS count_state
FROM demo.mv_events
GROUP BY country, hour;
```

**Content-note: MV — триггер ТОЛЬКО на INSERT, не на существующие данные**
(зафиксировано явным экспериментом, не как утверждение из документации):
1000 строк вставлены в `demo.mv_events` ДО создания MV → MV создан →
`demo.mv_events_agg` **пуста** (`count()=0`), несмотря на уже лежащие в
источнике строки:

```
[agg] pre-MV: 1000 rows вставлено в demo.mv_events ДО создания MV
[agg] СРАЗУ после создания MV: demo.mv_events_agg — 0 active parts, count()=0
  (несмотря на 1000 уже существующих строк в demo.mv_events)
```

После этого `demo.mv_events` очищена (`TRUNCATE`) для честного сравнения, и
основной датасет (1 000 000 строк) загружен батчем **ПОСЛЕ** создания MV:

```
[agg] bulk load: 1000000 rows в demo.mv_events за 2.238726656s (446682 rows/s)
[agg] demo.mv_events_agg: count()=152708 СРАЗУ после bulk load
  (много незамерженных partial-state строк на ключ (country,hour) —
   по одной на каждый вставленный батч, ждущих фонового merge)
```

### Предагрегат: `-Merge` / `finalizeAggregation` (Step 1 брифа)

Сверка через `sumMerge`/`uniqMerge`/`countMerge` с `GROUP BY` (корректна
**независимо** от того, схлопнулись ли state-строки фоновым merge) против
прямого пересчёта `sum`/`uniqExact`/`count` по сырым `demo.mv_events`:

```
[agg-verify до OPTIMIZE FINAL] demo.mv_events_agg (-Merge): 42495 групп (country,hour);
  прямой пересчёт по demo.mv_events: 42495 групп
[assert OK] countMerge()==count(): agg total=1000000, пересчёт total=1000000
[assert OK] sumMerge(revenue)==sum(revenue): agg total=13445645.77, пересчёт total=13445645.77
[agg-verify] uniqMerge(user_id) vs uniqExact(user_id): суммарная относительная
  погрешность 0.0000% (uniq — приближённый HyperLogLog/linear-counting
  оценщик, а НЕ точный подсчёт — честная оговорка, не баг; ассерт допускает
  < 2%, фактическая погрешность на этом датасете — нулевая)
```

`finalizeAggregation()` **без** `GROUP BY` — граница механизма: финализирует
состояние КАЖДОЙ строки саму по себе, не объединяя строки с одинаковым
ключом, которые ещё не схлопнулись merge'ем. После `OPTIMIZE TABLE ... FINAL`
(state-строки: 152708 → 42495, ровно одна на ключ) `finalizeAggregation()`
без `GROUP BY` даёт тот же результат, что `-Merge` с `GROUP BY`:

```
[agg] demo.mv_events_agg: count()=42495 ПОСЛЕ OPTIMIZE TABLE ... FINAL
[agg-verify finalizeAggregation] finalizeAggregation() без GROUP BY вернул
  42495 строк, count() таблицы = 42495 — совпадают
```

### SummingMergeTree (Step 2а брифа)

Более простой случай, чем `AggregatingMergeTree` — числовые колонки вне
`ORDER BY` складываются при merge, без `-State`/`-Merge`:

```sql
CREATE TABLE demo.mv_events_summing
(
    country LowCardinality(String),
    hour    DateTime,
    revenue Decimal(18,2),
    events  UInt64
)
ENGINE = SummingMergeTree
ORDER BY (country, hour);
```

7 отдельных строк (5× `(RU, 10:00)`, 2× `(US, 11:00)`) вставлены поштучно;
после `OPTIMIZE TABLE ... FINAL` — ровно 1 строка на ключ с корректной
суммой (`RU: 10.50+20.25+5.00+100.00+0.25=136.00`, `US: 300.00+199.99=499.99`
— совпало побайтово). **Живая деталь:** SELECT ДО явного `OPTIMIZE FINAL`
уже вернул 1 строку (не 5) — на простаивающем single-node dev-инстансе
фоновый merge scheduler успел схлопнуть крошечные parts раньше, чем наш
клиент сделал следующий запрос. `SummingMergeTree`, как и TTL ниже,
описывается как «фоновый»/«ленивый» механизм — это честно означает
«без гарантированного момента без explicit FINAL», а не «долго ждать»:
на практике фон может отработать за миллисекунды.

### Projections (Step 2б брифа)

Проекция с альтернативным `ORDER BY (user_id)` поверх `demo.mv_events`
(1 000 000 строк, основной `ORDER BY (event_time, user_id)`) — тот же
паттерн, что дал when-olap медленный point-lookup по `id` (12.5% таблицы):

```sql
ALTER TABLE demo.mv_events ADD PROJECTION user_id_proj (SELECT * ORDER BY user_id);
ALTER TABLE demo.mv_events MATERIALIZE PROJECTION user_id_proj;
```

```
[projection] ДО создания проекции (optimize_use_projections=0, полный скан):
  read_rows=1000000, count()=3
[projection] EXPLAIN indexes=1 (WHERE user_id = 213963):
  Expression ((Project names + Projection))
    Aggregating
      Expression
        ReadFromMergeTree (user_id_proj)
        Indexes:
          PrimaryKey
            Keys: user_id
            Parts: 15/15
            Granules: 15/121
[projection] ПОСЛЕ материализации (optimize_use_projections=1, дефолт):
  read_rows=122880, count()=3
[projection] force_optimize_projection=1 выполнился БЕЗ ошибки
  (сервер обязан использовать проекцию, иначе отказ): read_rows=122880
```

`read_rows` упал с 1 000 000 до 122 880 (в 8.1 раза), `count()` не изменился
(`3` в обоих случаях) — проекция не меняет результат, только путь чтения.
Доказательство использования — **двойное**: текст `EXPLAIN indexes=1`
содержит имя проекции (`user_id_proj`), И `force_optimize_projection=1`
выполнился без ошибки (если бы ClickHouse не мог использовать НИ ОДНУ
проекцию для этого запроса — сервер вернул бы отказ).

### TTL на партициях (Step 2в брифа)

Прод-DDL серии — `TTL event_time + INTERVAL 90 DAY` (как в брифе); для живой
проверки в рамках одного хода интервал сжат до 3 суток, строки вставлены с
синтетическим `event_time` (не `now()`), `PARTITION BY toDate(event_time)`:

```sql
CREATE TABLE demo.mv_events_ttl_demo (...)
ENGINE = MergeTree
ORDER BY (event_time, user_id)
PARTITION BY toDate(event_time)
TTL event_time + INTERVAL 3 DAY DELETE;
```

500 строк с `event_time = now-5d` (просрочено) + 500 с `event_time = now`
(свежие):

```
[ttl] вставлено: 500 строк с event_time=2026-07-04 22:50:52 (просрочено), 500 с event_time=2026-07-09 22:50:52 (свежие)
[ttl] ДО явного OPTIMIZE FINAL: count()=500, партиции: 2026-07-04=0 rows, 2026-07-09=500 rows
[ttl] ПОСЛЕ OPTIMIZE FINAL (гарантированная точка): count()=500, партиции: 2026-07-04=0 rows, 2026-07-09=500 rows
[ttl] content-note: партиция 2026-07-04 всё ещё присутствует в system.parts
  КАК ЗАПИСЬ (active part с rows=0) — TTL DELETE опустошил part, но
  физическая очистка пустого part'а — отдельный, более поздний фоновый цикл
```

Как и `SummingMergeTree` выше: TTL (описывается как «ленивый», применяется
фоновыми merge) на этом простаивающем single-node инстансе уже удалил
просроченные строки К МОМЕНТУ ПЕРВОГО SELECT, ещё до нашего явного
`OPTIMIZE TABLE ... FINAL` — стенд честно НЕ ассертит конкретный момент
применения (не гарантирован), только конечный результат ПОСЛЕ `FINAL`
(детерминированная точка). Отдельная живая деталь: опустевший part
партиции остаётся в `system.parts` как активная запись с `rows=0` —
«партиция опустела» и «партиция физически дропнута» — разные события
с разным таймингом.

### Kafka → ClickHouse пайплайн (Step 3 брифа)

`apache/kafka:4.3.1`, single-node KRaft (`compose/kafka.yml`, отдельный
брокер от 3-нодового `kafka-cookbook-net` из
[серии «Kafka: глубокое погружение»](../kafka/) — своя сеть/топология, тот
же образ). Цепочка ENGINE'ов и MV (тот же паттерн Step 1, теперь источник —
Kafka, а не батч-загрузка CSV):

```
demo.mv_events_queue (ENGINE=Kafka, kafka_broker_list='kafka:9092',
  kafka_topic_list='mv-events-stream', kafka_format='JSONEachRow')
  -> demo.mv_events_queue_mv (MV)
  -> demo.mv_events_kafka_raw (MergeTree)
  -> demo.mv_events_kafka_agg_mv (MV, тот же -State-паттерн, что Step 1)
  -> demo.mv_events_kafka_agg (AggregatingMergeTree)
```

**Порядок создания — важная деталь:** топик пересоздаётся (`DeleteTopics` +
`CreateTopics`, идемпотентный сброс между прогонами) **ДО** подключения
`ENGINE=Kafka` консюмера, а не наоборот — если бы уже подключённый консюмер
получил под собой удалённый/пересозданный topic-id, потребовался бы
дополнительный цикл обновления метаданных librdkafka внутри ClickHouse
(было исправлено во время разработки стенда — изначальный порядок
"MV сначала → топик пересоздать потом" ошибочен).

franz-go продюсер — асинхронный `Produce`+callback с финальным `Flush`
(НЕ `ProduceSync` на каждое сообщение — слишком медленно для 50 000):

```
[kafka] продюсер (franz-go): отправлено 50000/50000 сообщений за 107.993158ms (462992 msg/s)
```

Опрос ClickHouse — `SELECT count()` **с таймаутом** (120с в реальном
прогоне; `-kafka-timeout` по умолчанию 60с в `ops/matviews-demo.sh`, но
живой замер лага потребовал больше — см. ниже), интервал 500мс, **НЕ**
бесконечный цикл:

```
[kafka] опрос demo.mv_events_kafka_raw (интервал 500ms, таймаут 2m0s):
  50000 rows видно после 46.752125741s (94 опросов), отправлено продюсером=50000
[assert OK] Kafka-пайплайн: обработано в CH (50000) == отправлено продюсером (50000)
```

**Честный лаг:** продюсер отправил 50 000 сообщений за 108мс (сеть), но
ClickHouse потребовалось **46.75с**, чтобы все они стали видны в
`demo.mv_events_kafka_raw` — это НЕ сетевая задержка, а холодный старт
консюмера `ENGINE=Kafka` (подключение к брокеру, join consumer group) плюс
периодичность `kafka_flush_interval_ms=3000` (укорочено с дефолтных
7500мс). На меньшем объёме (5000 сообщений, отдельный smoke-прогон)
латентность видимости была похожей (~48с) — большая часть времени это
именно cold start консюмера, не пропускная способность потока. Дефолт
таймаута опроса в `ops/matviews-demo.sh` — 60с; для 50 000 событий в этом
прогоне заложено 120с (`-kafka-timeout=120s`), чтобы не зависеть от
дрожания cold-start на разном железе.

Предагрегат Kafka-пайплайна сверен так же, как Step 1 (после
`OPTIMIZE TABLE demo.mv_events_kafka_agg FINAL`):

```
[agg-verify Kafka-пайплайн] demo.mv_events_kafka_agg (-Merge): 30 групп; пересчёт: 30 групп
[assert OK] countMerge()==count(): agg total=50000, пересчёт total=50000
[assert OK] sumMerge(revenue)==sum(revenue): agg total=757510.25, пересчёт total=757510.25
[agg-verify Kafka-пайплайн] uniq() относительная погрешность: 0.0000%
```

### Ассерты (все прошли, fail-loud)

Step 1: pre-MV rows==ожидаемое; agg count()==0 сразу после CREATE MV
(content-note, не баг); bulk rows==ожидаемое; число групп agg==пересчёт (до
и после `OPTIMIZE FINAL`); `countMerge()`==`count()`; `sumMerge(revenue)`==
`sum(revenue)` (побайтовое равенство Decimal); `uniqMerge` относительная
погрешность < 2% (факт 0.0000%); `OPTIMIZE FINAL` не увеличивает число
строк; `finalizeAggregation()` без `GROUP BY` после `FINAL` == `count()`.
Step 2а: ровно 1 строка на ключ после `FINAL`, сумма совпадает с ожидаемой
(побайтово). Step 2б: `EXPLAIN` упоминает имя проекции;
`force_optimize_projection=1` не падает; результат `count()` одинаков без/с
проекцией/forced; `read_rows` с проекцией строго меньше, чем без (факт
8.1x). Step 2в: `count()` после `FINAL` == только свежие строки;
просроченная партиция — 0 строк; свежая — не тронута. Step 3: все
сообщения отправлены (`sent==requested`); обработано в CH == отправлено в
пределах таймаута; agg vs пересчёт (Kafka-источник) — та же сверка, что
Step 1.

### Версии в прогоне

ClickHouse `26.6.1.1193` (то же, что предыдущие стенды), clickhouse-go
`v2.47.0`, `github.com/shopspring/decimal` `v1.4.0`, Kafka
`apache/kafka:4.3.1` (KRaft single-node, тот же образ, что
[серия «Kafka: глубокое погружение»](../kafka/)), franz-go `v1.21.5`,
`franz-go/pkg/kadm` `v1.18.0`.

Content-note: throughput вставок/продюсера — host-зависим («характерный
прогон», Docker Desktop/Windows). Число групп/сумм/read_rows/parts —
детерминировано для этого датасета и версии CH. Лаг видимости Kafka→CH
(46.75с на 50k событий) — host-зависим (Docker Desktop/Windows compose-сеть,
cold start консюмера) и, в отличие от размеров/сумм, НЕ переносим как
абсолютное число на другое железо — воспроизводим по порядку величины
(десятки секунд на cold start + `kafka_flush_interval_ms`), не как точное
значение.

## Проверено живьём (distributed, 2026-07-10)

Стенд #5 (`distributed/`, `compose/cluster.yml`, `config/*.xml`,
`ops/distributed-demo.sh`) — распределённый ClickHouse: шардирование,
репликация, ClickHouse Keeper, отказ узла. ОТДЕЛЬНЫЙ, самостоятельный
compose (4 CH-ноды + 1 Keeper, своя сеть `clickhouse-cluster-net`) — не
поверх `compose/compose.yml`. Все программные ассерты (fail-loud) прошли на
реальном прогоне (`ops/distributed-demo.sh -rows=500000`, полный цикл — от
`up -d` до `down -v` — вручную и через сам оркестрационный скрипт,
одинаковый результат оба раза).

### Топология (Step 1 брифа)

4 CH-ноды (`clickhouse/clickhouse-server:26.6.1.1193`, та же версия, что вся
серия) — 2 шарда x 2 реплики — + 1 узел `clickhouse/clickhouse-keeper:
26.6.1.1193` (сверено живьём: `clickhouse-keeper --version` →
`ClickHouse keeper version 26.6.1.1193`). Один узел Keeper — dev-упрощение
(брифа Step 1: "прод — 3 для кворума"; при потере ЕДИНСТВЕННОГО dev-узла
Keeper вся координация ReplicatedMergeTree встаёт — это НЕ отрабатывается в
этом стенде, честно зафиксировано как scope-граница, не прод-рекомендация).

`remote_servers` (`config/remote-servers.xml`, `internal_replication=true`) +
per-нода `macros` (`config/macros-s1-r1.xml` и т.д., `{shard}`/`{replica}`
для пути ReplicatedMergeTree) — общие для всех 4 нод, различаются только
macros. `SELECT shard_num, replica_num, host_name FROM system.clusters
WHERE cluster='events_cluster'` — живой вывод, 4 строки:

```
shard=1 replica=1 host=ch-s1-r1
shard=1 replica=2 host=ch-s1-r2
shard=2 replica=1 host=ch-s2-r1
shard=2 replica=2 host=ch-s2-r2
```

`SELECT * FROM system.zookeeper WHERE path='/'` (ДО создания каких-либо
таблиц) — `[keeper, clickhouse]` — служебные znode самого Keeper,
подтверждает, что CH достижимо связан с `keeper1` ДО того, как
ReplicatedMergeTree вообще начал что-либо туда писать.

### Схема: ReplicatedMergeTree + Distributed (Step 2 брифа)

`CREATE TABLE demo.events ON CLUSTER events_cluster ... ENGINE =
ReplicatedMergeTree('/clickhouse/tables/{shard}/events', '{replica}') ...` —
ОДНА DDL-строка выполняется на всех 4 нодах (ON CLUSTER резолвит DDL-очередь
через Keeper), каждая нода подставляет СВОИ macros. `CREATE TABLE
demo.events_distributed ON CLUSTER events_cluster ... ENGINE =
Distributed(events_cluster, demo, events, cityHash64(user_id))` — поверх
кластера. После создания — `SELECT name FROM system.zookeeper WHERE
path='/clickhouse/tables/{1,2}/events/replicas'` вернул `[r1, r2]` для ОБОИХ
шардов — не просто "DDL не упал", а видимое снаружи подтверждение, что
Keeper реально знает про обе реплики каждого шарда.

**Вставка через Distributed + распределение по шардам** (`dataset/main.go
-rows=500000 -seed=42`, батч 100 000, `insert_distributed_sync=1`):

```
[insert-dist] 500000 rows вставлено в demo.events_distributed (insert_distributed_sync=1) за 2.512832941s (198979 rows/s)
[shard-dist] shard1=250050 shard2=249950 total=500000
```

Живой факт, НЕ угаданный заранее: даже с `insert_distributed_sync=1` сразу
после вставки `SELECT shardNum(), count() FROM demo.events_distributed
GROUP BY shardNum()` показал **shard1=250050 shard2=233212 total=483262**
(недостача 16 738 строк) — воспроизводится детерминированно на этом
датасете/seed при каждом прогоне. Причина: `insert_distributed_sync`
гарантирует, что батч ACK'нут КАКОЙ-ТО одной живой репликой шарда
(load_balancing выбирает её независимо на каждый батч — не всегда "первая"
реплика), а не обеими сразу; второй реплике той же пары ещё нужно время
догнать через Keeper-репликацию (в этом прогоне — единицы миллисекунд:
шард 1 и шард 2 сошлись за `3.9ms`/`3.8ms`, 1 опрос). Стенд опрашивает ОБЕ
реплики каждого шарда ДО СХОДИМОСТИ (`pollShardConverged`, таймаут 30s,
интервал 200ms) вместо того, чтобы доверять первому же fan-out read —
контрольный повторный запрос через Distributed после сходимости стабильно
даёт `shard1=250050 shard2=249950 total=500000`. `cityHash64(user_id)`
реально распределяет (оба шарда непусты, доля близка к 50/50 — 250050 vs
249950 на 500000).

### Репликация + дедупликация (Step 3 брифа)

3000 marker-строк (`country='ZZ'`, `event_type='replication_probe'`,
фиксированный `event_time` — НЕ `time.Now()`, для байт-в-байт
воспроизводимости блока) вставлены НАПРЯМУЮ в `ch-s1-r1` (минуя Distributed
— отдельное соединение на конкретную ноду), затем `ch-s1-r2` (вставка ей
НЕ посылалась) опрошена до появления той же строки:

```
[replication] вставлено 3000 marker-строк напрямую в ch-s1-r1 (МИМО Distributed): count(marker) 0 -> 3000
[replication] ch-s1-r2 увидела marker-строки через Keeper-репликацию: 3000/3000 rows за 212.155406ms (2 опросов)
```

Дедупликация — та же самая (байт-в-байт идентичная) пачка отправлена в
`ch-s1-r1` ВТОРОЙ раз отдельным `INSERT`:

```
[dedup] тот же самый блок (3000 идентичных строк) отправлен в ch-s1-r1 ВТОРОЙ раз: count(marker) 3000 -> 3000
[dedup] ch-s1-r2 после дубля: count(marker)=3000 (без изменений)
```

`ReplicatedMergeTree` сравнил хеш вставляемого блока с недавней историей
(координация через Keeper, `replicated_deduplication_window`) и отбросил
дубликат целиком — `count()` НЕ увеличился второй раз (осталось 3000, а не
6000), ни на принимающей ноде, ни на реплике (реплицировать нечего — дубль
отброшен ДО появления в логе репликации).

### Отказ узла: «реплика недоступна» vs «шард недоступен» (Step 4 брифа)

Docker stop/start — СНАРУЖИ Go-процесса (`ops/distributed-demo.sh`), между
фазами; фазы читают/пишут JSON-снимок baseline (`dataset/out/distributed-
state.json`, `shard1=253050 shard2=249950 total=503000` — main-датасет
500000 + 3000 marker-строк из Step 3, дедуп не удвоил).

**Сценарий А — ОДНА реплика шарда недоступна** (`docker stop
clickhouse-cluster-s1-r2`, `ch-s1-r1` — та же реплика шарда 1 — жива):

```
[replica-down] SELECT count() FROM demo.events_distributed = 503000 (baseline=503000)
```

Полный результат, БЕЗ потерь, БЕЗ `skip_unavailable_shards` — Distributed
прозрачно использует живую реплику шарда, клиент ничего не замечает.

**Сценарий Б — ВЕСЬ шард недоступен** (`docker stop clickhouse-cluster-s2-r1
clickhouse-cluster-s2-r2` — ОБЕ реплики шарда 2; `ch-s1-r1`/`ch-s1-r2` живы):

```
[shard-down] default (skip_unavailable_shards=0): запрос упал с ошибкой:
  code: 279, message: All connection tries failed. Log:
  Code: 32. DB::Exception: Attempt to read after eof. (ATTEMPT_TO_READ_AFTER_EOF)
  ...: While executing Remote
[shard-down] skip_unavailable_shards=1: SELECT count() FROM demo.events_distributed = 253050 (только шард 1; baseline shard1=253050, полный baseline total=503000)
```

Без `skip_unavailable_shards` Distributed ОБЯЗАН получить ответ от КАЖДОГО
шарда — шард 2 недостижим ЦЕЛИКОМ (ни одна из двух реплик не отвечает) —
запрос падает с ошибкой (`All connection tries failed`), а не отдаёт
частичные данные молча. С `skip_unavailable_shards=1` — честный частичный
результат: ровно данные живого шарда (253050 == baseline.shard1), меньше
полного (`< 503000`), БЕЗ ошибки. Это и есть измеримая разница «реплика
недоступна» (Distributed сама справляется, без настроек) vs «шард
недоступен» (нужно осознанное решение — упасть или отдать частичный ответ).

**Восстановление** (`docker start` обеих нод, дождаться healthy):

```
[restart-verify] shard1=253050 shard2=249950 total=503000 (baseline: shard1=253050 shard2=249950 total=503000)
```

Полное совпадение с baseline — данные НЕ потеряны (именованные docker-тома
пережили `stop`/`start`, это НЕ `down -v`), распределение по шардам не
изменилось.

### Ассерты (все прошли, fail-loud)

Step 1: `system.clusters` — ровно 4 строки, шард/реплика соответствие
хостам; `system.zookeeper` root непуст. Step 2: `demo.events` +
`demo.events_distributed` созданы на всех 4 нодах; Keeper знает про 2
реплики на шард; вставленные rows == expected (500000); сумма по шардам
ПОСЛЕ сходимости == вставленным; оба шарда непусты; контрольный fan-out read
после сходимости == expected. Step 3: marker rows выросли ровно на N на
принимающей ноде; вторая реплика догнала (== принимающей); повторная
идентичная вставка НЕ увеличила count (дедуп); реплика без изменений после
дедупа. Step 4: реплика-недоступна — total == baseline (без потерь); шард-
недоступен без skip — ошибка; шард-недоступен со skip — partial ==
baseline.shard1, partial < baseline.total; после restart — total ==
baseline, shard1/shard2 == baseline (оба).

### Версии в прогоне

`clickhouse/clickhouse-server:26.6.1.1193` + `clickhouse/clickhouse-keeper:
26.6.1.1193` (то же семейство версий, что вся серия — сверено живьём:
`clickhouse-keeper --version` → `ClickHouse keeper version 26.6.1.1193
(official build)`), clickhouse-go `v2.47.0` (`distributed/go.mod`).

Content-note: throughput вставки (`198 979 rows/s`) — host-зависим
(«характерный прогон», Docker Desktop/Windows). Распределение по шардам
(50050/49950 на 100k = близко к равномерному), число marker-строк,
результат дедупа, ассерты failover-сценария — детерминированы для этого
датасета/seed/версии CH и воспроизводимы. Единственное исключение — задержка
между "batch ACK'нут одной репликой" и "обе реплики сошлись" (в этом
прогоне — единицы миллисекунд для основного датасета, `~212ms` для marker-
строк Step 3) — host-зависима по абсолютному значению, но НАПРАВЛЕНИЕ
(нужен poll-until-converged, а не мгновенное доверие первому fan-out read)
воспроизводимо всегда.

## Проверено живьём (ops, 2026-07-10)

Стенд #6 (`ops-stand/`, `ops/ops-demo.sh`) — эксплуатация и тюнинг
single-node ClickHouse: parts/merges, mutations (UPDATE/DELETE), мониторинг
`system.*` + `BACKUP`/`RESTORE` round-trip, codec-тюнинг. Общий
`compose/compose.yml` (PG этому стенду не нужен) + `config/backups.xml`
(смонтирован туда же — `allowed_path` для `BACKUP TABLE ... TO File(...)`
внутри уже существующего тома `clickhouse-data`, отдельный backup-диск не
понадобился). Датасет — выделенный CSV `dataset/main.go -rows=750000
-seed=42` (`dataset/out/events-ops.csv`): первые 200 000 строк — `ops_events`
(Step 1/2/3) и `ops_gran_fine`/`ops_gran_coarse` (Step 4 бонус, тот же
диапазон), следующие 500 000 (200k–700k) — `ops_codec_zstd`/`ops_codec_delta`
(Step 4). Полный прогон `ops/ops-demo.sh -rows=750000` (`up -d` → генерация
датасета → `-phase=all`) — все программные ассерты (fail-loud) прошли,
включая повторный чистый прогон (`down -v` → `up -d` заново) с идентичным
результатом.

### Parts и merges (Step 1 брифа)

`SYSTEM STOP MERGES demo.ops_events` вокруг вставки 200 отдельных
`PrepareBatch`/`Send`-вызовов по 1000 строк (`loadManySmallBatches`,
единая открытая CSV-последовательность на весь вызов — не путать с
повторным `skip` на каждый батч, это была бы O(n²)-ошибка, найденная и
исправленная во время написания стенда). Урок стенда #4 (materialized-views)
подтверждён снова: фоновый merge scheduler на простаивающем single-node
инстансе схлопывает parts за секунды, а не долго ждёт.

```
[merges] 200 батчей по 1000 строк (SYSTEM STOP MERGES во время вставки): 200000 rows in 14.12s, 600 active parts
[merges] SYSTEM START MERGES; наблюдение фона 10s
[merges]   t+1s: active_parts=10 (было 600), system.merges в процессе=0
[merges] после 10s фонового наблюдения (без явного OPTIMIZE): 10 active parts (было 600 до START MERGES)
[merges] после OPTIMIZE TABLE ... FINAL (форсированное схлопывание): 3 active parts
```

600 active parts на 200 000 строк (в среднем 3 part на батч, не 1 — 1000
случайно перемешанных по 90-дневному окну строк в одном батче обычно
задевают 2–3 месячные партиции `toYYYYMM(event_time)`, каждая партиция —
отдельный part). Всего за **1 секунду** после `SYSTEM START MERGES` фон
схлопнул 600 → 10 parts (честно: не успели увидеть промежуточные шаги —
опрос раз в секунду оказался грубее самого merge). Явный `OPTIMIZE TABLE
... FINAL` довёл до 3 (по одной на месячную партицию, задетую 200k строками
— минимум для этой схемы партиционирования).

### Mutations: UPDATE/DELETE — анти-паттерн (Step 2 брифа)

`ALTER TABLE demo.ops_events UPDATE revenue=0 WHERE country='KZ'` и `ALTER
... DELETE WHERE country='JP'` — БЕЗ `mutations_sync` (реальное поведение по
умолчанию), опрос `system.mutations.is_done` с таймаутом 90с (не
бесконечно):

```
[mutations] ALTER ... UPDATE revenue=0 WHERE country='KZ' (5961 строк подпадает): submit=5.7ms (выглядит мгновенно), completion=211.2ms (parts_to_do at submit=3) — РЕАЛЬНАЯ стоимость
[mutations] ALTER ... DELETE WHERE country='JP' (6115 строк удаляется): submit=5.3ms (выглядит мгновенно), completion=7.7ms (parts_to_do at submit=0) — РЕАЛЬНАЯ стоимость
[mutations] rows: before=200000, after=193885 (ожидаемо after == before - deleted(6115))
```

`submit` (~5мс) — это просто отправка `ALTER`, НЕ стоимость операции;
реальная стоимость — `completion` (submit → `is_done=1`): **211мс** на
UPDATE, **7.7мс** на DELETE — оба на несколько порядков дороже эквивалентной
точечной OLTP-операции (для контраста: `pointops.go` стенда #1 показал
CH `ALTER ... UPDATE` по PK на ~1с против PG ~3мс — там же датасет крупнее и
затронуто больше parts). Почему дороже: MergeTree parts иммутабельны —
`UPDATE`/`DELETE` не правят строку на месте, а перечитывают и переписывают
ЦЕЛИКОМ каждый part, содержащий хоть одну подходящую под `WHERE` строку
(`parts_to_do` — число таких parts). Стоимость растёт с размером затронутых
partitions, а не с числом изменённых строк — точечные апдейты в CH
масштабируются плохо.

**Честная гонка, найденная живьём:** DELETE-мутация (маленький поднабор,
несколько parts) завершилась (`is_done=1`) быстрее (**7.7мс**), чем клиент
успел сделать самый первый `SELECT` из `system.mutations` — `parts_to_do`
прочитан уже равным `0` (мутация уже done к моменту чтения). Стенд не прячет
это: печатает честную деталь вместо того, чтобы делать вид, будто
`parts_to_do at submit` всегда положителен. Отдельная находка: `is_done=1` в
`system.mutations` не гарантирует, что новый набор active parts уже виден в
`system.parts` СЛЕДУЮЩЕМУ запросу той же сессии — на первом прогоне (до
фикса) финальный `SELECT` количества строк после DELETE иногда возвращал
старое значение (200000 вместо 193885) при `is_done=1`. Исправлено
poll-до-совпадения с таймаутом (`waitForRows`, до 5с) вместо однократного
чтения — тот же принцип «опрос с таймаутом, не единичный запрос», что и для
самой мутации.

### Мониторинг system.* + BACKUP/RESTORE (Step 3 брифа)

`system.query_log` (после `SYSTEM FLUSH LOGS`, тот же приём, что стенды
#1/#2):

```
[monitoring] system.query_log (SELECT count() WHERE country IN (RU,US,DE)): count()=115877, query_duration_ms=3, read_rows=193885, memory_usage=683.30 KiB
```

Размер/сжатие по колонкам — **живая находка**: `system.columns.
data_compressed_bytes`/`data_uncompressed_bytes` (метрика брифа дословно)
на этом стенде оказался **0 сразу после `RESTORE TABLE`** (кеш метаданных
`system.columns` не обновился синхронно с восстановлением). Переключились
на `system.parts_columns` (агрегирует напрямую по активным parts, не
кешируемое представление) — рабочий источник правды:

```
[monitoring]   event_time   compressed=3.70 MiB     uncompressed=8.12 MiB     ratio=2.20x
[monitoring]   url          compressed=3.70 MiB     uncompressed=8.12 MiB     ratio=2.20x
[monitoring]   revenue      compressed=3.70 MiB     uncompressed=8.12 MiB     ratio=2.20x
[monitoring]   country      compressed=3.70 MiB     uncompressed=8.12 MiB     ratio=2.20x
[monitoring]   user_id      compressed=3.70 MiB     uncompressed=8.12 MiB     ratio=2.20x
```

Все 5 колонок показывают ОДИНАКОВЫЙ размер (~3.70 MiB) — тоже живая деталь,
не баг: таблицы этого стенда небольшие (десятки-сотни тысяч строк), их parts
по умолчанию остаются в формате `Compact` (< 10 MiB на part), а в
`Compact`-parts ClickHouse хранит ВСЕ колонки в одном физическом файле —
`data_compressed_bytes` на колонку в этом случае равен размеру ВСЕГО part'а.
Добавили `min_bytes_for_wide_part=0` в DDL этого стенда (форсирует `Wide`
формат — файл на колонку — с первого же part'а), без чего Step 3/Step 4
сравнение размера по колонкам было бы бессмысленным (см. `system.metrics`/
`asynchronous_metrics` ниже и codec-сравнение — оба зависят от этого фикса).

```
[monitoring] system.metrics: Query=1, TCPConnection=1, MemoryTracking=121210638
[monitoring] system.asynchronous_metrics: Uptime=53.56, NumberOfTables=173.00, TotalPartsOfMergeTreeTables=31.00
```

**BACKUP/RESTORE round-trip** — `BACKUP TABLE demo.ops_events TO
File('/var/lib/clickhouse/backups/....zip')`, `DROP TABLE` (симуляция
потери), `RESTORE TABLE ... FROM File(...)` (DDL восстанавливается из
метаданных бэкапа сам, без ручного `CREATE TABLE`). Сверка — count() +
порядко-независимая контрольная сумма `sum(cityHash64(*))` по всей таблице
(тот же приём, что CRC32-сверка стенда #3, только построчный хеш всех
колонок целиком, не по группам):

```
[backup] до бэкапа: count()=193885, checksum(sum(cityHash64(*)))=1464479518751944986
[backup] BACKUP TABLE demo.ops_events TO File(...): 152ms (id=..., status=BACKUP_CREATED)
[backup] DROP TABLE demo.ops_events (симуляция потери данных)
[backup] RESTORE TABLE demo.ops_events FROM File(...): 25.3ms (id=..., status=RESTORED)
[backup] после restore: count()=193885, checksum=1464479518751944986
```

count() и checksum совпали побайтово до/после. Живая находка по правам
доступа: `BACKUP TABLE ... TO File(...)` требует `<backups><allowed_path>`
в конфиге сервера (`config/backups.xml`, `/var/lib/clickhouse/backups/`) —
но каталог, созданный `docker exec` (по умолчанию `root`), не был доступен
на запись самому процессу `clickhouse-server` (работает под `uid 101`,
пользователь `clickhouse`, официальный образ дропает привилегии) — первый
прогон упал `Permission denied`. Исправлено `chown clickhouse:clickhouse` в
`ops/ops-demo.sh` после `mkdir`.

### Codec-тюнинг: ZSTD(3) vs Delta+ZSTD(3) (Step 4 брифа)

Две отдельные таблицы (`ops_codec_zstd`/`ops_codec_delta`, идентичная схема
кроме `event_time`), одинаковый диапазон CSV (200k–700k, 500 000 строк),
`OPTIMIZE TABLE ... FINAL` перед замером:

```
[codec] event_time CODEC(ZSTD(3)):        compressed=8.99 MiB uncompressed=20.93 MiB (ratio 2.33x)
[codec] event_time CODEC(Delta, ZSTD(3)): compressed=8.13 MiB uncompressed=20.93 MiB (ratio 2.58x)
[codec] Delta+ZSTD компактнее ZSTD-only на event_time на 9.5% (compressed 8.13 MiB vs 8.99 MiB)
```

`Delta, ZSTD(3)` даёт **9.5%** меньший размер колонки `event_time`, чем
`ZSTD(3)` в одиночку — временной ряд внутри part физически почти монотонен
(`ORDER BY (event_time, user_id)`), `Delta` кодирует разницы соседних
значений (маленькие числа сжимаются лучше), `ZSTD` добивает остаток.

**Бонус — `index_granularity`** (`ops_gran_fine`=128 vs `ops_gran_coarse`
=8192, тот же 200k-диапазон, что `ops_events`, узкий фильтр 1 сутки):

```
[granularity] fine (index_granularity=128):   count()=2193, read_rows=1025 (из 200000 total)
[granularity] coarse (index_granularity=8192): count()=2193, read_rows=65536 (из 200000 total)
```

Одинаковый результат (`count()=2193` в обоих), но мелкая гранула читает
**в 64 раза меньше строк** (1025 vs 65536) на узком фильтре — разреженный
первичный индекс с шагом 128 отсекает гранулы намного точнее, чем с шагом
8192 (цена — больше индексных записей в памяти, не измерялась в этом
стенде).

**Бонус — `max_threads`/`max_memory_usage`:**

```
[threads] max_threads=1: count()=331131 за 4.5ms
[threads] max_threads=4: count()=331131 за 4.1ms (host-зависимо — «характерный прогон»)
[memory] max_memory_usage=1000000 (1MB) на groupArray(url) по country: код 241, "Query memory limit exceeded: would use 10.52 MiB ..., maximum: 976.56 KiB"
```

Результат не зависит от `max_threads` (детерминированный `count()`); разница
во времени между 1 и 4 потоками на этом объёме в пределах шума (маленький
датасет, host-зависимо). `max_memory_usage=1MB` на тяжёлом
`groupArray(url) GROUP BY country` реально отклонён сервером — не просто
настройка на бумаге, а рабочий защитный механизм.

### Ассерты (все прошли, fail-loud)

Step 1: загружено rows == smallBatches×smallBatchSize; active_parts до
`START MERGES` >= smallBatches/2 (много parts); active_parts после
`OPTIMIZE FINAL` < до. Step 2: обе мутации завершились (`is_done=1`) в
пределах таймаута; `parts_to_do` прочитан для обеих; rows после DELETE ==
before − deleted (после settle-поллинга `system.parts`, до 5с). Step 3:
monitoring-запрос вернул >0 строк; `read_rows` в query_log > 0; RESTORE
статус не содержит FAIL/ERROR; count()/checksum после restore == до бэкапа.
Step 4: одинаковое число строк в обеих codec-таблицах; `Delta+ZSTD` <
`ZSTD`-only для `event_time`; granularity fine/coarse — одинаковый
результат, `read_rows` fine <= coarse; `max_threads` не меняет результат;
`max_memory_usage=1MB` на тяжёлом запросе — реальный отказ сервера.

### Версии в прогоне

ClickHouse `26.6.1.1193` (то же, что вся серия), clickhouse-go `v2.47.0`,
`github.com/shopspring/decimal` `v1.4.0` (`ops-stand/go.mod`).

Content-note: throughput вставок/латентность запросов — host-зависимы
(«характерный прогон», Docker Desktop/Windows). Число active parts,
`read_rows`, размеры/коэффициенты сжатия колонок, результат codec-сравнения,
checksum backup/restore — детерминированы для этого датасета/seed/версии CH
и воспроизводимы. Абсолютное время мутаций (211мс/7.7мс) и фонового
схлопывания parts (600→10 за ~1с) — host-зависимо по значению, но
НАПРАВЛЕНИЕ (мутация на порядки дороже точечного OLTP; фон может схлопнуть
parts быстрее, чем секундный опрос успевает заметить промежуточные шаги) —
воспроизводимо всегда.

## Проверено живьём (s3, 2026-07-10)

Стенд #7 (`s3/`, `compose/minio.yml`, `config/storage.xml`,
`ops/s3-demo.sh`) — ClickHouse и S3: многоуровневое хранение (тиринг через
`storage_configuration`/`storage_policy`, MinIO) + табличная функция
`s3()` (Parquet round-trip + ingestion). **Флагманский стенд серии:**
живой контраст с Kafka tiered storage (`../kafka/README.md`, секция
«Проверено живьём (storage, ...)») — та серия честно зафиксировала tiered
storage как **НЕ воспроизведённый** (образ `apache/kafka:4.3.1` не
поставляется с реализацией плагина `RemoteStorageManager`, только с
фреймворком). Здесь — наоборот: **CH S3-тиринг воспроизведён живьём
целиком**, part физически переезжает на MinIO, запрос к нему по-прежнему
корректен. Полный прогон `ops/s3-demo.sh` (`up -d` compose.yml+minio.yml →
создание бакета `chdata` на MinIO → restart clickhouse → `-phase=all`) —
все программные ассерты (fail-loud) прошли, включая повторный чистый
прогон (`down -v` → заново с нуля) с идентичным результатом (те же
числа/центы/disk_name байт-в-байт).

### MinIO + storage_configuration (Step 1 брифа)

`minio/minio:RELEASE.2025-09-07T16-13-09Z` (образ бандлит `mc` — не
понадобился отдельный `minio/mc`, см. `ops/s3-demo.sh`), бакет `chdata`
(dev-ключи `minioadmin`/`minioadmin123`, НЕ секрет — локальный стенд, тот
же паттерн, что `../opensearch/ism/docker-compose.yml`). `config/
storage.xml` смонтирован в `compose/compose.yml` **безусловно** (общий
compose для всей серии, другие стенды поднимают его без MinIO) —
`skip_access_check=true` внутри диска `s3` не даёт серверу упасть при
старте, когда MinIO не поднят; реальная проверка диска происходит в
`phasePolicy` уже после того, как `ops/s3-demo.sh` поднял `minio.yml` и
создал бакет.

Диск `s3` и storage policy `hot_cold` (том `hot`=диск `default`, том
`cold`=диск `s3`) — живой вывод `system.disks`/`system.storage_policies`:

```
[policy] system.disks:
[policy]   name=default type=Local path=/var/lib/clickhouse/
[policy]   name=s3 type=ObjectStorage path=/var/lib/clickhouse/disks/s3/
[policy] system.storage_policies:
[policy]   policy_name=default volume_name=default volume_priority=1 disks=default
[policy]   policy_name=hot_cold volume_name=hot volume_priority=1 disks=default
[policy]   policy_name=hot_cold volume_name=cold volume_priority=2 disks=s3
```

Живая деталь: `system.disks.type` для s3-диска в CH `26.6.1.1193` —
`ObjectStorage` (не буквально `s3`, как можно было бы предположить из DDL
`<type>s3</type>`) — брифа Step 1 просил именно проверить `system.disks`/
`system.storage_policies` живьём, а не угадать имя типа заранее.

### S3-тиринг (Step 2 брифа)

DDL — `demo.s3_events`, `PARTITION BY toDate(event_time)` (та же техника,
что TTL-демо стенда #4: партиция целиком уезжает, не переписывается
построчно), `storage_policy='hot_cold'`, прод-`TTL event_time + INTERVAL
30 DAY TO VOLUME 'cold'`:

```sql
CREATE TABLE demo.s3_events
(
    event_time  DateTime,
    user_id     UInt64,
    event_type  LowCardinality(String),
    url         String,
    duration_ms UInt32,
    country     LowCardinality(String),
    revenue     Decimal(10,2)
)
ENGINE = MergeTree
ORDER BY (event_time, user_id)
PARTITION BY toDate(event_time)
TTL event_time + INTERVAL 30 DAY TO VOLUME 'cold'
SETTINGS storage_policy = 'hot_cold', index_granularity = 8192
```

Синтетический генератор этого стенда (`s3/insert.go`, НЕ общий
`dataset/main.go` серии — честно зафиксировано: этот стенд не про
сравнение на общем датасете #1/#3/#8, только про тиринг/round-trip)
вставил 300 000 «старых» строк (`event_time` в пределах суток
`2026-05-26`, партиция на 45 дней старше «сейчас») и 300 000 «свежих»
(`event_time` в пределах `2026-07-10`):

```
[tiering] вставлено: 300000 старых строк (партиция 2026-05-26) за 843.255621ms (355764 rows/s), 300000 свежих строк (партиция 2026-07-10) за 815.151106ms (368030 rows/s)
```

**Живая находка (тот же класс, что TTL-демо стенда #4):** старая партиция
на 45 дней старше «сейчас» — УЖЕ старше 30-дневного TTL в момент вставки,
условие `TO VOLUME 'cold'` выполнено сразу, не через 30 дней ожидания. На
простаивающем single-node dev-инстансе фоновый merge scheduler подобрал
это условие и переместил партицию на s3 САМ — ещё ДО того, как стенд успел
выполнить explicit `ALTER TABLE ... MOVE PARTITION`:

```
[tiering] ДО explicit MOVE PARTITION: 2026-05-26@s3(rows=300000,bytes=6.37 MiB), 2026-07-10@default(rows=50000,bytes=1.04 MiB) x6
[tiering] content-note: фоновый merge scheduler УЖЕ переместил партицию 2026-05-26 на диск 's3' по правилу TTL ... TO VOLUME 'cold' ДО нашего explicit ALTER ...
```

Стенд не полагается на угаданный момент фона — `ALTER TABLE ... MOVE
PARTITION '2026-05-26' TO DISK 's3'` **выполняется всегда**, проверка
живьём подтвердила его идемпотентность: если реально попытаться выполнить
его над партицией, все части которой уже на `s3`, ClickHouse отказывает
кодом `479` (`All parts of partition '20260526' are already on disk
's3'`) — стенд явно проверяет состояние ПЕРЕД ALTER (`system.parts.
disk_name`) и пропускает вызов как no-op в этом случае, вместо того чтобы
слепо звать ALTER и падать на живом прогоне. Это и есть **брифа Step 2
"форсировать перемещение ... для детерминизма"**, реализованное честно:
результат (партиция на `s3`) гарантирован ЛЮБЫМ путём — либо фон успел
сам, либо ALTER довершает работу.

`system.parts.disk_name` после — окончательное состояние (детерминировано
воспроизведено в двух независимых прогонах, включая полный `down -v` →
с нуля):

```
[tiering] ПОСЛЕ MOVE PARTITION: 2026-05-26@s3(rows=300000,bytes=6.37 MiB), 2026-07-10@default(rows=50000,bytes=1.04 MiB) x6
[assert OK] часть перемещённой партиции 2026-05-26 на disk_name='s3' (факт: s3)
[assert OK] часть НЕтронутой партиции 2026-07-10 осталась на disk_name='default' (факт: default) x6
[tiering] размер на диске: старая партиция (s3)=6.37 MiB, свежая партиция (default)=6.24 MiB
```

**Запрос всё ещё работает** — данные читаются С S3 прозрачно для SQL-слоя,
корректный результат (не просто «не упал»), плюс реальное время чтения
(median 5 прогонов) относительно локальной партиции того же прогона:

```
[tiering] SELECT по перемещённой (s3) партиции 2026-05-26: count()=300000, median времени за 5 прогонов=5.993352ms
[tiering] SELECT по локальной (default) партиции 2026-07-10: count()=300000, median времени за 5 прогонов=2.972585ms
[assert OK] count() по перемещённой на s3 партиции == вставленным старым строкам (факт 300000 == 300000)
[assert OK] count() по локальной партиции == вставленным свежим строкам (факт 300000 == 300000)
[assert OK] sum(revenue) по перемещённой на s3 партиции == независимо посчитанной сумме в Go, побайтово (факт 7511679381 == 7511679381 центов)
[assert OK] sum(revenue) по локальной партиции == независимо посчитанной сумме в Go, побайтово (факт 7530947786 == 7530947786 центов)
[assert OK] count() по ВСЕЙ таблице (одна партиция на s3, другая локально) == сумме вставленного (факт 600000 == 600000)
```

S3-чтение (5.99мс) медленнее локального (2.97мс) примерно вдвое на этом
объёме (300 000 строк, 6.37 MiB) — оба всё равно в диапазоне единиц
миллисекунд: MinIO в этом стенде живёт в ТОЙ ЖЕ compose-сети на том же
хосте, что ClickHouse (не реальный удалённый S3 с сетевой задержкой между
дата-центрами) — честная оговорка, разница «локально vs сетевой storage»
на проде с реальным облачным S3 будет заметно больше этой лабораторной
оценки.

### S3 table function: Parquet round-trip (Step 3 брифа)

`INSERT INTO FUNCTION s3(url, key, secret, 'Parquet') SELECT ... FROM
demo.s3_events` (обе партиции — s3-тиринг таблицы НЕважен для табличной
функции: она читает ЛОГИЧЕСКИЕ строки через обычный SELECT, а не байты
конкретного диска) → объект в MinIO → `SELECT ... FROM s3(url, key,
secret, 'Parquet')` обратно — round-trip, сверка через `toInt64(sum(
revenue)*100)` (тот же приём, что `../drivers/go` verify — побайтовая
сверка без плавающей точки):

```
[parquet] источник demo.s3_events: count()=600000, sum(revenue)*100=15042627167 центов
[parquet] записано в MinIO: http://minio:9000/chdata/s3-demo/events.parquet за 246.501464ms
[parquet] прочитано обратно из MinIO: count()=600000, sum(revenue)*100=15042627167 центов
[assert OK] Parquet round-trip через S3: count() совпал с источником (факт 600000 == 600000)
[assert OK] Parquet round-trip через S3: sum(revenue) совпал с источником побайтово (факт 15042627167 == 15042627167 центов)
```

**Ingestion S3 → MergeTree** (Step 3 брифа: «показать ingestion из S3 в
MergeTree») — свежая таблица `demo.s3_events_from_parquet` (обычный
default storage policy, эта таблица не про тиринг), `INSERT INTO ...
SELECT * FROM s3(...)`:

```
[parquet] ingestion S3 -> MergeTree (demo.s3_events_from_parquet): count()=600000 за 248.606403ms
[assert OK] ingestion S3(Parquet) -> MergeTree: count() совпал с источником (факт 600000 == 600000)
```

**Граница (Step 3 брифа):** `s3()`/`ENGINE=S3` — запрос к ОДНОМУ источнику
(файл/префикс в одном бакете), не федеративный движок. Для запроса ПО
МНОГИМ разнородным источникам сразу в одном SQL (S3 + Postgres + Kafka
одновременно) — Trino/lakehouse-инструменты, не ClickHouse; это отдельная
тема, не часть этой серии.

### Честный контраст: Kafka tiered storage vs CH S3-тиринг (Step 4 брифа)

| | Kafka tiered storage (`../kafka/`) | CH S3-тиринг (этот стенд) |
|---|---|---|
| Результат | **НЕ воспроизведено** — `remote.log.storage.system.enable=false`, `remote.log.storage.manager.class.name=null`, официальный образ `apache/kafka:4.3.1` не поставляется с реализацией плагина `RemoteStorageManager` | **Воспроизведено живьём** — part физически на MinIO (`system.parts.disk_name='s3'`), запрос корректен, round-trip Parquet == исходное число строк |
| Причина честного статуса | Pluggable-архитектура без встроенной реализации в официальном релизном образе | `storage_configuration`/s3-диск — встроенная функциональность сервера ClickHouse, работает "из коробки" против любого S3-совместимого API (включая MinIO) |
| Что подтверждено фактически | Статические конфиг-флаги + отсутствие jar-плагина в `/opt/kafka/libs` (косвенное доказательство "не может заработать") | Прямое доказательство: `disk_name='s3'` в `system.parts`, корректный `count()`/`sum(revenue)` после перемещения, Parquet round-trip |

### Ассерты (все прошли, fail-loud)

Step 1: `system.disks` содержит диск `s3`; `system.storage_policies`
policy `hot_cold` содержит ровно 2 тома (`hot`→`default`, `cold`→`s3`).
Step 2: до/после `MOVE PARTITION` — часть перемещённой партиции на
`disk_name='s3'`, часть нетронутой — на `disk_name='default'`; после
move — хотя бы одна часть каждой партиции в `system.parts`; `count()`/
`sum(revenue)` (побайтово через центы) по обеим партициям и по всей
таблице == независимо посчитанным в Go значениям. Step 3: Parquet
round-trip `count()`/`sum(revenue)` == источнику; ingestion S3→MergeTree
`count()` == источнику.

### Версии в прогоне

ClickHouse `26.6.1.1193` (то же, что вся серия), `minio/minio:
RELEASE.2025-09-07T16-13-09Z` (бандлит `mc`), clickhouse-go `v2.47.0`,
`github.com/shopspring/decimal` `v1.4.0` (`s3/go.mod`).

Content-note: throughput вставок/латентность запросов (в т.ч. s3 vs
local) — host-зависимы («характерный прогон», Docker Desktop/Windows,
MinIO в ТОЙ ЖЕ compose-сети — не аналог сетевой задержки до реального
облачного S3). Размеры на диске, `disk_name`, число строк/центов,
round-trip — детерминированы для этого прогона и воспроизводимы (повторный
прогон с нуля дал побайтово идентичные числа). Момент, когда именно
фоновый TTL-механизм перемещает партицию (сам, до explicit ALTER, или
после) — host/timing-зависим по МОМЕНТУ, но НЕ по конечному результату:
`ALTER TABLE ... MOVE PARTITION` гарантирует состояние `disk_name='s3'`
независимо от того, успел фон сам или нет (проверено живьём: оба прогона
этого стенда фон успел сам, ALTER пропущен как no-op — идемпотентность
проверена фактически, не предположена).

## Проверено живьём (decision, 2026-07-10)

Стенд #8 (финальный, `decision/`, Go, module
`tech.khorost/clickhouse-cookbook/decision`) — карта выбора: один и тот же
аналитический сценарий (тот же агрегат, что стенд #1 when-olap: `GROUP BY
country, toDate(event_time)`, `count()`+`sum(revenue)`) на **четырёх**
системах: **ClickHouse** (MergeTree), **TimescaleDB** (hypertable +
continuous aggregate + native compression), **DuckDB** (embedded, через
`go-duckdb`/CGO — БЕЗ отдельного сервера/контейнера, работает как
библиотека прямо внутри Go-бинаря `decision`), **PostgreSQL** (baseline,
без индексов). Запуск — по фазам (`-phase=schema|load|size|aggregate|
timescale`, эквивалент `-phase=all` в `ops/decision-demo.sh`), внутри
`golang:1.25` на сети `clickhouse-cookbook-net`, все программные ассерты
(fail-loud) прошли.

**Объём:** **10 000 000 строк**, ОДИНАКОВЫЙ во всех 4 системах — усечённое
подмножество общего датасета серии (`dataset/main.go -rows=20000000
-seed=42`, первые 10M строк, один и тот же CSV-файл для всех загрузчиков).
Изначально запускался тестовый прогон на 50 000 строк (проверка
корректности сценария), затем — этот боевой прогон на 10M синхронно, без
фоновых пауз, до полного завершения всех 5 фаз.

**Загрузка** (bulk, НЕ построчно: CH — `PrepareBatch`, батч 1 000 000
строк; PG и TimescaleDB — `pgx` `CopyFrom`/COPY-протокол; DuckDB — прямая
конвертация CSV→Parquet одним SQL, `COPY (SELECT ... FROM read_csv(...))
TO ... (FORMAT PARQUET)`, единственный «серверный» шаг для embedded-СУБД):

```
[load] CH:        10000000 rows in 20.014517098s (499637 rows/s)
[load] PG:        10000000 rows in 32.930117931s (303673 rows/s)
[load] Timescale: 10000000 rows in 41.170163358s (242894 rows/s)
[load] DuckDB:    10000000 rows converted CSV->Parquet in 4.337471067s (2305491 rows/s)
```

CH быстрее PG в 1.6x на той же bulk-загрузке (batch-INSERT vs COPY),
TimescaleDB чуть медленнее «сырого» PG (та же COPY, плюс маршрутизация
строк по 13 чанкам гипертаблицы). DuckDB здесь не «загружает» в
резидентное хранилище — перекодирует CSV в колоночный Parquet-файл,
поэтому число несравнимо напрямую с остальными тремя (иная природа шага).

**Размер на диске** (детерминировано, ДО TimescaleDB-компрессии):

```
[size] CH:        185.73 MiB on disk, 15 active parts, 10000000 rows (system.parts)
[size] PG:        761.36 MiB total (pg_total_relation_size), 10000000 rows
[size] Timescale: 998.84 MiB total (hypertable_size, ДО compress_chunk), 10000000 rows
[size] DuckDB:    189.91 MiB (единственный Parquet-файл на диске, никакого серверного хранилища)
```

CH компактнее PG в **4.10x**, компактнее некомпрессированной TimescaleDB
в **5.38x**, и почти вровень с Parquet DuckDB (**1.02x** — Parquet уже
колоночный и сжатый сам по себе, MergeTree с `LZ4` даёт сопоставимый
результат без ручного форматирования файла).

**Аналитический агрегат** (один и тот же запрос, medians по 5 прогонам,
«характерный прогон» — host-зависимо, Docker Desktop/Windows; revenue
сверяется как целые центы — тот же приём, что стенд #3 drivers):

```
[aggregate] CH:        median over 5 runs: 95.85225ms (durations: [73.194664ms 95.85225ms 81.66127ms 107.789659ms 110.99806ms])
[aggregate] PG:        median over 5 runs: 959.618887ms (durations: [1.165073385s 945.292458ms 959.618887ms 894.335244ms 2.212821424s])
[aggregate] Timescale (сырая гипертаблица): median over 5 runs: 2.68718594s (durations: [2.694857317s 2.717442577s 2.68718594s 2.662760554s 2.619705745s])
[aggregate] DuckDB (read_parquet, embedded): median over 5 runs: 279.901137ms (durations: [278.701744ms 288.987585ms 289.723599ms 279.901137ms 278.089205ms])

[aggregate] checksums: CH=0a158211(1800 groups) PG=0a158211(1800 groups) Timescale=0a158211(1800 groups) DuckDB=0a158211(1800 groups)
[aggregate] все 4 системы дали ОДИНАКОВЫЙ результат агрегата: 1800 групп (country, сутки), totalCents=13553742698

  CH               95.85225ms      (1.00x к CH)
  PG               959.618887ms    (10.01x к CH)
  Timescale(raw)   2.68718594s     (28.03x к CH)
  DuckDB           279.901137ms    (2.92x к CH)
```

Все 4 системы дали побайтово идентичный результат (CRC32 по канонической
сериализации групп + сумма центов, не Decimal/NUMERIC/DOUBLE напрямую).
CH — специализированный OLAP-движок, ожидаемо впереди (10x PG, 28x
«сырой» Timescale). DuckDB — заметно ближе к CH, чем к PG (2.92x к CH):
векторизованный движок читает уже готовый колоночный Parquet напрямую, без
серверного протокола/сети — цена «нулевой эксплуатации» здесь не в
скорости запроса, а в отсутствии резидентного хранилища.

**TimescaleDB обзорно** (Step 2 брифа — hypertable + continuous aggregate
+ native compression; глубокий time-series НЕ дублируется, отдельная
будущая статья):

*Continuous aggregate* (`CREATE MATERIALIZED VIEW decision_events_daily
WITH (timescaledb.continuous) AS ... GROUP BY country, time_bucket('1
day', event_time) WITH NO DATA`, наполнение — явный `CALL
refresh_continuous_aggregate(..., NULL, NULL)`; в проде — фоновая
`add_continuous_aggregate_policy`, здесь один ручной вызов, честно не
выдаётся за работающий scheduler):

```
[timescale] refresh_continuous_aggregate: 4.51685146s, decision_events_daily теперь 1800 строк (country x сутки)
[timescale] cagg query median over 5 runs: 3.544217ms
[timescale] cagg 3.544217ms vs сырой запрос 2.714814918s — cagg быстрее в 765.98x (предагрегат уже материализован)
```

Результат cagg-запроса (читает готовый предагрегат) побайтово совпал с
пересчётом по сырой гипертаблице (assert прошёл) — cagg быстрее в
**766x**, потому что агрегация уже материализована, а не потому что
запрос стал «умнее» (тот же принцип, что `-State`/`-Merge` в стенде #4).

*Native compression* (`ALTER TABLE ... SET (timescaledb.compress,
compress_orderby='event_time', compress_segmentby='country')` —
`compress_segmentby` по низкокардинальной колонке, участвующей в GROUP
BY, тот же принцип, что `LowCardinality` в CH; `compress_orderby` по
почти монотонной колонке времени, тот же эффект, что Delta-кодек CH
стенда #6):

```
[timescale] ДО compression: 13 chunks, 998.95 MiB (hypertable_size)
[timescale] compress_chunk: 13 chunks сжато за 14.826627402s
[timescale] ПОСЛЕ compression: 156.61 MiB (hypertable_size) — компрессия дала 6.38x
[timescale] агрегат ПОСЛЕ компрессии: median 2.575429431s (было 2.714814918s ДО компрессии), rows=10000000
```

Компрессия дала **6.38x** уменьшение размера (998.95 MiB → 156.61 MiB) —
после неё TimescaleDB (157 MB) компактнее baseline PG (761 MB) в 4.85x,
но всё ещё заметно больше CH (185.73 MiB). Результат агрегата ПОСЛЕ
компрессии побайтово не изменился (assert прошёл); скорость сырого
запроса по сжатым чанкам практически не изменилась (2.575s vs 2.715s —
разница в пределах шума, TimescaleDB compression прежде всего снижает
объём на диске и I/O, само по себе не эквивалент предагрегата).

**Ассерты** (все прошли, fail-loud): CH rows == PG rows == Timescale rows
== DuckDB/Parquet rows после загрузки (10 000 000 на каждой из четырёх);
CH/PG/Timescale rows == `-expect-rows=10000000`; checksum агрегата
стабилен между 5 прогонами КАЖДОЙ системы (детерминизм, не просто
скорость); результат агрегата CH == PG == Timescale(сырая) ==
DuckDB/Parquet побайтово (checksum/groups/totalCents); continuous
aggregate == пересчёт по сырой гипертаблице побайтово; после
compress_chunk — все 13 чанков сжаты (compressed == show_chunks ДО
компрессии), `hypertable_size` после < до, результат агрегата после
компрессии не изменился побайтово.

**Финальное состояние проверено напрямую в БД** (после завершения
Go-прогона, container уже снят `--rm`): CH `185.73 MiB, 15 parts, 10M
rows` (`system.parts`), PG `761 MB, 10M rows`
(`pg_total_relation_size`), TimescaleDB `157 MB, 10M rows` (пост-
компрессия, `hypertable_size`), `decision_events_daily` — 1800 строк,
DuckDB Parquet-файл `dataset/out/events-decision.parquet` — 199 137 990
байт (189.94 MiB, совпадает с числом из прогона в пределах округления).

### Карта выбора «класс задачи → система»

| Класс задачи | Система | Почему |
|---|---|---|
| Аналитика по десяткам–сотням млн строк, GROUP BY/агрегаты, append-only события | **ClickHouse** | Специализированный колоночный OLAP: 10x быстрее PG, 28x быстрее «сырой» TimescaleDB на этом агрегате; 4-5x компактнее на диске. Цена — отдельный кластер/сервис, эксплуатационная сложность (см. стенды #5/#6), мутации дороги (стенд #1). |
| Time-series поверх уже существующего PostgreSQL: метрики/IoT/события с TTL, нужны SQL-джойны с реляционными данными в той же БД | **TimescaleDB** | Расширение PG — тот же SQL/экосистема/операционная модель, `hypertable`+`time_bucket`+continuous aggregate закрывают типовой time-series сценарий БЕЗ отдельного кластера; native compression даёт 6.38x на этом датасете. Сырые запросы по некомпрессированным чанкам всё ещё заметно медленнее CH — не замена специализированному OLAP на больших объёмах. |
| Локальная/встраиваемая аналитика: отчёты по файлам (Parquet/CSV), ad-hoc исследование данных, CLI/ноутбук/десктоп-инструмент, нет и не нужен сервер | **DuckDB** | «SQLite для аналитики» — ноль серверной эксплуатации, embedded-библиотека внутри процесса. На этом прогоне быстрее PG (2.92x к CH vs PG 10x к CH — DuckDB заметно ближе к CH, чем к PG), но данные — файл на диске одного хоста, не сетевой сервис для множества клиентов. |
| OLTP + нечастая простая аналитика, точечные UPDATE/DELETE важны, объём умеренный, не хочется поднимать вторую систему | **PostgreSQL (baseline)** | «Достаточно хорошо и уже есть» — этот стенд специально прогнал PG БЕЗ индексов на этом агрегате (см. стенд #1: индексы на `event_time`/`country` агрегату не помогают, нужен полный скан в любом случае). Если аналитика становится основной нагрузкой — сигнал мигрировать в CH или добавить TimescaleDB. |

**Модель эксплуатации** (кластер vs расширение PG vs библиотека):
ClickHouse — отдельный сервис/кластер (см. стенд #5: шардинг+репликация+
Keeper), своя команда/мониторинг/бэкапы (стенд #6). TimescaleDB —
`CREATE EXTENSION` поверх уже эксплуатируемого PostgreSQL, тот же
бэкап/HA/мониторинг, что и остальной PG-парк, минимальный
дополнительный операционный вес. DuckDB — вообще не сервис: линкуется в
процесс (Go/Python/CLI/расширение для DBA-инструментов), нулевая
собственная эксплуатация, но и нулевая многопользовательская
сетевая доступность из коробки.

**Content-note: граница Trino/lakehouse.** Этот стенд сравнивает четыре
СУБД, каждая из которых сама владеет и исполняет запрос над СВОИМИ
данными. Федеративный запрос ПО МНОГИМ разнородным источникам ОДНИМ SQL
(например, JOIN между таблицей в CH, файлами в S3 и таблицей в PG в
одном запросе) — задача другого класса инструментов (Trino, lakehouse-
движки), не часть этой серии (та же оговорка, что стенд #7 про
`s3()`/`ENGINE=S3`).

### Версии в прогоне

ClickHouse `26.6.1.1193`, PostgreSQL `16.14` (`SHOW server_version`),
TimescaleDB `2.28.2-pg16` (extension `2.28.2`, `pg_extension.extversion`),
DuckDB (embedded engine) `v1.4.1` (`SELECT version()` внутри процесса,
через `github.com/marcboeker/go-duckdb/v2` `v2.4.3`), Go `1.25.0`,
`clickhouse-go/v2` `v2.47.0`, `github.com/jackc/pgx/v5` `v5.10.0`,
`github.com/shopspring/decimal` `v1.4.0` (`decision/go.mod`).

Content-note: throughput загрузки и латентность агрегатов — host-зависимы
(замерено внутри compose-сети на Docker Desktop/Windows, «характерный
прогон», абсолютные числа не переносимы на другое железо). Размеры на
диске, коэффициент сжатия TimescaleDB-компрессии, число групп/checksum —
детерминированы для этого датасета/версий и воспроизводимы. Объём (10M
строк вместо полных 20M общего датасета серии) — сознательное решение
ради синхронного прогона в рамках одной сессии (см. Global Constraints:
«умеренный объём, который проходит синхронно»), не ограничение метода —
на полном 20M-датасете направление результатов (относительные разы,
карта выбора) ожидаемо то же, абсолютные числа — иные.

## Content-notes для будущих стендов

- Числа throughput/latency в последующих стендах — host-зависимые (Docker
  Desktop/Windows искажает host-замеры, см. урок `java-deep-dive`) — замерять
  внутри compose-сети/контейнера, помечать «характерный прогон». Размеры на
  диске/коэффициенты сжатия/планы/число parts — детерминированы, годятся как
  воспроизводимые числа.
