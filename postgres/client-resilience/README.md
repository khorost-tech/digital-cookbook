# postgres/client-resilience

Живой стенд к статье [Клиенты Go, Java и Rust к PostgreSQL: надёжность подключений](https://khorost.tech/databases/postgres-clients-reliability-go-java-rust/).
Показывает то, что нельзя увидеть на сниппетах: как клиенты Go (`pgx`), Java (`JDBC` + `HikariCP`)
и Rust (`sqlx`) ведут себя при работе через pgbouncer, чтении с реплики, обрыве соединения
с primary и failover — на топологии PostgreSQL primary + streaming-реплика + pgbouncer.

## Что внутри

```
client-resilience/
  topology/
    docker-compose.yml        # pg-primary + pg-replica + pgbouncer + client-{go,java,rust}
    init-primary.sh            # роль replicator, таблица users, pg_hba для подсети стенда
    entrypoint-replica.sh       # pg_basebackup -R с primary при пустом PGDATA
    pgbouncer/
      pgbouncer.ini            # pool_mode = transaction
      userlist.txt
  clients/
    go/     pgx v5:              writer-пул через pgbouncer + reader-пул с реплики
    java/   JDBC + HikariCP:     то же самое, простой protocol на записи
    rust/   sqlx:                то же самое + оговорка про отсутствие multi-host failover
```

Каждый клиент — простой цикл раз в секунду: `INSERT INTO users` через пул к pgbouncer
(запись), затем `SELECT ... ORDER BY id DESC LIMIT 3` напрямую с реплики (чтение). При старте
клиент один раз подключается по `MULTIHOST_DSN` и логирует, какой хост был выбран как
read-write (primary) — это и есть failover-aware selection у Go/Java. Клиент **не падает**
на ошибке: он логирует её и продолжает — поэтому при рестарте/failover видно и окно
недоступности, и восстановление.

## Требования

- Docker + Docker Compose v2.
- Свободная подсеть `172.30.0.0/16`.

**Зафиксированные версии** (сверены на актуальность 2026-07-01):
PostgreSQL `18` (образ `postgres:18`), pgbouncer `1.25.2` (образ `edoburu/pgbouncer:v1.25.2-p0`),
pgx `v5.10.0`, HikariCP `6.3.0`, PostgreSQL JDBC (`pgjdbc`) `42.7.7`, sqlx `0.9`, tokio `1`.

> Начиная с `postgres:18` официальный образ ожидает `VOLUME` на `/var/lib/postgresql`
> (не `.../data`) — в стенде `PGDATA` явно задан в подкаталог (`/var/lib/postgresql/pgdata`),
> чтобы путь не зависел от мажорной версии образа.

## Запуск

```bash
cd topology
docker compose -f docker-compose.yml --profile go   up --build    # или --profile java / --profile rust
```

Все три клиента (Go, Java, Rust) собираются в контейнере через `--build` по своим
`Dockerfile` — отдельной локальной сборки не требуется.

Дождитесь, пока `pg-primary` и `pg-replica` станут `healthy` (реплика поднимается через
`pg_basebackup` с primary), и клиент начнёт печатать `WROTE`/`READ`.

## Упражнения

### 1. Пул через pgbouncer и грабли prepared statements

`pgbouncer` в стенде — `pool_mode = transaction`: на каждую транзакцию клиенту может
достаться другое физическое соединение к Postgres. Server-side prepared statements это
ломают — если драйвер готовит запрос (`Parse`) на одном соединении и пытается исполнить
его на другом, Postgres отвечает `prepared statement does not exist`. Все три клиента
отключают server-side prepare для writer-пула (который ходит через `pgbouncer`):

- Go (`pgx`): `cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol`.
- Java (JDBC): `prepareThreshold=0` в URL и как свойство датасорса.
- Rust (`sqlx`): `PgConnectOptions::statement_cache_capacity(0)`.

Reader-пул у каждого клиента подключается напрямую к `pg-replica`, минуя `pgbouncer`, —
там server-side prepare безопасен и оставлен по умолчанию.

### 2. Чтение с реплики и лаг

Клиент пишет через `PGBOUNCER_DSN` (primary) и сразу же читает через `REPLICA_DSN`
(`pg-replica`) в том же цикле. Репликация асинхронная — иногда только что записанная
строка ещё не видна в `SELECT` с реплики (read-your-writes не гарантируется). Понаблюдайте
за логом `WROTE id=N` / `READ [...]`: при небольшой нагрузке лаг обычно укладывается в один
тик (1 секунда), но на медленном железе видна задержка в несколько тиков.

### 3. Reconnect

```bash
docker compose -f topology/docker-compose.yml restart pg-primary
```

Пока `pg-primary` перезапускается, писатель начнёт печатать `ERROR write: ...` — клиент не
падает, а логирует ошибку и продолжает. После того как `pg-primary` снова пройдёт
healthcheck и `pgbouncer` восстановит соединение, запись возобновится сама, без перезапуска
клиента.

### 4. Failover

Ручной промоушен (в стенде нет Patroni/repmgr):

```bash
docker exec client-resilience-topology-pg-primary-1 pg_ctl stop -D /var/lib/postgresql/pgdata -m fast
docker exec client-resilience-topology-pg-replica-1 pg_ctl promote -D /var/lib/postgresql/pgdata
```

(Имена контейнеров — из `docker compose -f topology/docker-compose.yml ps`; при другом
имени проекта/каталога они будут отличаться.)

`MULTIHOST_DSN` задан как
`postgres://app:app_pw@pg-primary:5432,pg-replica:5432/app?target_session_attrs=read-write`.

- **Go (pgx)** и **Java (pgjdbc)** умеют multi-host DSN: драйвер перебирает хосты и
  выбирает тот, что отвечает как read-write. После промоушена реплики следующее
  подключение по тому же DSN уедет на новый primary без изменений в коде клиента.
- **Rust (sqlx) этого не поддерживает.** sqlx — не обёртка над `libpq`, а собственная
  реализация протокола Postgres на чистом Rust, и парсер DSN у неё рассчитан ровно на один
  host; `target_session_attrs` не распознаётся. Это открытый feature-request
  ([launchbadge/sqlx#3333](https://github.com/launchbadge/sqlx/issues/3333) «Multiple Hosts,
  Failover»), всё ещё не реализован в 0.9. Rust-клиент стенда явно логирует это
  ограничение при старте вместо того, чтобы имитировать failover, которого в клиенте нет.
  Это осознанная кросс-клиентская разница, а не недоделка стенда.

> Записи, принятые упавшим primary, но не успевшие реплицироваться на `pg-replica`, при
> промоушене теряются — репликация асинхронная. Это не баг стенда, а свойство топологии
> (см. статью).

## Остановка

```bash
docker compose -f topology/docker-compose.yml --profile go down -v    # тот же профиль, что запускали
```

## Лицензия

MIT — см. [LICENSE](../../LICENSE) в корне репозитория.
