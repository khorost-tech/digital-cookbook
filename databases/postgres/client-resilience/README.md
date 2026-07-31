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
- Свободный адрес в пуле Docker для одной bridge-сети (конкретная подсеть не
  фиксируется — её выбирает Docker, а `pg_hba.conf` на primary разрешает `samenet`).

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

### 1. Пул через pgbouncer и сложности prepared statements

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

Ручной промоушен (в стенде нет Patroni/repmgr). Из каталога `topology/`:

```bash
docker compose stop pg-primary
docker compose exec -u postgres pg-replica pg_ctl promote -D /var/lib/postgresql/pgdata
docker compose --profile go restart client-go   # чтобы стартовая MULTIHOST-проверка прошла заново
```

Три детали, без которых упражнение не воспроизводится:

- **Гасить контейнер, а не процесс.** У `pg-primary` стоит `restart: unless-stopped`,
  поэтому `pg_ctl stop` внутри контейнера приводит к тому, что Docker тут же поднимает
  его заново: старый primary воскресает уже после промоушена реплики, и вместо failover
  получаются два независимых primary (`pg_is_in_recovery()` = `f` на обоих).
- **`-u postgres`.** `docker compose exec` без него заходит под root, а `pg_ctl` под root
  работать отказывается («Please log in ... as the (unprivileged) user»).
- **Windows + Git Bash:** префикс `MSYS_NO_PATHCONV=1`, иначе `/var/lib/postgresql/pgdata`
  превращается в `C:/Program Files/Git/var/lib/...` и `pg_ctl` не находит каталог.

После рестарта клиента стартовая проверка логирует уже адрес промотированной реплики
(проверено на всех трёх профилях в WSL2 + Docker Desktop):

```
Go,   до failover:  [go] MULTIHOST: ... -> выбран хост 172.24.0.2/32:5432 (in_recovery=false)   # pg-primary
Go,   после:        [go] MULTIHOST: ... -> выбран хост 172.24.0.4/32:5432 (in_recovery=false)   # pg-replica
Java, до failover:  [java] MULTIHOST: targetServerType=primary -> выбран хост 172.24.0.2/32:5432
Java, после:        [java] MULTIHOST: targetServerType=primary -> выбран хост 172.24.0.3/32:5432
Rust:               [rust] MULTIHOST: не удалось разобрать даже первый хост MULTIHOST_DSN ... (ожидаемо)
```

Основной writer при этом продолжает ошибаться (`ERROR write`) — он идёт через pgbouncer,
жёстко закреплённый на `host=pg-primary`, и сам на новый primary не переезжает. Это не
дефект стенда, а ровно тот разрыв, ради которого multi-host DSN показывается отдельно.

`MULTIHOST_DSN` задан как
`postgres://app:app_pw@pg-primary:5432,pg-replica:5432/app?target_session_attrs=read-write`.

- **Go (pgx)** подключается по `MULTIHOST_DSN` как есть: pgx понимает libpq-параметр
  `target_session_attrs=read-write` напрямую из DSN, перебирает хосты и выбирает тот, что
  отвечает как read-write.
- **Java (pgjdbc) `target_session_attrs` не понимает.** У pgjdbc свой параметр для той же
  задачи — `targetServerType=primary`. Клиент стенда отбрасывает исходный query из
  `MULTIHOST_DSN` и подставляет `targetServerType=primary`, чтобы получить тот же результат
  (подключение к текущему read-write primary) через pgjdbc-совместимый параметр.
- И в Go, и в Java: после промоушена реплики следующее подключение уедет на новый primary
  без изменений в коде клиента — просто разными параметрами DSN.
- **Rust (sqlx) не поддерживает ни то, ни другое.** sqlx — не обёртка над `libpq`, а собственная
  реализация протокола Postgres на чистом Rust, и парсер DSN у неё рассчитан ровно на один
  host; `target_session_attrs` не распознаётся. Это открытый feature-request
  ([launchbadge/sqlx#3333](https://github.com/launchbadge/sqlx/issues/3333) «Multiple Hosts,
  Failover»), всё ещё не реализован в 0.9. Rust-клиент стенда явно логирует это
  ограничение при старте вместо того, чтобы имитировать failover, которого в клиенте нет.
  Это осознанная кросс-клиентская разница, а не недоделка стенда.

> Записи, принятые упавшим primary, но не успевшие реплицироваться на `pg-replica`, при
> промоушене теряются — репликация асинхронная. Это не баг стенда, а свойство топологии
> (см. статью).

### Три кросс-клиентских различия, которые вылезли на прогоне

- **Пул не должен падать в конструкторе.** `pgxpool` (Go) соединение при создании не
  открывает, а HikariCP (дефолт `initializationFailTimeout=1`) и `PgPoolOptions::connect()`
  (sqlx) — открывают и бросают исключение, если не смогли. В стенде это ломало ровно
  failover-сценарий: перезапущенный при мёртвом primary Java-клиент падал с
  `Failed to initialize pool`, Rust — с `FATAL: не удалось создать writer pool`, оба не
  доходя до цикла и до MULTIHOST-проверки. Исправлено явно: `setInitializationFailTimeout(-1)`
  у Hikari, `connect_lazy_with`/`connect_lazy` у sqlx.
- **`extra_float_digits` против pgbouncer.** sqlx шлёт этот startup-параметр, а pgbouncer в
  transaction mode отвергает всё, чего нет в `ignore_startup_parameters` — Rust-клиент падал
  с `unsupported startup parameter: extra_float_digits` ещё до первого запроса. pgx и pgjdbc
  этот параметр не шлют, поэтому упирался только Rust. Параметр добавлен в `pgbouncer.ini`.
- **`serial` — это INT4.** `pgx` и `pgjdbc` молча отдают такую колонку в `int64`/`long`, а
  sqlx требует точного соответствия и падает с `Rust type i64 (as SQL type INT8) is not
  compatible with SQL type INT4`. В Rust-клиенте id читается как `i32`.

## Остановка

```bash
docker compose -f topology/docker-compose.yml --profile go down -v    # тот же профиль, что запускали
```

## Лицензия

MIT — см. [LICENSE](../../LICENSE) в корне репозитория.
