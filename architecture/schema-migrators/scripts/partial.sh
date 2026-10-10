#!/usr/bin/env bash
#
# partial.sh — частичное применение миграции: главный риск, о котором статья.
#
# Вторая миграция: успешный ALTER TABLE ... ADD COLUMN note, затем заведомо
# ошибочный INSERT в несуществующую таблицу. После отказа проверяется:
#   - осталась ли в схеме колонка note (применилась ли половина миграции);
#   - что записано в журнале инструмента;
#   - что происходит при повторном запуске той же миграции.
# На базе с транзакционным DDL ожидается: колонки нет, в журнале только
# первая миграция, повторный запуск падает на той же ошибке. Иначе — схема и
# журнал разошлись, и повтор упирается в уже применённую половину.
#
#   bash scripts/partial.sh   → fixtures/20-partial.txt, fixtures/partial.tsv
#
# CockroachDB — только goose, Flyway и Liquibase: Atlas и Alembic там не
# доходят и до первой миграции (см. матрицу).

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=20-partial
fixture_header "$f"
TSV="$FIXTURES_DIR/partial.tsv"
printf 'tool\tengine\trun\texit\tprobe_table\tnote_column\tjournal\tmessage\n' > "$TSV"

apply_cmd() {  # <инструмент> <движок> <база>
    local t="$1" e="$2" db="$3" U J
    U="$(url "$e" "$db")"; J="$(jdbc "$e" "$db")"
    case "$t" in
        goose)     echo "docker run --rm --network smig -v \"\$PWD/tools/goose/partial:/mig\" smig-goose -dir /mig postgres '$U' up" ;;
        flyway)    echo "docker run --rm --network smig -v \"\$PWD/tools/flyway/partial:/flyway/sql\" flyway/flyway:13.9.0 -url='$J' -user=$(juser "$e") -password='$(jpass "$e")' migrate" ;;
        liquibase) echo "docker run --rm --network smig -e LIQUIBASE_ANALYTICS_ENABLED=false -v \"\$PWD/tools/liquibase:/liquibase/changelog\" smig-liquibase --url='$J' --username=$(juser "$e") --password='$(jpass "$e")' --search-path=/liquibase/changelog --changelog-file=partial.sql update" ;;
        atlas)     echo "docker run --rm --network smig -v \"\$PWD/tools/atlas/partial:/mig\" arigaio/atlas:1.3.3-community migrate apply --url '$U&search_path=public' --dir file:///mig" ;;
        alembic)   echo "docker run --rm --network smig -e SMIG_URL='$(sa_url "$e" "$db")' -v \"\$PWD/tools/alembic:/w\" -v \"\$PWD/tools/alembic/partial:/w/versions\" smig-alembic upgrade head" ;;
    esac
}

# Содержимое журнала — запросом к таблице инструмента.
journal_sql() {
    case "$1" in
        goose)     echo "SELECT string_agg(version_id || ':' || is_applied, ' ' ORDER BY id) FROM goose_db_version WHERE version_id > 0" ;;
        flyway)    echo "SELECT string_agg(version || ':' || success, ' ' ORDER BY installed_rank) FROM flyway_schema_history" ;;
        liquibase) echo "SELECT string_agg(id, ' ' ORDER BY orderexecuted) FROM databasechangelog" ;;
        atlas)     echo "SELECT string_agg(version || ':' || applied || '/' || total, ' ' ORDER BY version) FROM atlas_schema_revisions" ;;
        alembic)   echo "SELECT string_agg(version_num, ' ') FROM alembic_version" ;;
    esac
}

probe_state() {  # <инструмент> <движок> <база> <прогон> <код>
    local t="$1" e="$2" db="$3" run="$4" rc="$5" tbl col jr
    table_exists "$e" "$db" probe && tbl=есть || tbl=нет
    column_exists "$e" "$db" probe note && col=есть || col=нет
    capture "$f" "$t × $e: журнал после прогона $run" \
        "docker run --rm --network smig postgres:18.4 psql '$(url "$e" "$db")' -tAc \"$(journal_sql "$t")\""
    # Таблицы журнала может не быть вовсе — если откатилось всё, включая её.
    if [ "$CAPTURE_RC" != 0 ] && printf '%s' "$CAPTURE_OUT" | grep -q 'does not exist'; then
        jr="таблицы журнала нет"
    else
        jr="$(printf '%s' "$CAPTURE_OUT" | tail -1)"; jr="${jr:-пусто}"
    fi
    echo "# $t × $e, прогон $run: код $rc, таблица probe — $tbl, колонка note — $col, журнал — $jr" >> "$FIXTURES_DIR/$f.txt"
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$t" "$e" "$run" "$rc" "$tbl" "$col" "$jr" "$MSG" >> "$TSV"
    log "$t × $e, прогон $run: код $rc, probe — $tbl, note — $col, журнал — $jr"
}

for e in pg crdb; do
    case "$e" in pg) tools="$TOOLS" ;; crdb) tools="goose flyway liquibase" ;; esac
    for t in $tools; do
        db="$(dbname "$e" "$t")"
        fresh "$e" "$t"
        for run in 1 2; do
            capture "$f" "$t × $e: прогон $run — обе миграции, вторая падает на середине" "$(apply_cmd "$t" "$e" "$db")"
            rc="$CAPTURE_RC"; MSG="$(err_line "$CAPTURE_OUT")"
            probe_state "$t" "$e" "$db" "$run" "$rc"
        done
    done
done
log "записано: $FIXTURES_DIR/$f.txt, $TSV"
