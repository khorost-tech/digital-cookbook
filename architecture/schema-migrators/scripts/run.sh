#!/usr/bin/env bash
#
# run.sh <инструмент> <СУБД> — одна ячейка матрицы.
#
#   bash scripts/run.sh goose pico     → fixtures/goose-pico.txt + строки в results.tsv
#
# Шаги — одни и те же для всех инструментов:
#   apply1      применить первую миграцию (CREATE TABLE probe)
#   journal     появился ли собственный журнал версий инструмента
#   apply2      применить вторую (ALTER TABLE probe ADD COLUMN note)
#   introspect  прочитать схему штатной командой (только Atlas и Alembic):
#               alembic check — сравнение с моделью; atlas schema inspect —
#               инспекция: проверяется лишь, что найдены таблица и колонка
#   rollback    откатить вторую миграцию
#
# Исход шага определяется НЕ по коду возврата, а по паре «код возврата +
# фактическое состояние базы, проверенное SQL-запросом»:
#   ok            инструмент сказал «готово», и изменение есть
#   fail          инструмент сказал «ошибка», и изменения нет
#   false-ok      инструмент сказал «готово», а изменения нет
#   applied-err   инструмент сказал «ошибка», а изменение применено
#   edition       откат отклонён: функции нет в бесплатной редакции или открытой сборке
#   no-pre        откатывать или сравнивать нечего: вторая миграция не применилась
#   n/a           шаг к инструменту неприменим

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

tool="${1:-}"; e="${2:-}"
case " $TOOLS " in *" $tool "*) ;; *) fail "инструмент: $TOOLS" ;; esac
case " $ENGINES " in *" $e "*) ;; *) fail "СУБД: $ENGINES" ;; esac

db="$(dbname "$e" "$tool")"
f="$tool-$e"
# Имя собственной таблицы-журнала инструмента.
case "$tool" in
    goose) journal=goose_db_version ;; flyway) journal=flyway_schema_history ;;
    liquibase) journal=databasechangelog ;; atlas) journal=atlas_schema_revisions ;;
    alembic) journal=alembic_version ;;
esac
mkdir -p "$FIXTURES_DIR"
[ -f "$RESULTS" ] || printf 'tool\tengine\tstep\texit\tstate\tverdict\tmessage\n' > "$RESULTS"
# Строки этой ячейки от прежнего прогона убираем: результат — только последний.
awk -F'\t' -v t="$tool" -v en="$e" '!($1 == t && $2 == en)' "$RESULTS" > "$RESULTS.tmp" && mv "$RESULTS.tmp" "$RESULTS"

# cmd <действие> — команда инструмента ровно в том виде, в каком её можно
# скопировать в терминал (из корня стенда).
cmd() {
    local a="$1" U J
    U="$(url "$e" "$db")"; J="$(jdbc "$e" "$db")"
    case "$tool" in
    goose)
        local b="docker run --rm --network smig -v \"\$PWD/tools/goose/migrations:/mig\" smig-goose -dir /mig postgres '$U'"
        case "$a" in apply1) echo "$b up-to 1" ;; apply2) echo "$b up" ;; rollback) echo "$b down" ;; esac ;;
    flyway)
        local b="docker run --rm --network smig -v \"\$PWD/tools/flyway/migrations:/flyway/sql\" flyway/flyway:13.9.0 -url='$J' -user=$(juser "$e") -password='$(jpass "$e")'"
        case "$a" in apply1) echo "$b migrate -target=1" ;; apply2) echo "$b migrate" ;; rollback) echo "$b undo" ;; esac ;;
    liquibase)
        local b="docker run --rm --network smig -e LIQUIBASE_ANALYTICS_ENABLED=false -v \"\$PWD/tools/liquibase:/liquibase/changelog\" smig-liquibase --url='$J' --username=$(juser "$e") --password='$(jpass "$e")' --search-path=/liquibase/changelog --changelog-file=changelog.sql"
        case "$a" in apply1) echo "$b update-count --count=1" ;; apply2) echo "$b update" ;; rollback) echo "$b rollback-count --count=1" ;; esac ;;
    atlas)
        local AU="$U&search_path=public" DU
        DU="$(url "$e" atlas_dev)&search_path=public"
        [ "$e" = pico ] && DU="$AU"   # отдельной пустой базы у Picodata нет
        local b="docker run --rm --network smig -v \"\$PWD/tools/atlas/migrations:/mig\" arigaio/atlas:1.3.3-community"
        case "$a" in
            apply1) echo "$b migrate apply --url '$AU' --dir file:///mig 1" ;;
            apply2) echo "$b migrate apply --url '$AU' --dir file:///mig" ;;
            introspect) echo "$b schema inspect --url '$AU'" ;;
            rollback) echo "$b migrate down --url '$AU' --dir file:///mig --dev-url '$DU'" ;;
        esac ;;
    alembic)
        local b="docker run --rm --network smig -e SMIG_URL='$(sa_url "$e" "$db")' -v \"\$PWD/tools/alembic:/w\" smig-alembic"
        case "$a" in apply1) echo "$b upgrade 0001" ;; apply2) echo "$b upgrade head" ;; introspect) echo "$b check" ;; rollback) echo "$b downgrade -1" ;; esac ;;
    esac
}

# Признак того, что откат отклонён как платная функция. Шаблоны — из
# фактического вывода инструментов, а не из документации.
paid_pattern() {
    case "$tool" in
        flyway) echo 'Edition Required|not supported by OSS Edition' ;;
        atlas)  echo 'community build|not supported by the community version' ;;
        *)      echo 'NEVER-MATCHES-a1b2c3' ;;
    esac
}

record() {  # <шаг> <код> <состояние 0/1> [паттерн платного]
    local step="$1" rc="$2" st="$3" v msg
    if [ "$rc" = 0 ] && [ "$st" = 1 ]; then v=ok
    elif [ "$rc" != 0 ] && [ "$st" = 0 ]; then v=fail
    elif [ "$rc" = 0 ]; then v=false-ok
    else v=applied-err
    fi
    if [ "$step" = rollback ] && [ "$v" = fail ] && printf '%s' "$CAPTURE_OUT" | grep -qiE "$(paid_pattern)"; then
        v=edition
    fi
    msg="$(err_line "$CAPTURE_OUT")"
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$tool" "$e" "$step" "$rc" "$st" "$v" "$msg" >> "$RESULTS"
    log "$tool × $e: $step → $v"
}

fixture_header "$f"
log "чистая база под $tool на $e"
fresh "$e" "$tool"

capture "$f" "apply1: первая миграция (CREATE TABLE probe)" "$(cmd apply1)"
table_exists "$e" "$db" probe && st=1 || st=0
record apply1 "$CAPTURE_RC" "$st"

# Журнал — не отдельная команда, а побочный эффект первого применения.
table_exists "$e" "$db" "$journal" && jst=1 || jst=0
printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$tool" "$e" journal - "$jst" \
    "$([ "$jst" = 1 ] && echo ok || echo fail)" "таблица $journal" >> "$RESULTS"
echo "# журнал версий $journal существует: $([ "$jst" = 1 ] && echo да || echo нет)" >> "$FIXTURES_DIR/$f.txt"

capture "$f" "apply2: вторая миграция (ADD COLUMN note)" "$(cmd apply2)"
column_exists "$e" "$db" probe note && st=1 || st=0
record apply2 "$CAPTURE_RC" "$st"
applied2="$st"

case "$tool" in
atlas|alembic)
    capture "$f" "introspect: сверка схемы базы с ожидаемой" "$(cmd introspect)"
    if [ "$applied2" != 1 ]; then
        # Сравнивать не с чем: миграции не применились, ожидаемой схемы нет.
        printf '%s\t%s\tintrospect\t%s\t-\tno-pre\t%s\n' "$tool" "$e" "$CAPTURE_RC" "$(err_line "$CAPTURE_OUT")" >> "$RESULTS"
        log "$tool × $e: introspect → no-pre"
    else
    if [ "$tool" = alembic ]; then
        printf '%s' "$CAPTURE_OUT" | grep -q 'No new upgrade operations detected' && st=1 || st=0
    else
        printf '%s' "$CAPTURE_OUT" | grep -q 'probe' && printf '%s' "$CAPTURE_OUT" | grep -q 'note' && st=1 || st=0
    fi
    record introspect "$CAPTURE_RC" "$st"
    fi ;;
*)
    printf '%s\t%s\tintrospect\t-\t-\tn/a\t\n' "$tool" "$e" >> "$RESULTS" ;;
esac

capture "$f" "rollback: откат второй миграции" "$(cmd rollback)"
# Откат удался, если колонки больше нет, а таблица осталась.
if [ "$applied2" != 1 ]; then
    # Без применённой второй миграции «колонки нет» — не заслуга отката.
    printf '%s\t%s\trollback\t%s\t-\tno-pre\t%s\n' "$tool" "$e" "$CAPTURE_RC" "$(err_line "$CAPTURE_OUT")" >> "$RESULTS"
    log "$tool × $e: rollback → no-pre"
else
    if table_exists "$e" "$db" probe && ! column_exists "$e" "$db" probe note; then st=1; else st=0; fi
    record rollback "$CAPTURE_RC" "$st"
fi

log "записано: $FIXTURES_DIR/$f.txt"
