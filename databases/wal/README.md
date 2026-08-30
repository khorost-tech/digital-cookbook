# wal — WAL и его аналоги (стенды к серии статей)

Живые Docker-стенды к серии «WAL и его аналоги» (3 статьи: что такое WAL и
зачем нужна надёжность → WAL-аналоги в других БД → репликация/CDC/PITR).
Все числа и логи в статьях — из реальных прогонов этих стендов, не выдуманы.

## Обзор стендов

| Каталог | Что демонстрирует | Как запустить |
|---|---|---|
| `postgres/` | Флагманский стенд: рост WAL при insert/update, checkpoint, `synchronous_commit` (pgbench), crash recovery (SIGKILL) | `cd postgres && mkdir -p archive && docker compose up -d` |
| `mysql/` | Redo log (InnoDB, crash recovery) и binlog (репликация/CDC) — два независимых журнала на разных уровнях | `cd mysql && docker compose up -d && ./show-logs.sh` |
| `mongo/` | Oplog как capped-коллекция — логический журнал для репликации | `cd mongo && docker compose up -d && ./run.sh` |
| `sqlite/` | `journal_mode=WAL` — WAL-файл живёт только пока есть открытое соединение | `cd sqlite && ./wal-mode.sh` |
| `redis/` | AOF (`appendonly yes`, `everysec`) — multi-part `appendonlydir` (base RDB + incr AOF + manifest) | `cd redis && docker compose up -d && ./aof.sh` |
| `logical/` | Логический слот PostgreSQL (`pgoutput`) + самодельный Go-потребитель (pglogrepl) — CDC без брокера | `cd logical/go && go run .` (см. `logical/README.md` про обход host-firewall) |
| `debezium/` | Debezium Embedded Engine (без Kafka) — индустриальный аналог `logical/`, JSON change-события | см. `debezium/README.md` |
| `replication/` | Физическая streaming-репликация primary→standby (`pg_basebackup -R -X stream`) | `cd replication && docker compose up -d && ./setup.sh` |
| `pitr/` | Point-in-time recovery: базовый бэкап + WAL-архив → восстановление на точку между коммитами | `cd pitr && ./pitr-test.sh` |

Порядок по статьям: **#1** (`wal-what-and-reliability`) — `postgres/`. **#2**
(`wal-across-databases`) — `mysql/`, `mongo/`, `sqlite/`, `redis/`. **#3**
(`wal-replication-and-cdc`) — `logical/`, `debezium/`, `replication/`, `pitr/`.

## Версии (фактические, проверено на текущем тулчейне)

| Компонент | Образ/пакет в репо | Фактически резолвится в |
|---|---|---|
| PostgreSQL | `postgres:18.4` | 18.4 (все PG-стенды: postgres/, logical/, debezium/, replication/, pitr/) |
| MySQL | `mysql:8.4` | mysqld **8.4.10** (Community Server GPL, LTS) |
| MongoDB | `mongo:8.2` | **8.2.11** |
| Redis | `redis:8` | **8.8.0** |
| SQLite | `keinos/sqlite3:latest` | sqlite3 **3.53.2** (2026-06-03) |
| Go | `go.mod` / `golang:1-alpine` | **1.26.3** |
| `github.com/jackc/pgx/v5` | go.mod | **v5.10.0** |
| `github.com/jackc/pglogrepl` | go.mod | **v0.0.0-20260401131349-e37c41485510** (псевдо-версия, без семверного тега — таков был `@latest` на момент разработки) |
| Debezium | `pom.xml` (`debezium.version`) | **3.6.0.Final** (latest стабильный 3.x на Maven Central на момент проверки, 2026-07) |
| JDK | `maven:3.9-eclipse-temurin-21` | **21** |
| Maven / exec-maven-plugin | образ / pom.xml | **3.9** / **3.5.0** |

`mysql:8.4`, `mongo:8.2`, `redis:8`, `keinos/sqlite3:latest` — плавающие теги
(резолвятся в конкретный патч на момент `docker pull`); при повторном прогоне
в будущем возможна другая патч-версия того же мажора. Поведение WAL/AOF/oplog
стабильно в пределах мажорной линии, но абсолютные числа (размеры файлов,
имена файлов) могут отличаться на патч-другой.

## Host-зависимость чисел

Абсолютные величины (`tps`, `latency average` из pgbench, размеры WAL/AOF/redo
файлов, тайминги прогрева логического декодирования) **зависят от машины**, на
которой прогонялся стенд (в частности — Docker Desktop на Windows/WSL2, диск,
загрузка хоста). Для статей это не проблема: важен **порядок величины и
относительный разрыв** (например `synchronous_commit=off` быстрее `on` в
разы, а не абсолютные `tps`), а не то, что читатель воспроизведёт цифру
день-в-день. В таблицах и логах в статьях такие цифры помечены как
«характерный прогон».

## Стенды учебные, не production-паттерны

Ряд решений в стендах сознательно упрощён ради демонстрации механики WAL и
**не должен копироваться в прод как есть**:

- **`replication/`**: `pg_hba.conf` разрешает `host replication all all
  trust` — без пароля/сертификата. Допустимо только потому, что сеть —
  приватный docker-compose bridge конкретного стенда. В проде для replication
  нужны `scram-sha-256` + пароль или сертификат.
- **`pitr/` и `postgres/`**: `archive_command = test ! -f ... && cp %p ...`
  — учебный однопоточный архиватор. Он последователен и залипает (бесконечный
  retry) на первом же сегменте, который уже существует в архиве (например, от
  прошлого прогона демо) — это реально воспроизводится на стенде `pitr/`, где старые
  сегменты `archive/` приходится чистить перед прогоном. В проде для
  WAL-архивации нужны инструменты вроде `pgBackRest`/`wal-g`, которые сами
  трекают уже заархивированные сегменты.
- **`replication/`, `pitr/`**: репликация асинхронная (`sync_state=async`,
  дефолт), без `synchronous_standby_names` — другой durability-контракт,
  чем synchronous replication (async может потерять последние транзакции при
  падении primary до их доставки на реплику).
- **`logical/`**: обход host-firewall через запуск Go-процесса в контейнере
  на сети стенда (симптом наблюдался на машине автора: хостовый firewall
  блокирует TCP от Go-бинарников к опубликованному Docker-порту, хотя `psql`
  и обычный TCP-тест проходят) — специфика конкретного окружения, не общий
  паттерн; на других ОС может не воспроизводиться.
- Пароли (`waldemo` и т.п.) — учебные, только для локальных демо-стендов, не
  для переиспользования где-либо ещё.

## Общее

- Все стенды поднимаются и опускаются независимо (`docker compose up -d` /
  `docker compose down` в своём каталоге); данные не персистентны между
  прогонами (volume не bind-монтируется, кроме `postgres/archive/` — WAL-архив
  для `pitr/`).
- Пути `-v`/`-w` в `docker run` на Windows/Git Bash (MSYS) переписываются —
  везде, где это встречается, используется `MSYS_NO_PATHCONV=1` инлайн перед
  конкретной командой (см. комментарии в скриптах и `pitr/README.md`,
  `logical/README.md`). На Linux/macOS хосте этот костыль не нужен.
- Детальные разборы (сырые логи, нюансы, пошаговое ручное воспроизведение) —
  в `README.md` каждого подкаталога, где он есть (`logical/`, `debezium/`,
  `pitr/`).
