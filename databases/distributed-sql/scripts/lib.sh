#!/usr/bin/env bash
#
# lib.sh — общие функции стенда distributed-sql. Подключается через source
# из каждого скрипта. Сама по себе не выполняется.

ENGINES="crdb yb tidb ob pg mysql"
NET=dsql
BENCH_IMAGE=dsql-bench
NETEM_IMAGE=dsql-netem
FIXTURES_DIR="${FIXTURES_DIR:-fixtures}"

log()  { echo "[$(basename "${0%.sh}")] $*" >&2; }
fail() { echo "[$(basename "${0%.sh}")] ОШИБКА: $*" >&2; exit 1; }

compose_of() { echo "compose/$1.yml"; }

# Контейнеры, между которыми ходит внутренний трафик кластера. Именно на них
# netem.sh вешает задержку. Одиночные серверы в список не входят: задерживать
# у них нечего.
members() {
    case "$1" in
        crdb) echo "crdb1 crdb2 crdb3" ;;
        yb)   echo "yb1 yb2 yb3" ;;
        tidb) echo "pd1 tikv1 tikv2 tikv3 tidb" ;;
        ob)   echo "ob" ;;
        pg)   echo "pg" ;;
        mysql) echo "mysql" ;;
        *) fail "неизвестный движок '$1' (ожидается: $ENGINES)" ;;
    esac
}

# sql_cmd <движок> <SQL> — печатает команду, которой читатель выполнит SQL
# штатным клиентом движка. Именно эта строка попадает в фикстуру как `# cmd:`.
sql_cmd() {
    local e="$1" q="$2"
    case "$e" in
        crdb) printf 'docker exec crdb1 cockroach sql --insecure --host=localhost:26257 -e "%s"' "$q" ;;
        yb)   printf 'docker exec yb1 bin/ysqlsh -h yb1 -c "%s"' "$q" ;;
        tidb) printf 'docker run --rm --network dsql mysql:8.4 mysql -h tidb -P 4000 -u root -D test -t -e "%s"' "$q" ;;
        ob)   printf 'docker exec ob obclient -h127.0.0.1 -P2881 -uroot@test -A -t -e "%s"' "$q" ;;
        pg)   printf 'docker exec pg psql -U postgres -d bench -c "%s"' "$q" ;;
        mysql) printf 'docker exec mysql mysql -uroot -pbench bench -t -e "%s"' "$q" ;;
    esac
}

# sql <движок> <SQL> — выполнить и вывести результат.
sql() { bash -c "$(sql_cmd "$1" "$2")" 2>&1 | grep -v 'Using a password on the command line'; }

# bench_cmd <аргументы bench> — команда запуска инструмента замеров.
bench_cmd() { printf 'docker run --rm --network dsql %s %s' "$BENCH_IMAGE" "$*"; }

# capture <файл> <заголовок> <команда> — выполнить команду, показать вывод
# и дописать секцию в fixtures/<файл>.txt. Первая строка секции — `# cmd:`
# с командой ровно в том виде, в каком её можно скопировать в терминал.
capture() {
    local file="$FIXTURES_DIR/$1.txt" title="$2" cmd="$3" out
    mkdir -p "$FIXTURES_DIR"
    out="$(bash -c "$cmd" 2>&1 | grep -v 'Using a password on the command line')" || true
    {
        echo "--- $title ---"
        echo "# cmd: $cmd"
        printf '%s\n\n' "$out"
    } >> "$file"
    printf '\n--- %s ---\n$ %s\n%s\n' "$title" "$cmd" "$out"
}

# fixture_header <файл> <движок> — начать файл фикстуры заново.
fixture_header() {
    mkdir -p "$FIXTURES_DIR"
    {
        echo "# $1 — движок: $2"
        echo "# снято: $(date -u '+%Y-%m-%d %H:%M UTC'), хост: $(uname -sm), Docker $(docker version --format '{{.Server.Version}}')"
        echo
    } > "$FIXTURES_DIR/$1.txt"
}

# wait_until <секунд> <описание> <команда> — ждать, пока команда не вернёт 0.
wait_until() {
    local secs="$1" what="$2" i; shift 2
    for i in $(seq 1 "$secs"); do
        if bash -c "$*" >/dev/null 2>&1; then log "$what: готово за ${i}с"; return 0; fi
        sleep 1
    done
    fail "$what: не дождались за ${secs}с"
}

# ip_of <контейнер> — адрес контейнера в сети dsql.
ip_of() {
    docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" "$1"
}

# running <движок> — запущен ли профиль (все его контейнеры живы).
running() {
    local c
    for c in $(members "$1"); do
        [ "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null)" = true ] || return 1
    done
}

require_running() { running "$1" || fail "профиль $1 не запущен: bash scripts/up.sh $1"; }

# leaders_cmd <движок> — команда, показывающая лидеров четырёх диапазонов kv.
leaders_cmd() {
    case "$1" in
        crdb) sql_cmd crdb 'SELECT range_id, start_key, end_key, lease_holder, replicas FROM [SHOW RANGES FROM TABLE kv WITH DETAILS] ORDER BY start_key;' ;;
        yb)   echo "docker exec yb1 bin/yb-admin --master_addresses yb1:7100,yb2:7100,yb3:7100 list_tablets ysql.yugabyte kv" ;;
        tidb) sql_cmd tidb 'SHOW TABLE kv REGIONS;' ;;
        ob)   sql_cmd ob "SELECT partition_name, tablet_id, ls_id, svr_ip, role FROM oceanbase.DBA_OB_TABLE_LOCATIONS WHERE table_name = 'kv' ORDER BY partition_name;" ;;
    esac
}
