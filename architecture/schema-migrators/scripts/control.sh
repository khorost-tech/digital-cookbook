#!/usr/bin/env bash
#
# control.sh — контрольная проба раннера на PostgreSQL.
#
# Каждому инструменту даётся заведомо невалидная миграция (лишняя запятая в
# CREATE TABLE broken). Инструмент обязан вернуть ненулевой код, а таблица
# broken не должна появиться. Инструмент, который на этом «успешен», к
# остальным замерам не допускается: его «ok» ничего не значит.
#
#   bash scripts/control.sh   → fixtures/00-control.txt

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=00-control
fixture_header "$f"
bad=0
for t in $TOOLS; do
    q pg postgres "DROP DATABASE IF EXISTS t_ctl_$t WITH (FORCE)" >/dev/null
    q pg postgres "CREATE DATABASE t_ctl_$t" >/dev/null
    U="$(url pg "t_ctl_$t")"; J="$(jdbc pg "t_ctl_$t")"
    case "$t" in
        goose)     c="docker run --rm --network smig -v \"\$PWD/tools/goose/invalid:/mig\" smig-goose -dir /mig postgres '$U' up" ;;
        flyway)    c="docker run --rm --network smig -v \"\$PWD/tools/flyway/invalid:/flyway/sql\" flyway/flyway:13.9.0 -url='$J' -user=postgres -password='smig' migrate" ;;
        liquibase) c="docker run --rm --network smig -e LIQUIBASE_ANALYTICS_ENABLED=false -v \"\$PWD/tools/liquibase:/liquibase/changelog\" smig-liquibase --url='$J' --username=postgres --password='smig' --search-path=/liquibase/changelog --changelog-file=invalid.sql update" ;;
        atlas)     c="docker run --rm --network smig -v \"\$PWD/tools/atlas/invalid:/mig\" arigaio/atlas:1.3.3-community migrate apply --url '$U&search_path=public' --dir file:///mig" ;;
        alembic)   c="docker run --rm --network smig -e SMIG_URL='$(sa_url pg "t_ctl_$t")' -v \"\$PWD/tools/alembic:/w\" -v \"\$PWD/tools/alembic/invalid:/w/versions\" smig-alembic upgrade head" ;;
    esac
    capture "$f" "$t: невалидная миграция" "$c"
    if [ "$CAPTURE_RC" != 0 ] && ! table_exists pg "t_ctl_$t" broken; then
        echo "# $t: отказ распознан — код $CAPTURE_RC, таблицы broken нет" >> "$FIXTURES_DIR/$f.txt"
        log "$t: контроль пройден"
    else
        echo "# $t: КОНТРОЛЬ НЕ ПРОЙДЕН — код $CAPTURE_RC" >> "$FIXTURES_DIR/$f.txt"
        log "$t: КОНТРОЛЬ НЕ ПРОЙДЕН"; bad=1
    fi
done
[ "$bad" = 0 ] || fail "есть инструменты, не различающие успех и отказ — их результаты не публикуются"
log "все инструменты различают успех и отказ"
