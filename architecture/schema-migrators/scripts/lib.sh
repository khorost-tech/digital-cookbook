#!/usr/bin/env bash
#
# lib.sh — общие функции стенда schema-migrators. Подключается через source.

NET=smig
TOOLS="goose flyway liquibase atlas alembic"
ENGINES="pg crdb pico"
FIXTURES_DIR="${FIXTURES_DIR:-fixtures}"
RESULTS="$FIXTURES_DIR/results.tsv"
PSQL_IMAGE=postgres:18.4

log()  { echo "[$(basename "${0%.sh}")] $*" >&2; }
fail() { echo "[$(basename "${0%.sh}")] ОШИБКА: $*" >&2; exit 1; }

# База под инструмент. У PostgreSQL и CockroachDB — своя на каждый инструмент,
# чтобы следы одного мигратора (журналы, блокировки) не влияли на другой.
# У Picodata база одна, поэтому её инстанс пересоздаётся (см. fresh).
dbname() { case "$1" in pico) echo picodata ;; *) echo "t_$2" ;; esac; }

# url <движок> <база> — URL в формате libpq.
url() {
    case "$1" in
        pg)   echo "postgres://postgres:smig@pg:5432/$2?sslmode=disable" ;;
        crdb) echo "postgres://root@crdb:26257/$2?sslmode=disable" ;;
        pico) echo "postgres://admin:Picodata1@pico:5432/$2?sslmode=disable" ;;
    esac
}
# jdbc <движок> <база>, juser, jpass — для Flyway и Liquibase.
jdbc() {
    case "$1" in
        pg)   echo "jdbc:postgresql://pg:5432/$2" ;;
        crdb) echo "jdbc:postgresql://crdb:26257/$2?sslmode=disable" ;;
        pico) echo "jdbc:postgresql://pico:5432/$2?sslmode=disable" ;;
    esac
}
juser() { case "$1" in pg) echo postgres ;; crdb) echo root ;; pico) echo admin ;; esac; }
jpass() { case "$1" in pg) echo smig ;; crdb) echo "" ;; pico) echo Picodata1 ;; esac; }
# sa_url — URL SQLAlchemy для Alembic.
sa_url() { url "$1" "$2" | sed 's#^postgres://#postgresql+psycopg://#'; }

# q <движок> <база> <SQL> — одно значение без заголовков.
q() {
    docker run --rm --network "$NET" "$PSQL_IMAGE" psql "$(url "$1" "$2")" -tAc "$3" 2>&1
}

# Состояние базы проверяется SQL-запросом, а не по коду возврата мигратора:
# инструмент может сказать «готово», ничего не сделав, и наоборот.
# У Picodata information_schema нет, таблицы — в системном _pico_table.
table_exists() {  # <движок> <база> <таблица>
    local n
    if [ "$1" = pico ]; then
        n="$(q pico picodata "SELECT count(*) FROM _pico_table WHERE name = '$3'")"
    else
        n="$(q "$1" "$2" "SELECT count(*) FROM information_schema.tables WHERE lower(table_name) = '$3'")"
    fi
    [ "$n" = 1 ]
}
column_exists() {  # <движок> <база> <таблица> <колонка>
    if [ "$1" = pico ]; then
        q pico picodata "SELECT format FROM _pico_table WHERE name = '$3'" | grep -q "\"$4\""
    else
        [ "$(q "$1" "$2" "SELECT count(*) FROM information_schema.columns WHERE table_name = '$3' AND column_name = '$4'")" = 1 ]
    fi
}

# fresh <движок> <инструмент> — чистая база под инструмент.
fresh() {
    local e="$1" t="$2" db
    db="$(dbname "$e" "$t")"
    case "$e" in
    pg)
        q pg postgres "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null
        q pg postgres "DROP DATABASE IF EXISTS atlas_dev WITH (FORCE)" >/dev/null
        q pg postgres "CREATE DATABASE $db" >/dev/null
        q pg postgres "CREATE DATABASE atlas_dev" >/dev/null ;;
    crdb)
        q crdb defaultdb "DROP DATABASE IF EXISTS $db CASCADE" >/dev/null
        q crdb defaultdb "DROP DATABASE IF EXISTS atlas_dev CASCADE" >/dev/null
        q crdb defaultdb "CREATE DATABASE $db" >/dev/null
        q crdb defaultdb "CREATE DATABASE atlas_dev" >/dev/null ;;
    pico)
        docker compose -f compose/pico.yml down -v >/dev/null 2>&1
        docker compose -f compose/pico.yml up -d >/dev/null 2>&1
        wait_pico ;;
    esac
}

# Барьер готовности Picodata: инстанс Online и принимает SQL по PG-протоколу.
# Пароль admin задаётся через admin-сокет; без заглавной буквы Picodata его
# отвергает («password should contain at least one uppercase letter»).
wait_pico() {
    local i
    for i in $(seq 1 60); do
        if docker exec -i smig-pico picodata admin /var/lib/picodata/admin.sock >/dev/null 2>&1 <<'EOF'
\sql
ALTER USER "admin" PASSWORD 'Picodata1';
EOF
        then
            [ "$(q pico picodata 'SELECT 1' | tail -1)" = 1 ] && return 0
        fi
        sleep 2
    done
    fail "Picodata не стала доступна по PG-протоколу за 120 с"
}

# capture <файл> <заголовок> <команда> — выполнить, показать, дописать секцию
# в fixtures/<файл>.txt. Код возврата команды — в CAPTURE_RC, вывод — в CAPTURE_OUT.
capture() {
    local file="$FIXTURES_DIR/$1.txt" title="$2" cmd="$3"
    mkdir -p "$FIXTURES_DIR"
    CAPTURE_OUT="$(bash -c "$cmd" 2>&1)"; CAPTURE_RC=$?
    {
        echo "--- $title ---"
        echo "# cmd: $cmd"
        printf '%s\n' "$CAPTURE_OUT"
        echo "# код возврата: $CAPTURE_RC"
        echo
    } >> "$file"
    printf '\n--- %s ---\n$ %s\n%s\n# код возврата: %s\n' "$title" "$cmd" "$CAPTURE_OUT" "$CAPTURE_RC" >&2
}

fixture_header() {
    mkdir -p "$FIXTURES_DIR"
    {
        echo "# $1"
        echo "# снято: $(date -u '+%Y-%m-%d %H:%M UTC'), хост: $(uname -sm), Docker $(docker version --format '{{.Server.Version}}')"
        echo
    } > "$FIXTURES_DIR/$1.txt"
}

# Строка с причиной ошибки — для таблицы результатов. Заголовки вида
# «Exception Details» и строки кода из трассировки пропускаются. У Python
# причина — в последней строке трассировки, у остальных — в первой подходящей.
err_line() {
    local cand
    cand="$(printf '%s\n' "$1" \
        | grep -iE 'error|fatal|exception|failed|not supported|unsupported|could not|unable to' \
        | grep -vE '^[[:space:]]|Exception Details|Exception Primary (Class|Source)|For more information|^Traceback|Background on this error' )"
    if printf '%s' "$1" | grep -q '^Traceback'; then
        printf '%s\n' "$cand" | tail -1
    else
        printf '%s\n' "$cand" | head -1
    fi | tr '\t' ' ' | cut -c1-220
}
