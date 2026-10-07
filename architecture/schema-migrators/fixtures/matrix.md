# Матрица: инструмент × СУБД

Шаги в ячейке: apply1 / journal / apply2 / introspect / rollback.

| Инструмент | PostgreSQL | CockroachDB | Picodata |
|---|---|---|---|
| goose | ok / ok / ok / n/a / ok | ok / ok / ok / n/a / ok | fail / fail / fail / n/a / no-pre |
| flyway | ok / ok / ok / n/a / edition | ok / ok / ok / n/a / edition | fail / fail / fail / n/a / no-pre |
| liquibase | ok / ok / ok / n/a / ok | ok / ok / ok / n/a / ok | fail / fail / fail / n/a / no-pre |
| atlas | ok / ok / ok / ok / edition | fail / ok / fail / no-pre / no-pre | fail / fail / fail / no-pre / no-pre |
| alembic | ok / ok / ok / ok / ok | fail / fail / fail / no-pre / no-pre | fail / fail / fail / no-pre / no-pre |

## Отказы — первая строка ошибки

| Инструмент | СУБД | Шаг | Исход | Сообщение |
|---|---|---|---|---|
| flyway | PostgreSQL | rollback | edition | `ERROR: Flyway Redgate Edition Required: undo is not supported by OSS Edition` |
| atlas | PostgreSQL | rollback | edition | `Error: unknown flag: --url` |
| flyway | CockroachDB | rollback | edition | `ERROR: Flyway Redgate Edition Required: undo is not supported by OSS Edition` |
| atlas | CockroachDB | apply1 | fail | `Error: pq: unexpected transaction status idle` |
| atlas | CockroachDB | apply2 | fail | `Error: pq: unexpected transaction status idle` |
| atlas | CockroachDB | introspect | no-pre | `triggers, and stored procedures are not supported. To read more: https://atlasgo.io/community-edition` |
| atlas | CockroachDB | rollback | no-pre | `Error: unknown flag: --url` |
| alembic | CockroachDB | apply1 | fail | `AssertionError: Could not determine version from string 'CockroachDB CCL v26.2.5 (aarch64-unknown-linux-gnu, built 2026/07/28 18:55:27, go1.25.5)'` |
| alembic | CockroachDB | apply2 | fail | `AssertionError: Could not determine version from string 'CockroachDB CCL v26.2.5 (aarch64-unknown-linux-gnu, built 2026/07/28 18:55:27, go1.25.5)'` |
| alembic | CockroachDB | introspect | no-pre | `AssertionError: Could not determine version from string 'CockroachDB CCL v26.2.5 (aarch64-unknown-linux-gnu, built 2026/07/28 18:55:27, go1.25.5)'` |
| alembic | CockroachDB | rollback | no-pre | `AssertionError: Could not determine version from string 'CockroachDB CCL v26.2.5 (aarch64-unknown-linux-gnu, built 2026/07/28 18:55:27, go1.25.5)'` |
| goose | Picodata | apply1 | fail | `2026/10/05 08:10:12 goose run: ERROR: sbroad: table with name "goose_db_version" not found (SQLSTATE XX000); ERROR: sbroad: rule parsing error:  --> 2:14` |
| goose | Picodata | apply2 | fail | `2026/10/05 08:10:13 goose run: ERROR: sbroad: table with name "goose_db_version" not found (SQLSTATE XX000); ERROR: sbroad: rule parsing error:  --> 2:14` |
| goose | Picodata | rollback | no-pre | `2026/10/05 08:10:13 goose run: ERROR: sbroad: table with name "goose_db_version" not found (SQLSTATE XX000); ERROR: sbroad: rule parsing error:  --> 2:14` |
| flyway | Picodata | apply1 | fail | `ERROR: Unable to determine the original schema for the connection` |
| flyway | Picodata | apply2 | fail | `ERROR: Unable to determine the original schema for the connection` |
| flyway | Picodata | rollback | no-pre | `ERROR: Flyway Redgate Edition Required: undo is not supported by OSS Edition` |
| liquibase | Picodata | apply1 | fail | `ERROR: Exception Primary Reason:  ERROR: sbroad: rule parsing error:  --> 1:114` |
| liquibase | Picodata | apply2 | fail | `ERROR: Exception Primary Reason:  ERROR: sbroad: rule parsing error:  --> 1:114` |
| liquibase | Picodata | rollback | no-pre | `ERROR: Exception Primary Reason:  ERROR: sbroad: rule parsing error:  --> 1:114` |
| atlas | Picodata | apply1 | fail | `Error: postgres: scanning system variables: pq: sbroad: SQL function current_setting not found (XX000)` |
| atlas | Picodata | apply2 | fail | `Error: postgres: scanning system variables: pq: sbroad: SQL function current_setting not found (XX000)` |
| atlas | Picodata | introspect | no-pre | `Error: postgres: scanning system variables: pq: sbroad: SQL function current_setting not found (XX000)` |
| atlas | Picodata | rollback | no-pre | `Error: unknown flag: --url` |
| alembic | Picodata | apply1 | fail | `sqlalchemy.exc.InternalError: (psycopg.errors.InternalError_) sbroad: rule parsing error:  --> 1:26` |
| alembic | Picodata | apply2 | fail | `sqlalchemy.exc.InternalError: (psycopg.errors.InternalError_) sbroad: rule parsing error:  --> 1:26` |
| alembic | Picodata | introspect | no-pre | `sqlalchemy.exc.InternalError: (psycopg.errors.InternalError_) sbroad: rule parsing error:  --> 1:26` |
| alembic | Picodata | rollback | no-pre | `sqlalchemy.exc.InternalError: (psycopg.errors.InternalError_) sbroad: rule parsing error:  --> 1:26` |
