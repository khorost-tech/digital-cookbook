# explain — чтение EXPLAIN на практике (стенд)

Живой стенд к статье «Оптимизация запросов PostgreSQL: EXPLAIN на практике»
(серия «PostgreSQL в проде»). Docker.

Фокус — **чтение вывода** `EXPLAIN (ANALYZE, BUFFERS)`: структура плана, оценка против
факта, обращения к буферам, стоимость узлов. Типы индексов (btree/GiN/GiST/BRIN)
показаны через то, **как их узел выглядит в плане**, а не «когда какой выбирать» —
выбор индекса разобран в серии [db-indexes](../../db-indexes/).

## Как запускать

    docker compose up -d
    ./run.sh sql/00-schema.sql          # схема магазина + данные (~8M строк, ~30 с)
    ./run.sh sql/01-explain-basics.sql  # анатомия EXPLAIN(ANALYZE,BUFFERS)
    ./run.sh sql/02-scan-types.sql      # Seq / Index / Index Only / Bitmap scan
    ./run.sh sql/03-index-btree.sql     # btree в плане: Index Cond, порядок без Sort
    ./run.sh sql/04-index-gin.sql       # GiN: Bitmap Index Scan + Recheck Cond
    ./run.sh sql/05-index-gist.sql      # GiST range (&&) и kNN (ORDER BY <->)
    ./run.sh sql/06-index-brin.sql      # BRIN: размер и узел в плане
    ./run.sh sql/07-stats-stale.sql     # устаревшая статистика ломает оценку
    ./run.sh sql/08-antipatterns.sql    # как в плане видно, что индекс не работает
    ./run.sh sql/09-pgss-heavy.sql      # pg_stat_statements: поиск тяжёлых запросов

Каждый скрипт печатает реальный вывод планировщика — снимки в `fixtures/`.

Go-утилита `cmd/pgss-top` читает топ `pg_stat_statements` по суммарному времени:

    cd cmd/pgss-top && go build ./... && ./pgss-top

## Версии (зафиксированы, проверено 2026-07)

| Компонент | Версия |
|---|---|
| PostgreSQL | 18.4 |
| Go | 1.26.3 |
| github.com/jackc/pgx/v5 | 5.10.0 |

Проверено на фактическом тулчейне 2026-07: PostgreSQL 18.4.
