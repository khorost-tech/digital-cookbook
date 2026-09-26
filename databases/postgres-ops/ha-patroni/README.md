# ha-patroni — HA PostgreSQL: Patroni, репликация, PITR (стенд)

Живой стенд к статье «HA PostgreSQL: Patroni, репликация и PITR-бэкапы» (серия
«PostgreSQL в проде»). Docker.

Высокая доступность самого сервера: streaming-репликация (sync/async), автоматический
failover через Patroni поверх etcd, маршрутизация HAProxy, replication slots и PITR-бэкапы
(pgBackRest и wal-g).

## Топология

    3× PostgreSQL 18.4 под Patroni  ←→  3× etcd (кворум DCS)
                    ↑
                 HAProxy :5000 (запись → лидер) / :5001 (чтение → реплики) / :7000 (stats)
    Patroni REST :8008 (patroni1 проброшен для health-check)

PITR вынесен в подкаталог `pitr/` (отдельный docker-compose, single-node) — это про
бэкап, а не про репликацию, и два инструмента спорят за единственный archive_command.

## Как запускать

    docker compose build && docker compose up -d
    # подождать ~45 c инициализации кластера
    docker compose exec patroni1 patronictl -c /etc/patroni/patroni.yml list

    ./run.sh sql/00-setup.sql          # таблица в лидере (через HAProxy :5000)
    ./run.sh sql/01-replication.sql    # состояние pg_stat_replication
    ./run.sh sql/02-slots.sql          # replication slots и удерживаемый WAL
    ./repl-demo.sh                     # latency: async vs sync репликация
    ./failover-demo.sh                 # убить лидера, замер RTO, split-brain

PITR (отдельный подстенд):

    cd pitr && HTTPS_PROXY=http://<proxy> docker compose build && docker compose up -d
    ./pitr-pgbackrest.sh               # PITR через pgBackRest
    ./pitr-walg.sh                     # PITR через wal-g

Go health-check (собирать в хосте):

    cd cmd/replica-health && go run .  # лаг реплик + топология Patroni REST

Снимки прогонов — в `fixtures/`.

## Что показывает стенд

- **репликация** — sync стоит +51% latency ради RPO=0; Patroni держит одну sync-реплику;
- **failover** — убили лидера → RTO ~33 c (определяется ttl=30), новый лидер на новом
  timeline, вернувшийся узел входит репликой (split-brain невозможен);
- **slots** — Patroni создаёт physical slot на реплику; риск роста retained WAL при отвале;
- **PITR** — pgBackRest и wal-g откатывают базу на момент до ошибочной операции;
- **health-check** — лаг реплик из pg_stat_replication + топология из Patroni REST.

## Версии (зафиксированы, проверено 2026-07)

| Компонент | Версия |
|---|---|
| PostgreSQL | 18.4 |
| Patroni | 4.1.4 |
| etcd | 3.6.13 |
| HAProxy | 3.0 |
| pgBackRest | 2.58.0 |
| wal-g | 3.0.8 |
| Go / pgx | 1.26.3 / v5.10.0 |

Проверено на фактическом тулчейне 2026-07. Точная версия Patroni — `patroni --version` в образе.
