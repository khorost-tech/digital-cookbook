#!/usr/bin/env bash
#
# recover.sh — восстановление после частичного применения на CockroachDB
# штатными командами инструментов. Два пути из двух возможных:
#
#   Liquibase — ДОВЕСТИ ДО КОНЦА: выполнить недостающую часть changeset руками,
#     проверить схему, отметить changeset выполненным (mark-next-changeset-ran),
#     убедиться, что update больше нечего применять.
#   Flyway — ВЕРНУТЬ ИСХОДНОЕ: откатить применённую половину руками, убрать
#     запись о неудаче (repair), применить исправленную миграцию.
#
# Команды мигратора идут без фильтров: фикстура хранит полный вывод и
# настоящий код возврата инструмента, а не код grep.
#
# Отметить частично выполненную миграцию «выполненной», не приведя схему к
# полному ожидаемому результату, — значит записать в журнал неправду. Поэтому
# перед командой согласования журнала каждый раз проверяется схема.
#
#   bash scripts/recover.sh   → fixtures/21-recover-crdb.txt

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=21-recover-crdb
fixture_header "$f"
psq() { echo "docker run --rm --network smig postgres:18.4 psql 'postgres://root@crdb:26257/$1?sslmode=disable' -tAc \"$2\""; }

# --- Liquibase: довести до конца -------------------------------------------
db=t_liquibase
fresh crdb liquibase
LB="docker run --rm --network smig -e LIQUIBASE_ANALYTICS_ENABLED=false -v \"\$PWD/tools/liquibase:/liquibase/changelog\" smig-liquibase --url='jdbc:postgresql://crdb:26257/$db?sslmode=disable' --username=root --password='' --search-path=/liquibase/changelog --changelog-file=partial.sql"
capture "$f" "Liquibase: сбой на середине второго changeset" "$LB update"
capture "$f" "Liquibase: схема и журнал после сбоя" "$(psq $db "SELECT (SELECT count(*) FROM information_schema.columns WHERE table_name = 'probe' AND column_name = 'note') AS note_column, (SELECT string_agg(id, ' ' ORDER BY orderexecuted) FROM databasechangelog) AS journal")"
capture "$f" "Liquibase: доводим changeset до полного результата руками" "$(psq $db "CREATE TABLE missing_table (id INT PRIMARY KEY); INSERT INTO missing_table VALUES (1)")"
capture "$f" "Liquibase: проверка схемы перед отметкой в журнале" "$(psq $db "SELECT (SELECT count(*) FROM information_schema.columns WHERE table_name = 'probe' AND column_name = 'note') AS note_column, (SELECT count(*) FROM missing_table WHERE id = 1) AS inserted_row")"
capture "$f" "Liquibase: mark-next-changeset-ran" "$LB mark-next-changeset-ran"
capture "$f" "Liquibase: журнал после отметки" "$(psq $db "SELECT string_agg(id || ':' || exectype, ' ' ORDER BY orderexecuted) FROM databasechangelog")"
capture "$f" "Liquibase: update после восстановления" "$LB update"

# --- Flyway: вернуть исходное ----------------------------------------------
db=t_flyway
fresh crdb flyway
FW="docker run --rm --network smig flyway/flyway:13.9.0 -url='jdbc:postgresql://crdb:26257/$db?sslmode=disable' -user=root -password=''"
capture "$f" "Flyway: сбой на середине второй миграции" "docker run --rm --network smig -v \"\$PWD/tools/flyway/partial:/flyway/sql\" flyway/flyway:13.9.0 -url='jdbc:postgresql://crdb:26257/$db?sslmode=disable' -user=root -password='' migrate"
capture "$f" "Flyway: схема и журнал после сбоя" "$(psq $db "SELECT (SELECT count(*) FROM information_schema.columns WHERE table_name = 'probe' AND column_name = 'note') AS note_column, (SELECT string_agg(version || ':' || success, ' ' ORDER BY installed_rank) FROM flyway_schema_history) AS journal")"
capture "$f" "Flyway: откатываем применённую половину руками" "$(psq $db "ALTER TABLE probe DROP COLUMN note")"
capture "$f" "Flyway: проверка схемы перед repair" "$(psq $db "SELECT count(*) AS note_column FROM information_schema.columns WHERE table_name = 'probe' AND column_name = 'note'")"
capture "$f" "Flyway: repair" "docker run --rm --network smig -v \"\$PWD/tools/flyway/partial-fixed:/flyway/sql\" flyway/flyway:13.9.0 -url='jdbc:postgresql://crdb:26257/$db?sslmode=disable' -user=root -password='' repair"
capture "$f" "Flyway: журнал после repair" "$(psq $db "SELECT string_agg(version || ':' || success, ' ' ORDER BY installed_rank) FROM flyway_schema_history")"
capture "$f" "Flyway: migrate исправленной миграции" "docker run --rm --network smig -v \"\$PWD/tools/flyway/partial-fixed:/flyway/sql\" flyway/flyway:13.9.0 -url='jdbc:postgresql://crdb:26257/$db?sslmode=disable' -user=root -password='' migrate"
capture "$f" "Flyway: схема и журнал после восстановления" "$(psq $db "SELECT (SELECT count(*) FROM information_schema.columns WHERE table_name = 'probe' AND column_name = 'note') AS note_column, (SELECT string_agg(version || ':' || success, ' ' ORDER BY installed_rank) FROM flyway_schema_history) AS journal")"
log "записано: $FIXTURES_DIR/$f.txt"
