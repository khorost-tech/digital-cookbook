# Матрица принимаемого синтаксиса Picodata

Сгенерировано `report.py` по выводам раннеров. ✅ — конструкция **принята**
сервером, ❌ — отвергнута. Контрольные пробы у всех клиентов сошлись, иначе
отчёт не был бы собран.

⚠️ **Эта таблица не проверяет результаты.** Раннеры выполняют запрос и
смотрят только на факт отказа: «принято» здесь означает «не отвергнуто»,
а не «даёт тот же ответ, что PostgreSQL». Совпадение результатов и
постусловия DML проверяются отдельно — `results.sh`, вывод в
`out/results.txt`.

| Проба | Группа | psql | pgx | psycopg | JDBC PostgreSQL | picodata-jdbc |
|---|---|---|---|---|---|---|
| `version` | интроспекция | ✅ | ✅ | ✅ | ✅ | ✅ |
| `current_database` | интроспекция | ❌ | ❌ | ❌ | ❌ | ❌ |
| `current_schema` | интроспекция | ❌ | ❌ | ❌ | ❌ | ❌ |
| `information_schema` | интроспекция | ❌ | ❌ | ❌ | ❌ | ❌ |
| `pg_tables` | интроспекция | ❌ | ❌ | ❌ | ❌ | ❌ |
| `pg_class` | интроспекция | ❌ | ❌ | ❌ | ❌ | ❌ |
| `cte` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `cte_recursive` | запросы | ❌ | ❌ | ❌ | ❌ | ❌ |
| `join` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `left_join` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `subquery_in` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `exists_uncorrelated` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `exists_correlated` | запросы | ❌ | ❌ | ❌ | ❌ | ❌ |
| `union` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `union_all` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `except` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `intersect` | запросы | ❌ | ❌ | ❌ | ❌ | ❌ |
| `distinct` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `group_having` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `order_by_projected` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `order_by_position` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `order_by_unprojected` | запросы | ❌ | ❌ | ❌ | ❌ | ❌ |
| `order_by_expr` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `order_by_alias` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `limit` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `offset` | запросы | ❌ | ❌ | ❌ | ❌ | ❌ |
| `window_row_number_small` | запросы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `window_row_number_full` | запросы | ❌ | ❌ | ❌ | ❌ | ❌ |
| `window_rank` | запросы | ❌ | ❌ | ❌ | ❌ | ❌ |
| `case_when` | выражения | ✅ | ✅ | ✅ | ✅ | ✅ |
| `coalesce` | выражения | ✅ | ✅ | ✅ | ✅ | ✅ |
| `cast` | выражения | ✅ | ✅ | ✅ | ✅ | ✅ |
| `like` | выражения | ✅ | ✅ | ✅ | ✅ | ✅ |
| `ilike` | выражения | ✅ | ✅ | ✅ | ✅ | ✅ |
| `between` | выражения | ✅ | ✅ | ✅ | ✅ | ✅ |
| `is_null` | выражения | ✅ | ✅ | ✅ | ✅ | ✅ |
| `concat` | выражения | ✅ | ✅ | ✅ | ✅ | ✅ |
| `substr` | выражения | ✅ | ✅ | ✅ | ✅ | ✅ |
| `upper_lower` | выражения | ✅ | ✅ | ✅ | ✅ | ✅ |
| `now` | выражения | ❌ | ❌ | ❌ | ❌ | ❌ |
| `agg_basic` | агрегаты | ✅ | ✅ | ✅ | ✅ | ✅ |
| `agg_string` | агрегаты | ✅ | ✅ | ✅ | ✅ | ✅ |
| `agg_count_distinct` | агрегаты | ✅ | ✅ | ✅ | ✅ | ✅ |
| `explain` | планы | ✅ | ✅ | ✅ | ✅ | ✅ |
| `set_param` | сессия | ✅ | ✅ | ✅ | ✅ | ✅ |
| `show_param` | сессия | ❌ | ❌ | ❌ | ❌ | ❌ |
| `ddl_create_table` | ddl | ✅ | ✅ | ✅ | ✅ | ✅ |
| `ddl_alter_add_column` | ddl | ✅ | ✅ | ✅ | ✅ | ✅ |
| `ddl_alter_drop_column` | ddl | ❌ | ❌ | ❌ | ❌ | ❌ |
| `ddl_create_index` | ddl | ✅ | ✅ | ✅ | ✅ | ✅ |
| `ddl_drop_index` | ddl | ✅ | ✅ | ✅ | ✅ | ✅ |
| `dml_insert` | dml | ✅ | ✅ | ✅ | ✅ | ✅ |
| `dml_insert_multi` | dml | ✅ | ✅ | ✅ | ✅ | ✅ |
| `dml_update` | dml | ✅ | ✅ | ✅ | ✅ | ✅ |
| `dml_delete` | dml | ✅ | ✅ | ✅ | ✅ | ✅ |
| `dml_returning` | dml | ❌ | ❌ | ❌ | ❌ | ❌ |
| `dml_upsert` | dml | ❌ | ❌ | ❌ | ❌ | ❌ |
| `dml_truncate` | dml | ✅ | ✅ | ✅ | ✅ | ✅ |
| `ddl_drop_table` | ddl | ✅ | ✅ | ✅ | ✅ | ✅ |

Всего проб: **59**, принято хотя бы одним клиентом: **42**.

## Расхождения между клиентами

Не обнаружены: все клиенты сошлись на каждой пробе. Это и есть главный
вывод уровня SQL — граница совместимости проходит по серверу, а выбор
драйвера на неё не влияет.

## Причины отказов

Сообщения сервера по отвергнутым пробам (по первому клиенту в списке).

- `current_database` — psql:<stdin>:1: ERROR: sbroad: SQL function current_database not found
- `current_schema` — psql:<stdin>:1: ERROR: sbroad: SQL function current_schema not found
- `information_schema` — psql:<stdin>:1: ERROR: sbroad: rule parsing error: --> 1:40 | 1 | SELECT count(*) FROM information_schema.tables | ^--- | = expected EOI, IndexedByExpr, or DqlO
- `pg_tables` — psql:<stdin>:1: ERROR: sbroad: rule parsing error: --> 1:32 | 1 | SELECT count(*) FROM pg_catalog.pg_tables | ^--- | = expected EOI, IndexedByExpr, or DqlOption
- `pg_class` — psql:<stdin>:1: ERROR: sbroad: rule parsing error: --> 1:32 | 1 | SELECT count(*) FROM pg_catalog.pg_class | ^--- | = expected EOI, IndexedByExpr, or DqlOption
- `cte_recursive` — psql:<stdin>:1: ERROR: sbroad: rule parsing error: --> 1:6 | 1 | WITH RECURSIVE t(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM t WHERE n<3) SELECT count(*) FROM t 
- `exists_correlated` — psql:<stdin>:1: ERROR: sbroad: column with name "id" and scan Some("p") not found
- `intersect` — psql:<stdin>:1: ERROR: sbroad: rule parsing error: --> 1:36 | 1 | SELECT id FROM products WHERE id<5 INTERSECT SELECT id FROM products WHERE id<3 | ^--- | = exp
- `order_by_unprojected` — psql:<stdin>:1: ERROR: sbroad: column with name "views" not found
- `offset` — psql:<stdin>:1: ERROR: sbroad: rule parsing error: --> 1:45 | 1 | SELECT id FROM products ORDER BY id LIMIT 5 OFFSET 10 | ^--- | = expected EOI or DqlOption
- `window_row_number_full` — psql:<stdin>:1: ERROR: sbroad: box error: ProcLua: {"code":32,"base_type":"ClientError","type":"ClientError","message":"Error on replicaset <uuid>
- `window_rank` — psql:<stdin>:1: ERROR: sbroad: invalid query: window function rank does not exist
- `now` — psql:<stdin>:1: ERROR: sbroad: SQL function now not found
- `show_param` — psql:<stdin>:1: ERROR: sbroad: rule parsing error: --> 1:1 | 1 | SHOW server_version | ^--- | = expected EOI, AnonymousBlock, CreatePlugin, DropPlugin, AlterPlu
- `ddl_alter_drop_column` — psql:<stdin>:1: ERROR: sbroad: unsupported DDL: `ALTER TABLE _ DROP COLUMN` is not yet supported
- `dml_returning` — psql:<stdin>:1: ERROR: sbroad: rule parsing error: --> 1:51 | 1 | INSERT INTO ddl_probe (id, title) VALUES (4, 'd') RETURNING id | ^--- | = expected EOI or DqlO
- `dml_upsert` — psql:<stdin>:1: ERROR: sbroad: rule parsing error: --> 1:53 | 1 | INSERT INTO ddl_probe (id, title) VALUES (1, 'dup') ON CONFLICT (id) DO UPDATE SET title = 'du

## Метаданные JDBC

SQL-пробы у обоих драйверов совпадают, поэтому вопрос «зачем фирменный
драйвер» решается не здесь, а на уровне `DatabaseMetaData` — того самого,
по которому GUI-клиенты строят дерево объектов.

| Проба | JDBC PostgreSQL | picodata-jdbc | Значение у picodata-jdbc |
|---|---|---|---|
| `md_product_name` | ✅ | ✅ | Picodata 15.0 |
| `md_get_tables` | ❌ | ✅ | таблиц: 1 |
| `md_get_columns` | ❌ | ✅ | колонок products: 7 |
| `md_get_schemas` | ❌ | ✅ | схем: 1 |
| `md_get_primary_keys` | ❌ | ✅ | ключей products: 1 |
| `md_get_type_info` | ❌ | ✅ | типов: 16 |
| `md_supports_transactions` | ✅ | ✅ | заявляет поддержку транзакций: false |
