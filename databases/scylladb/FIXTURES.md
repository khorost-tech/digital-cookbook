# FIXTURES — реальные числа стендов ScyllaDB cookbook (источник для Phase 2)

Сводка живых чисел, снятых на реальном кластере (`scylladb/scylla:2026.2.0`,
docker compose, Docker Desktop/Windows) в ходе Task 1–9 этого cookbook.
Каждая строка — **реально измеренное** значение, ссылка на раздел README, где
оно снято и объяснено подробнее. Ничего здесь не придумано и не пересчитано —
только перенесено из уже закоммиченного `scylladb/README.md` (и, где отмечено,
`scratchout/*.txt`, не коммитится, воспроизводимо по инструкциям README).

Формат: `утверждение → число (единица) → источник`.

Датасет и версии — сквозные для всех стендов, вынесены в отдельный раздел
внизу вместе с честными оговорками, общими для нескольких стендов.

---

## Стенд #1 — модель данных: good vs bad vs hot partition

- Общий датасет `telemetry.readings` → **672 000 строк** (500 устройств × 14
  суток × 96 замеров/сутки) → `dataset/main.go -devices 500 -days 14 -per-day 96`,
  README «Датасет».
- Загрузка `readings_bad`/`readings_hot` → по **672 000 строк** каждая, ~23–24с
  на загрузку → README «Загрузка bad/hot», `SELECT count(*)` подтвердил.
- Размер партиции (`nodetool tablestats`, после `flush`):
  - `readings` (good, `(device_id, day)`) → 7000 партиций, **max 4768 байт**,
    mean 4768 → README «Реальные размеры партиций».
  - `readings_bad` (bad, `device_id`) → 500 партиций, **max 73 457 байт**,
    mean 63 467 → там же.
  - `readings_hot` (hot, `region`) → **4 партиции** (== число регионов),
    **max 8 409 007 байт**, mean 8 409 007 → там же.
  - Вывод: bad_max/good_max = **15.4×**; hot_max/bad_max = **114.5×**;
    ширина строки во всех трёх ~50 байт/строку (структура одинакова, разница
    — только число строк на партицию).
- `-scenario partition-size` (Go, живой прогон): good max 96 строк/партиция,
  bad max 1344 строк/партиция, hot max 168 000 строк/партиция → README, ассерты
  `bad_max > good_max`, `hot_partitions == 4`, `hot_max > bad_max` — все `OK`.
- `-scenario hot-partition` — скан партиции: good 96 строк за **4.85ms**, hot
  168 000 строк за **218.6ms** → hot дороже good в **45×** → README.
- `-scenario query-shape` — типовой запрос «1 устройство/1 сутки»: good 3.6ms
  и bad 5.1ms без `ALLOW FILTERING`; hot без `ALLOW FILTERING` — CQL-ошибка
  (`Clustering column "device_id" cannot be restricted`); с `ALLOW FILTERING` —
  **28.7ms** → README.
- `nodetool toppartitions` — честное ограничение: вернул `Nothing recorded`
  (сэмплер меряет только окно замера, кластер был idle в момент вызова) →
  README «Реальные размеры партиций», конец раздела.

## Стенд #2 — архитектура shard-per-core

- `-scenario shard-distribution` (20 000 точечных чтений, before/after дельта
  `scylla_database_total_reads` по шардам на 3 узлах): оба шарда каждого узла
  выросли примерно поровну (**scylla1: Δ7128/Δ6855, scylla2: Δ6834/Δ6042,
  scylla3: Δ6831/Δ7467**) → распределение по шардам почти 50/50, не легло на
  один шард → README.
- Сумма дельт по узлу (~12 900–14 600) больше `n/3` (~6667) — ожидаемо: RF=3 +
  `Consistency: Quorum` → каждый запрос физически исполняется на ≥2 узлах.
- `-scenario latency` (50 000 последовательных точечных чтений, один коннекшн,
  клиентские тайминги): **p50 = 1.5468ms, p99 = 1.7115ms, max = 4.283ms**,
  `p99/p50 ≈ 1.11` → README. Воспроизведено трижды, `p99/p50` стабильно в
  диапазоне **1.09–1.13**, ни одного выброса до сотен мс (в контраст с
  JVM-style GC-паузами).
- Серверная сторона (`nodetool proxyhistograms` тот же момент): **p50 =
  179µs, p99 = 253.75µs**, `p99/p50 ≈ 1.42` — тот же порядок, что клиентский
  (1.11); разница ~1.3–1.4ms — сетевой/Docker Desktop оверхед моста, не
  серверная задержка.
- Ассерт `p99 <= K*p50` c **K=5** (осознанный запас, не подгонка под факт
  1.13) — держится с большим запасом на всех прогонах.
- Метод шардинга — `scylla_database_total_reads` (monotonic counter), НЕ
  `scylla_reactor_utilization` (gauge, шумит между снапшотами) → README
  «Честная оговорка».

## Стенд #3 — compaction, tombstones, repair

- `gc_grace_seconds` (живьём из `system_schema.tables`) → **864000** (10.0
  суток) → README.
- **TWCS группирует sstable по WRITE-timestamp мутации, НЕ по значению
  столбца данных** (живая находка). До фикса (`INSERT` без `USING TIMESTAMP`)
  все 14 симулированных суток телеметрии легли в **одно** окно TWCS (реальное
  время загрузки, разница `max_timestamp - min_timestamp` ≈ **39.5 секунд**,
  не 14 суток `event_time`) → README. После фикса (`INSERT ... USING
  TIMESTAMP` с `event_time.UnixMicro()`): **14/14 окон** дали новый sstable,
  каждое окно — **ровно 32 новых sstable** (avg/окно=32.00 без отклонений),
  финальный `live_ss_table_count = 448` → README.
- Байтовое подтверждение: `scylla sstable dump-statistics` по первому/
  последнему sstable — timestamps попадают ровно в границы суток 0 и суток
  13 соответственно, ни один sstable не содержит данные двух суток → README.
- `-scenario tombstones`: массовый `DELETE` по 500 партициям (12ч диапазон
  суток), строк на выборке **до = 96**, **после = 48** на всех 20 проверенных
  партициях → README.
- Traced read (`gocql.Tracer` → `system_traces.events`) через удалённый
  диапазон: `tombstones_scanned` (сумма dead cells по ВСЕМ репликам QUORUM) =
  **96** = 48 dead × 2 опрошенные реплики (RF=3, QUORUM=2) — **не** 96
  различных tombstone-ячеек, реальное число — **48** (проверено по полю
  `TraceEntry.Source`, два разных источника: `172.22.0.2` и `172.22.0.3`) →
  README «Исправлено по ревью».
- Честная находка: `nodetool tablestats ... tombstone` и REST
  `tombstone_scanned_histogram` остались на **0** во всех проверках несмотря
  на подтверждённые 48 dead-строк на чтение — рабочий источник, задействован
  стендом, `Page stats` из `system_traces.events` (двумя путями: host-side
  `cqlsh TRACING ON` и programmatic `gocql.NewTracer`).
- `ops/repair-demo.sh` — живая находка: буквальная команда брифа `nodetool
  repair -pr telemetry` **не работает** на tablet-keyspace (ошибка `nodetool
  repair repairs only vnode keyspaces!`) → рабочая замена `nodetool cluster
  repair --keyspace telemetry`. Прогон A поймал реальное расхождение
  (партиция `dev-repair-demo-0001-00499`: **20 vs 0** до repair → **20/20/20**
  после) → README. Кластер вернулся к `3×UN` в обоих прогонах.

## Стенд #4 — consistency levels + LWT

- `-scenario cl-latency` (20 000 записей + 20 000 чтений на CL, idle-кластер),
  combined p50/p99 (микросекунды, полный вывод `scratchout/cl.txt`):

  | CL | write p50 | write p99 | read p50 | read p99 | combined p50 | combined p99 |
  |---|---:|---:|---:|---:|---:|---:|
  | ONE | 1418 | 1508 | 1345 | 1506 | 1390 | 1507 |
  | LOCAL_QUORUM | 1490 | 1718 | 1497 | 1726 | 1493 | 1722 |
  | QUORUM | 1503 | 2011 | 1510 | 2084 | 1507 | 2047 |
  | ALL | 1497 | 1641 | 1502 | 1641 | 1500 | 1641 |

  Честно: разница между CL на idle однохостовом кластере — **единицы
  процентов** (`combined_p50` ONE→ALL: 1390→1500µs, ~8%), не порядки — все 3
  реплики физически на одном Docker-хосте. `LOCAL_QUORUM ≈ QUORUM` (1493 vs
  1507µs) — архитектурно один и тот же порог кворума в одном ДЦ, разница 14µs
  — шум измерения. Ассерт `lat[ALL] >= lat[ONE]` — `OK`.
- `-scenario lwt` (plain vs LWT, `n=5000`): **plain p50 = 1.496ms**, **lwt
  p50 = 4.924ms**, `applied=5000/5000`, **ratio lwt/plain = 3.29×** (Paxos
  prepare-propose-commit против одного round-trip) → README.
- Contention (16 горутин × 312 попыток = 4992 CAS-попыток на один ключ):
  **applied=312, failed=4680**, `contended_failed_fraction = 0.9375`
  (93.75%), CAS latency под конкуренцией **p50=78.85ms, p99=91.24ms** →
  README. Формула `≈(contenders-1)/contenders` подтверждена другими
  значениями `-contenders`: 8 → **0.8750** (175/200), 4 → **0.7500**
  (75/100) — доля меняется по формуле, не константа 93.75%.
- Ассерты `lwt_latency > plain_latency` и `contended_failed_fraction > 0` —
  оба `OK`, воспроизведено дважды (ratio 3.27×/3.29×, failed fraction
  93.75%/93.75%).
- Серверные CAS-метрики — живая находка: `scylla_storage_proxy_coordinator_
  cas_prune` (HELP: "how many times paxos prune was done after successful cas
  operation") на самом деле инкрементируется на КАЖДЫЙ завершённый Paxos-раунд
  (не только успешный `applied=true`) — подтверждено точным совпадением
  `cas_prune` delta = **9993** = 5000 (LWT insert) + 1 (init) + 4992
  (contention, applied+failed) → README. Корроборирует
  `cas_total_operations` (тот же delta, HELP-текст точен).

## Стенд #5 — tablets + multi-DC

- `nodetool status` на `compose/multidc.yml`: **4×UN** в 2 ДЦ (DC1: dc1a,
  dc1b; DC2: dc2a, dc2b) → README.
- `-scenario multidc` (`n=10000`, keyspace `telemetry_mdc`
  `{DC1:2, DC2:2}`): Фаза A — 10000 записей `LOCAL_QUORUM` (координатор DC1),
  Фаза B — чтение из DC2 `LOCAL_QUORUM`: **совпало=10000, расхождение=0** →
  репликация DC1→DC2 подтверждена → README.
- `LOCAL_QUORUM write p50 = 1.486ms`, `QUORUM write p50 = 1.564ms` — QUORUM
  дороже на **~78µs (~5%)** по p50, **~144µs (~9%)** по p99 → README. Честная
  оговорка: оба ДЦ на одном Docker-хосте — не настоящий межрегиональный RTT,
  только направление эффекта (QUORUM ≥ LOCAL_QUORUM) архитектурно верно.
- DC failover (`dc2a`+`dc2b` остановлены): `LOCAL_QUORUM` в DC1 продолжает
  работать без ошибок (`write rc=0, read rc=0`); `EACH_QUORUM` отказывает с
  буквальной серверной ошибкой `Unavailable exception ... required_replicas:
  2, alive_replicas: 0` → README.
- tablets — живая находка: `nodetool decommission dc2b` **отказывает** при
  `DC2:2` (`Unable to find new replica for tablet ...`) — ScyllaDB защищает от
  decommission, ломающего заявленную репликацию → README. После `ALTER
  KEYSPACE ... DC2:1` — decommission проходит (`rc=0`).
- Миграция tablets при decommission (`telemetry_mdc_tablets`, `{DC1:1,
  DC2:1}`, table_id, tablet_count=32): **до** — dc1a/dc1b/dc2a/dc2b по 16
  реплик каждый (64 всего); **после** decommission `dc2b` — dc2a вырос
  **16→32**, dc1a/dc1b без изменений, сумма реплик неизменна **(64)** → чистая
  миграция без потерь/дублей → README.
- Живая находка: `nodetool tablets`/`nodetool cluster` (буквальные команды
  брифа) НЕ существуют в сборке 2026.2.0 — рабочий источник `system.tablets`
  напрямую → README.

## Стенд #6 — shard-aware драйверы (Go + Java)

- Go bench (`drivers/go`, `-mode aware|naive`, `n=100000`, `CL=QUORUM`):

  | mode | throughput | p50 | p99 |
  |---|---:|---:|---:|
  | aware | **653.3 rows/s** | 1.520ms | **1.708ms** |
  | naive | 639.7 rows/s | 1.569ms | 1.743ms |

  `ratio aware/naive = 1.021` (aware быстрее на **~2.1%** throughput, p99
  ниже на **~2.0%**). Повторный прогон (`n=30000`, другой seed, обратный
  порядок): aware 655.3 rows/s / p99=1.636ms vs naive 637.3 rows/s /
  p99=1.748ms (ratio **1.028**) — направление устойчиво → README.
- Java bench (`drivers/java`, `DriversBench`, `-mode on|off`, изолированные
  процессы, `n=20000`, `warmup=3000`, `CL=QUORUM` после правки Finding 2):

  | mode | throughput | p50 | p99 |
  |---|---:|---:|---:|
  | ON (shard-aware) | 2122.0 rows/s | 451µs | 721µs |
  | OFF (наивно) | **2141.7 rows/s** | 447µs | **694µs** |

  `ratio ON/OFF = 0.991` (**OFF быстрее ON на ~0.9%**). Повторный прогон
  (seed 99): OFF 2152.8 rows/s vs ON 2130.3 rows/s (ratio **0.990**,
  ~1.1%) — направление устойчиво, но ПРОТИВОПОЛОЖНОЕ Go → README.
- Живая находка #1: JVM single-процесс (ON/OFF подряд в одном процессе,
  `-mode both`) дал swing **~26–30%** от порядка запуска (JIT-прогрев) — на
  порядок больше самого shard-awareness эффекта (~1%). Исправлено — два
  независимых процесса (как Go).
- Живая находка #2: до правки CL Java-бенч наследовал дефолт драйвера
  `LOCAL_ONE` (не `QUORUM`, как Go) и показывал ON быстрее OFF на 0.7–1.2%;
  после выравнивания CL на `QUORUM` направление развернулось (OFF быстрее) —
  честный результат честного фикса методики, не регрессия.
- Живая находка #3: два драйвера (upstream `gocql/gocql` + fork
  `scylladb/gocql`; или DataStax OSS + ScyllaDB Java driver) физически НЕ
  могут сосуществовать в одном модуле/classpath — оба самодекларируют один и
  тот же module path/пакет (`github.com/gocql/gocql`;
  `com.datastax.oss.driver.*`), подтверждено `unzip -l
  java-driver-core-4.19.2.0.jar` — только `com/datastax/...`, ни одного
  `com/scylladb/...` → README.
- Итог: эффект shard-aware routing на `--smp 2` **модестный (0.9–3%)** и после
  выравнивания CL у Java направление отличается от Go — оба реальных
  измерения, не подгонка.

## Стенд #7 — эксплуатация (monitoring/backup/Alternator)

- Monitoring: число `scylla_*`-метрик на узел (curlimages/curl, внутри сети) —
  **scylla1: 4360, scylla2: 4352, scylla3: 4100** → README.
- Ключевые метрики scylla1 на момент снятия: `scylla_database_total_reads`
  (сумма) = **880862**, `scylla_database_total_writes` (сумма) = **297740**,
  `scylla_compaction_manager_backlog` = **0.0** (idle-кластер, легитимно
  нулевой — Стенд #3 уже показал ненулевой backlog под активной записью),
  `scylla_storage_proxy_coordinator_cas_total_operations` (накоплено Стендом
  #4) = **3820** → README.
- Backup/restore (`ops_backup_demo`, 50 строк): snapshot **245 файлов, 1.1M**;
  после `TRUNCATE` — count=0; после `nodetool refresh` из `upload/` — count=50
  (== original) → README. `telemetry.readings` за весь цикл — неизменны
  (**672000**).
- Живая находка: `DROP TABLE` не освобождает директорию данных немедленно —
  несколько `ops_backup_demo-<cf_id>` каталогов на диске одновременно;
  скрипт определяет актуальную директорию через `system_schema.tables.id`, а
  не `ls | grep` → README.
- Alternator (`ops-stand`, `-scenario alternator`, отдельный транзитный
  однонодовый контейнер `scylla-alt`, `--alternator-port 8000`): `PutItem` +
  `GetItem` → **match=true** → README.
- Живая находка: DynamoDB-тип `N` не сохраняет литеральную строку — `"42.50"`
  записано, `"42.5"` прочитано (decimal-нормализация, задокументированное
  поведение протокола, не баг) → ассерт стенда сравнивает числовое значение,
  не строку.

---

## Сквозные факты (для всех статей серии)

### Версии (сверено живьём 2026-07-10/11)

- Образ ScyllaDB: **`scylladb/scylla:2026.2.0`** (пин явный, не `latest`) —
  `docker exec scylla1 scylla --version` → `2026.2.0-0.20260618.ccb141ab3d0c`.
- CQL/`release_version`: **`3.0.8`** (протокольная Cassandra-совместимая
  версия, не версия сервера) — `SELECT release_version FROM system.local`.
- gocql (шард-осведомлённый форк ScyllaDB): **`v1.18.3`** через
  `require github.com/gocql/gocql v1.18.3` + `replace github.com/gocql/gocql
  => github.com/scylladb/gocql v1.18.3` (см. «Почему replace» в README —
  форк самодекларирует module path апстрима после слияния репозиториев).
- Go: **`1.26`** (`go.mod` всех 8 модулей), сборка через образ `golang:1.26`.
- JDK (Java): **`25`** (`maven.compiler.release=25` в `java/pom.xml`), сборка
  через образ `maven:3.9-eclipse-temurin-25`.
- java-driver-core (шард-осведомлённый форк ScyllaDB): **`4.19.2.0`**
  (`groupId=com.scylladb`, `drivers/java/pom.xml` наследует из
  `java/pom.xml` dependencyManagement) — сверено живьём по
  `maven-metadata.xml` Maven Central.
- Prometheus (monitoring, Стенд #7): образ **`prom/prometheus:v3.7.3`**.
- Датасет: генератор `dataset/main.go`, **`-seed 42`** (детерминированный,
  привязан к опорной точке `2026-07-01T00:00:00Z`, не к `time.Now()`),
  **672 000 строк** по умолчанию (500 устройств × 14 суток × 96 замеров/сутки).

### Честные оговорки, общие для нескольких стендов

- **Малые дельты между consistency levels на idle однохостовом кластере —
  единицы процентов, не порядки.** Все узлы (одного ДЦ и обоих multi-DC)
  физически на одном Docker-хосте — RTT между «локальными» и «удалёнными»
  (multi-DC) репликами исчезающе мал по сравнению с реальным multi-host/
  multi-region развёртыванием. Направление эффекта (`ALL ≥ ONE`,
  `QUORUM ≥ LOCAL_QUORUM`) архитектурно верно и воспроизведено во ВСЕХ
  прогонах (Стенды #4, #5), но абсолютная величина (~5–9%) — артефакт
  локальной топологии стенда, не оценка реальной гео-задержки.
- **Апстрим/форк-коллизия module path/classpath (Стенд #6).** И gocql
  (Go), и java-driver-core (Java) — форки ScyllaDB опубликованы под другими
  координатами верхнего уровня, но с ИДЕНТИЧНЫМ внутренним API/module path/
  пакетами апстрима. Это делает миграцию тривиальной (замена координаты
  зависимости), но не позволяет держать upstream и fork одновременно в одном
  go.mod/classpath — архитектурное следствие способа поддержки форков
  ScyllaDB, не баг конкретного стенда.
- **TWCS группирует sstable по WRITE-timestamp мутации, не по значению
  столбца данных (Стенд #3).** Историческая загрузка исторических данных БЕЗ
  явного `USING TIMESTAMP` кладёт все sstable в одно окно текущего момента
  записи, а не в окна, соответствующие `event_time` данных — классическая
  ловушка backfill'а time-series в ScyllaDB/Cassandra.
- **Repair и tablets keyspace (Стенды #3, #5).** Классический
  `nodetool repair -pr <keyspace>` не работает на tablet-keyspace (дефолт
  новых keyspace в 2026.2.0) — нужен `nodetool cluster repair --keyspace
  <keyspace>`. Аналогично `nodetool tablets`/`nodetool cluster tablets` не
  существуют в этой сборке — распределение tablets читается напрямую из
  `system.tablets`.
- **`nodetool decommission` защищён репликацией (Стенд #5).** Decommission
  узла ДЦ, где RF ДЦ равен текущему числу узлов ДЦ, отказывает («Unable to
  find new replica for tablet») — нужно сперва понизить RF затронутого ДЦ
  (`ALTER KEYSPACE`). Реальное защитное поведение ScyllaDB, не ограничение
  стенда.
- **Alternator Number-нормализация (Стенд #7).** DynamoDB-тип `N` хранится
  как decimal-значение, не байт-в-байт строка литерала — `"42.50"` читается
  обратно как `"42.5"`. Задокументированное поведение протокола DynamoDB
  (унаследовано Alternator), не баг совместимости.
- **`:9180/metrics` и REST `:10000` слушают на сетевом адресе контейнера, не
  на `127.0.0.1` (Стенды #2, #3, #7).** `docker exec ... curl
  localhost:9180/metrics` → connection refused; тот же запрос на IP
  контейнера/DNS-имя узла (`scylla1:9180`) с ЛЮБОГО контейнера
  compose-сети → 200 OK. Единый паттерн доступа к метрикам/REST для всех
  стендов, читающих их из Go/Java без docker-сокета хоста.
- **Docker Desktop/Windows: `fs.aio-max-nr` и шард-осведомлённый клиент через
  NAT.** Дефолтный `fs.aio-max-nr=65536` хватает только на один узел ScyllaDB
  на хосте — фикс `echo 1048576 > /proc/sys/fs/aio-max-nr` ДО старта
  кластера. Шард-осведомлённая маршрутизация gocql ненадёжна через
  опубликованный порт Docker (NAT) — загрузчики/бенчи запускаются
  контейнером на сети cookbook (`scylla1:9042` и т.п.), не с хоста через
  `localhost:9042`.

### Лицензионная оговорка (ScyllaDB)

С 2024 года ScyllaDB перешла на годовые unified/source-available релизы
(модель `ScyllaDB Source Available License`, не классическая OSS-лицензия
предыдущих мажорных версий) — этот стенд пинуется к конкретному образу
**`scylladb/scylla:2026.2.0`**, актуальному на момент написания серии.
Условия лицензирования и версийная нумерация (`YYYY.N`) могут отличаться в
будущих релизах — при воспроизведении стенда на другой версии образа
поведение (особенно tablets-специфика, Alternator, метрики) сверять по
официальным release notes соответствующего года, не считать это README
универсально применимым ко всем версиям ScyllaDB.
