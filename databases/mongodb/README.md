# MongoDB: глубокое погружение — стенды

Живые стенды к серии статей «MongoDB: глубокое погружение» на
[khorost.tech](https://khorost.tech) (секция `databases/`, `series: "mongodb"`).
**Phase 1 (cookbook) завершён:** все 7 стендов живьём прогнаны, реальные числа
зафиксированы в [`FIXTURES.md`](FIXTURES.md) — источник фактов для Phase 2
(7 статей серии). Метод — двухфазный, как `../clickhouse/`, `../scylladb/`,
`../kafka/`, `../java-deep-dive/`: сперва живой cookbook даёт реальные числа,
потом статьи пишутся строго из зафиксированных фактов.

Правило серии: **утверждение → стенд → число → ассерт → честная граница.**
Каждый содержательный тезис в статьях подкреплён живым прогоном конкретного
стенда, конкретным измеренным числом, программным ассертом (fail-loud) и,
где уместно, честной оговоркой о границах применимости результата (host-
зависимость, упрощение топологии стенда и т.п.).

PostgreSQL проходит через серию контрастной нитью (не отдельной статьёй):
документ vs строка / `JSONB`, oplog vs логическая/стриминговая репликация,
документное моделирование vs нормализация — числа берутся из живого
PG-контейнера (`compose/postgres.yml`), а не из рукомашества.

## Тема → каталог стенда → demo-скрипт → FIXTURES-секция

| # | Тема | Каталог стенда | Demo-скрипт | Topology | `FIXTURES.md` | Статья (slug) |
|---|---|---|---|---|---|---|
| 1 | Документная модель и схема-дизайн (+ PG JSONB) | `modeling/` | `ops/modeling-demo.sh` | replica-set + postgres | «Стенд #1» | `mongodb-document-model-and-schema-design` |
| 2 | WiredTiger: движок хранения (B-tree, MVCC, cache, journal) | `wiredtiger/` | `ops/wiredtiger-demo.sh` | replica-set | «Стенд #2» | `mongodb-wiredtiger-storage-engine` |
| 3 | Индексы вглубь: multikey, ESR, explain | `indexes/` | `ops/indexes-demo.sh` | replica-set | «Стенд #3» | `mongodb-indexes-deep` |
| 4 | Aggregation pipeline вглубь (+ Java-зеркало `java/aggregation/`) | `aggregation/` | `ops/aggregation-demo.sh` | replica-set | «Стенд #4» | `mongodb-aggregation-pipeline-deep` |
| 5 | Replica sets, oplog, concerns (+ PG-репликация) | `replication/` | `ops/replication-demo.sh` | replica-set + postgres | «Стенд #5» | `mongodb-replica-sets-oplog-concerns` |
| 6 | Sharding вживую: shard key, balancer, resharding | `sharding/` | `ops/sharding-demo.sh` | sharded | «Стенд #6» | `mongodb-sharding-in-production` |
| 7 | Эксплуатация, драйверы, карта выбора Mongo/Postgres (+ Java-зеркало `java/ops/`) | `ops-stand/` | `ops/ops-demo.sh` | replica-set | «Стенд #7» | `mongodb-operations-and-decision` |

(«Стенд #N» — соответствующий заголовок `## Стенд #N — …` в [`FIXTURES.md`](FIXTURES.md).)

**Обратите внимание:** Go-стенд темы #7 физически лежит в `ops-stand/` (модуль
`tech.khorost/mongodb-cookbook/ops`), а `ops/` — каталог shell-оркестраторов
серии (`*-demo.sh` + `verify-static.sh`), общий для всех 7 стендов. Тот же
приём, что в `../clickhouse/ops-stand` — сделано, чтобы имя каталога стенда
не совпадало с каталогом скриптов.

Общий детерминированный генератор датасета — `dataset/` (единый источник
документов для всех 7 стендов, `-seed 42`, `dataset/manifest.json`); каждый
`ops/*-demo.sh` импортирует его заново перед своим стендом.

## Версии и хост

| Компонент | Версия | Где закреплено |
|---|---|---|
| MongoDB (образ) | `mongo:8.2.11` | `compose/replica-set.yml`, `compose/sharded.yml` |
| PostgreSQL (образ) | `postgres:18` | `compose/postgres.yml` |
| Go-модули | `go 1.24`, `go.mongodb.org/mongo-driver/v2 v2.3.0` | `go.mod` каждого стенда |
| Java-реактор | `maven.compiler.release=25`, `org.mongodb:mongodb-driver-sync 5.5.1` | `java/pom.xml` (родитель), `java/aggregation`, `java/ops` |
| Сборочные образы гейта | `golang:1.25`, `maven:3.9-eclipse-temurin-25` | `ops/verify-static.sh` |
| Датасет | `seed=42`, users=50000, products=5000, orders=200000 | `dataset/manifest.json` |
| Хост прогонов | Docker Desktop/Windows, все контейнеры одного стенда — один физический хост | см. честные оговорки в `FIXTURES.md` |

Полные измеренные числа, ассерты и честные границы каждого пункта — в
[`FIXTURES.md`](FIXTURES.md).

## Топология стендов

### `compose/replica-set.yml` — 3-узловой replica set `rs0`

| Сервис | Образ | Внутри сети | С хоста |
|---|---|---|---|
| mongo1 (priority 2) | `mongo:8.2.11` | `mongo1:27017` | `localhost:27017` |
| mongo2 | `mongo:8.2.11` | `mongo2:27017` | `localhost:27018` |
| mongo3 | `mongo:8.2.11` | `mongo3:27017` | `localhost:27019` |

Инициация — `compose/init/rs-init.js` (`rs.initiate(...)`), выполняется
самим соответствующим `ops/*-demo.sh` сразу после `docker compose up`.

### `compose/sharded.yml` — шардированный кластер

| Сервис | Роль | Образ | Внутри сети | С хоста |
|---|---|---|---|---|
| csrs1 | config server RS (`csrs`, 1 узел) | `mongo:8.2.11` | `csrs1:27017` | `localhost:27020` |
| shard1a | shard RS `shard1` (1 узел) | `mongo:8.2.11` | `shard1a:27017` | `localhost:27021` |
| shard2a | shard RS `shard2` (1 узел) | `mongo:8.2.11` | `shard2a:27017` | `localhost:27022` |
| mongos1 | роутер (`--configdb csrs/csrs1:27017`) | `mongo:8.2.11` | `mongos1:27017` | `localhost:27023` |

Регистрация шардов — `compose/init/shard-init.js` (`sh.addShard(...)`),
после `rs.initiate()` на каждом из `csrs`/`shard1`/`shard2` — весь этот
порядок выполняется автоматически внутри `ops/sharding-demo.sh`.

**Честная оговорка топологии:** каждый шард здесь — 1-узловой RS (не
3-узловой, как был бы в проде) — упрощение ради воспроизводимости стенда на
одной машине; `mongos`/`csrs` реального продакшн-масштаба не меняются в
принципе, а число узлов шарда для демонстрации balancer/resharding/routing
(стенд #6) не критично.

### `compose/postgres.yml` — PostgreSQL-контраст

| Сервис | Образ | Внутри сети | С хоста |
|---|---|---|---|
| pg | `postgres:18` | `pg:5432` | `localhost:5433` |

## Каркас (Phase 1 завершён)

```
mongodb/
  compose/
    replica-set.yml       # 3-узловой rs0
    sharded.yml            # csrs + 2 шарда + mongos
    postgres.yml            # контраст-контейнер
    init/
      rs-init.js             # rs.initiate() для rs0
      shard-init.js           # sh.addShard() для shard1/shard2
  dataset/                    # общий генератор (-seed 42) + manifest.json
  drivers/go/                 # общий тонкий враппер mongo-go-driver/v2 для всех стендов
  modeling/                   # стенд #1 (+ postgres контраст)
  wiredtiger/                 # стенд #2
  indexes/                    # стенд #3
  aggregation/                # стенд #4 (Go)
  replication/                # стенд #5 (+ postgres контраст)
  sharding/                   # стенд #6
  ops-stand/                  # стенд #7 (Go) — НЕ каталог ops/, см. таблицу выше
  java/                       # Java-реактор (Maven, родительский pom.xml)
    aggregation/               # Java-зеркало стенда #4
    ops/                        # Java-зеркало стенда #7 (change streams)
  ops/
    modeling-demo.sh            # оркестратор стенда #1
    wiredtiger-demo.sh           # оркестратор стенда #2
    indexes-demo.sh               # оркестратор стенда #3
    aggregation-demo.sh            # оркестратор стенда #4 (Go + Java)
    replication-demo.sh             # оркестратор стенда #5
    sharding-demo.sh                 # оркестратор стенда #6
    ops-demo.sh                       # оркестратор стенда #7 (Go + Java)
    verify-static.sh                   # статический гейт (см. ниже)
  README.md
  FIXTURES.md                 # §1-§7 заполнены реальными числами всех 7 стендов
```

## Быстрая статическая проверка (без тяжёлых стендов)

```bash
cd mongodb
bash ops/verify-static.sh
```

Гейт для CI/pre-push: `bash -n` всех `ops/*.sh`, `go build -o /dev/null ./... && go vet ./...`
по всем 9 Go-модулям (`dataset`, `drivers/go`, `modeling`, `wiredtiger`,
`indexes`, `aggregation`, `replication`, `sharding`, `ops-stand`), `mvn
-DskipTests package` Java-реактора (`java/`, модули `aggregation` + `ops`),
`docker compose config -q` по трём комбинациям compose-файлов
(`replica-set.yml`; `replica-set.yml + postgres.yml`; `sharded.yml`). Требует
только Docker, **не поднимает ни одного сервиса** — секунды/минуты вместо
полного live-прогона (сборка Go/Java в контейнерах — самая долгая часть).

## Как воспроизвести стенд

Каждый `ops/*-demo.sh` **полностью самодостаточен**: сам поднимает нужный
compose (`down -v --remove-orphans` начисто → `up -d`), сам инициирует
replica set/sharded-топологию (`rs.initiate`/`sh.addShard` из
`compose/init/`), сам импортирует полный датасет (`seed=42`), гоняет
сценарий стенда (Go, и Go+Java для #4/#7), печатает FIXTURE-строки и в конце
сам гасит compose (`down -v`). Никакой ручной подготовки не требуется —
достаточно Docker и одной команды:

```bash
cd mongodb
bash ops/modeling-demo.sh        # стенд #1 (replica-set.yml + postgres.yml)
bash ops/wiredtiger-demo.sh      # стенд #2 (replica-set.yml)
bash ops/indexes-demo.sh         # стенд #3 (replica-set.yml)
bash ops/aggregation-demo.sh     # стенд #4 (replica-set.yml) — Go + Java-зеркало подряд
bash ops/replication-demo.sh     # стенд #5 (replica-set.yml + postgres.yml) — включает failover (docker stop primary)
bash ops/sharding-demo.sh        # стенд #6 (sharded.yml) — включает resharding (см. честную оговорку в FIXTURES.md)
bash ops/ops-demo.sh             # стенд #7 (replica-set.yml) — Go + Java-зеркало change streams, mongodump/restore
```

Повторный прогон того же скрипта воспроизводит те же ассерты (не обязательно
байт-в-байт те же тайминги — см. честные оговорки о хосте в `FIXTURES.md`);
`ops/sharding-demo.sh` — единственный, где повторный прогон может дать другой
исход именно для шага resharding (задокументировано в `FIXTURES.md` §6 как
наблюдаемая нестабильность на границе клиентского окна ожидания, не баг
стенда).
