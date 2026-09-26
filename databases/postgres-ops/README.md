# postgres-ops — PostgreSQL в проде (стенды)

Живые стенды к серии статей «PostgreSQL в проде»: эксплуатация самого сервера
(не клиентский код). Стенды 1–4 самодостаточны — Docker, пиновка версий, реальные
замеры в `fixtures/`. Пятая часть — прод-кейс: не docker-стенд, а sanitized-runbook.

| Материал | Статья | Что показывает | Статус |
|---|---|---|---|
| [`explain/`](explain/) | Оптимизация запросов: EXPLAIN на практике | чтение `EXPLAIN (ANALYZE, BUFFERS)`, типы сканирования, btree/GiN/GiST/BRIN как узлы плана, устаревшая статистика, антипаттерны, `pg_stat_statements` (+ Go) | готов |
| [`partitioning-vacuum/`](partitioning-vacuum/) | Партиционирование, VACUUM и bloat | declarative partitioning + pruning, bloat от MVCC (pgstattuple), тюнинг autovacuum, `VACUUM`/`VACUUM FULL`/`pg_repack` (+ живая разница блокировок), txid wraparound | готов |
| [`pooling/`](pooling/) | Пулинг соединений: PgBouncer и pgcat | цена соединения (мультиплексирование), режимы session/transaction/statement, поломки transaction pooling (LISTEN/prepared), PgBouncer + pgcat + Odyssey, бенчмарки | готов |
| [`ha-patroni/`](ha-patroni/) | HA PostgreSQL: Patroni, репликация, PITR | 3× PG + 3× etcd + HAProxy, sync/async (+51% latency), автофейловер (RTO ~33 c), split-brain, replication slots, PITR на pgBackRest и wal-g, Go health-check | готов |
| [`timescaledb-zabbix/`](timescaledb-zabbix/) | TimescaleDB под Zabbix (прод-кейс) | sanitized-runbook: конвертация history/trends на гипертаблицы, `drop_chunks` vs `DELETE`, компрессия, ловушка `compress_older ≈ hk_history`, проверочные запросы и грабли (не docker-стенд) | готов |

Разграничение: выбор индекса под задачу — в серии [db-indexes](../db-indexes/);
клиентские пулы и reconnect — в статье про надёжную работу с PostgreSQL из
Go/Java/Rust; аномалии изоляции — в серии [transactions](../transactions/).
Здесь — только эксплуатация сервера.

## Версии (факт на 2026-07)

PostgreSQL 18.4. Прочие инструменты — в README соответствующего стенда.
