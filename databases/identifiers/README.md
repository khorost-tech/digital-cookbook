# Идентификаторы: локальность, bloat, генерация

Стенд к серии «Идентификаторы» (khorost.tech). Показывает, как способ
генерации ID (случайный UUIDv4 vs монотонные bigint/UUIDv7) влияет на
локальность записи, раздувание индекса и объём WAL в PostgreSQL, на
распределение в Mongo и Scylla, и чем различается генерация ID в Go/Java/Rust.

## Углы

- `pg-locality` — PostgreSQL 18: bigint vs UUIDv4 vs UUIDv7 (артефакты 1, 2).
- `multiengine` — Mongo `_id`, Scylla токены, Mongo sharded (артефакты 3, 4, 4b).
- `gen` — генерация ID в Go/Java/Rust + типы колонок PG (артефакты 5, 6/7).

## Артефакты

| # | Артефакт | Что показывает | Скрипт |
|---|---|---|---|
| 1 | PG локальность/bloat/WAL | bigint vs UUIDv4 vs UUIDv7: leaf_density, WAL за вставку, размер PK-индекса | `scripts/pg-locality-demo.sh` |
| 2 | Cross-engine вторичный индекс | Ширина PK во вторичном индексе: PG (ctid, не влияет) vs MySQL/InnoDB (clustered PK, влияет) | `scripts/secondary-index-demo.sh` |
| 3 | Mongo `_id` | ObjectId (монотонный, 12 байт) vs UUID (случайный, 16 байт): размер `_id`-индекса | `scripts/mongo-id-demo.sh` |
| 4 | Scylla token-кольцо | Честный негатив: под Murmur3 монотонный ключ распределяется так же ровно, как случайный | `scripts/scylla-token-demo.sh` |
| 4b | Mongo ranged vs hashed shard key | Реальный обратный трейдоф: монотонный ranged-ключ → горячий шард, hashed → ровно | `scripts/mongo-ranged-shard-demo.sh` |
| 5 | Генерация ID Go/Java/Rust | uuidv4/uuidv7/ulid(/snowflake): ops/sec (within-язык), monotonic_within_ms, byte_sortable | `scripts/gen-bench.sh` |
| 6/7 | PG типы колонок + `::text`-каст | uuid vs bytea vs text: размер; `::text`-каст ломает покрытие индекса | `scripts/pg-storage-types-demo.sh` |

## Требования

**Артефакты 1–4b, 6/7 (движки):** Docker + Docker Compose. Всё остальное
поднимает `scripts/up.sh`; версии образов пинованы в `compose/compose.yml`.
Демо-скрипты Scylla (артефакт 4) и Mongo ranged (артефакт 4b) считают
token-span / перекос по шардам на стороне хоста и требуют **`python` или
`python3`** в `PATH` (скрипт берёт `python`, затем fallback на `python3` — в
WSL/многих дистрибутивах есть только последний).

**Хостовый лимит `fs.aio-max-nr` для ScyllaDB (артефакт 4).** Дефолт ядра
Docker Desktop/WSL2 (`65536`) не удовлетворяет минимум AIO даже для одного
узла Scylla с несколькими шардами — узел падает на старте: `Could not
initialize seastar: std::runtime_error (Your system does not satisfy minimum
AIO requirements. Set /proc/sys/fs/aio-max-nr to at least 67590...)`. Поднять
лимит ДО `scripts/up.sh` (значение общее для всех контейнеров хоста, sysctl не
namespaced):

    docker run --rm --privileged alpine sh -c "echo 1048576 > /proc/sys/fs/aio-max-nr"

На хостах с нативным Linux-ядром лимит обычно уже выше дефолта — шаг может не
понадобиться, но безвреден. То же ограничение подробно разобрано в стенде
[`databases/scylladb`](../scylladb/README.md) (там узлов несколько — упор жёстче).

**Артефакт 5 (генерация ID, `scripts/gen-bench.sh`):** нужны тулчейны трёх
языков — **Go**, **JDK 17 + Maven**, **Cargo (Rust)**. Скрипт берёт `go`,
`mvn` и `cargo` из `PATH`; отсутствующий тулчейн он не пропускает молча —
печатает `SKIP` с подсказкой и завершается ненулевым кодом.

Если тулчейн лежит нестандартно, переопределяется переменными окружения:

| Переменная | Назначение | По умолчанию |
|---|---|---|
| `GO` | путь к `go` | `go` из `PATH` |
| `MVN` | путь к `mvn` | `mvn` из `PATH` |
| `JAVA_HOME` | JDK для Maven | системный, который найдёт Maven |
| `M2_REPO` | локальный репозиторий Maven | не передаётся (штатный `~/.m2`) |
| `CARGO` | путь к `cargo` | `cargo` из `PATH` |
| `RUST_VIA_WSL=1` | гонять `cargo` под WSL | авто-fallback, если `cargo` нет на хосте |
| `GOPROXY` | прокси Go-модулей | `https://go.khorost.tech,direct` |

Типовой случай — нет прав на общий `~/.m2` (Maven падает на правах
репозитория): задайте свой каталог.

    # обычная машина: всё из PATH
    N=1000000 bash scripts/gen-bench.sh

    # свой Maven-репозиторий (частый случай)
    M2_REPO="$HOME/.m2-identifiers" N=1000000 bash scripts/gen-bench.sh

    # нестандартные пути к тулчейнам
    MVN=/opt/maven/bin/mvn JAVA_HOME=/usr/lib/jvm/java-17 CARGO=$HOME/.cargo/bin/cargo \
      N=1000000 bash scripts/gen-bench.sh

Языковые части можно запускать и по отдельности, без агрегатора:
`cd gen/go && go run . -n=1000000`, `cd gen/rust && cargo run --release -- 1000000`,
`cd gen/java && mvn -q compile exec:java -Dexec.mainClass=tech.khorost.GenBench -Dexec.args=1000000`.

## Как поднять

    # Docker Desktop/WSL2: поднять fs.aio-max-nr до старта Scylla (см. «Требования»)
    docker run --rm --privileged alpine sh -c "echo 1048576 > /proc/sys/fs/aio-max-nr"
    bash scripts/up.sh          # поднять PG18 + Mongo (+ sharded) + Scylla + MySQL, применить схему
    bash scripts/pg-locality-demo.sh
    ...
    bash scripts/down.sh        # снести (сотрёт данные И конфигурацию sharded-Mongo — переинициализация)

Все узлы на одном хосте: стенд показывает ХАРАКТЕР эффекта, не его величину
на реальном кластере. Живые числа, версии образов и библиотек, честные
находки и оговорки — единственный источник фактов для статей серии:
[`FIXTURES.md`](FIXTURES.md).
