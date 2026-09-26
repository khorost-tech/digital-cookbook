# partitioning-vacuum — обслуживание хранилища PostgreSQL (стенд)

Живой стенд к статье «Партиционирование, VACUUM и борьба с bloat в PostgreSQL»
(серия «PostgreSQL в проде»). Docker.

Обслуживание хранилища: как резать большие таблицы на партиции, откуда берётся bloat
из-за MVCC, как его измерять и убирать (`VACUUM` / `VACUUM FULL` / `pg_repack`), как
autovacuum держит статистику и предотвращает txid wraparound.

Образ — `postgres:18.4` + `pg_repack` (внешнее расширение из PGDG). `pgstattuple` — contrib.

## Как запускать

    docker compose build && docker compose up -d
    ./run.sh sql/00-setup.sql              # расширения + версии
    ./run.sh sql/01-partitioning.sql       # range/list/hash + partition pruning
    ./run.sh sql/02-bloat-mvcc.sql         # откуда bloat, измерение pgstattuple
    ./run.sh sql/03-autovacuum-tuning.sql  # когда autovacuum не успевает и как тюнить
    ./run.sh sql/04-vacuum-repack.sql      # VACUUM vs VACUUM FULL vs pg_repack (SQL-часть)
    ./run.sh sql/05-wraparound-freeze.sql  # txid wraparound и freeze
    ./lock-demo.sh                         # живая разница блокировок VACUUM FULL vs pg_repack

Снимки прогонов — в `fixtures/`.

`pg_repack` — CLI-утилита (не SQL), вызывается отдельно:

    docker compose exec -T postgres pg_repack -U postgres -d opsdemo -t <table> --no-order

## Версии (зафиксированы, проверено 2026-07)

| Компонент | Версия |
|---|---|
| PostgreSQL | 18.4 |
| pg_repack | 1.5.3 |
| pgstattuple | 1.5 (contrib) |

Проверено на фактическом тулчейне 2026-07: PostgreSQL 18.4, pg_repack 1.5.3.
