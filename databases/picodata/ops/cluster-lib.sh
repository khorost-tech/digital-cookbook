#!/usr/bin/env bash
# cluster-lib.sh — общие проверки состояния кластера. Подключается через
# `source`, самостоятельно не запускается.
#
# Зачем отдельным файлом: барьер готовности нужен и при подъёме стенда, и в
# предусловиях сценариев, и в их уборке. Три копии одной логики разъехались бы,
# а расхождение здесь означало бы, что часть прогонов измеряет переходное
# состояние кластера и об этом никто не узнает.
#
# ГЛАВНЫЙ УРОК, ради которого файл появился: «4 инстанса Online + один
# Raft-лидер + weight=1 ready» НЕ означает, что кластер готов к работе.
# Наблюдался прогон, где при всех этих признаках все 3000 бакетов лежали на
# ОДНОМ репликасете (3000/0), и туда же уехали все 200 000 строк. Rebalancer
# в журнале сообщил, что на момент bootstrap увидел ноль активных бакетов, и
# остановился. Загружать данные в таком состоянии — значит измерять гонку
# инициализации, а не свойства системы.

PICO_DSN_DEFAULT="postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable"
# Общее число бакетов кластера (дефолт vshard в Picodata). Сумма по уникальным
# репликасетам обязана совпасть с этим числом.
CL_TOTAL_BUCKETS="${CL_TOTAL_BUCKETS:-3000}"

_psql() { MSYS_NO_PATHCONV=1 docker exec picodata-origin psql "${PICO_DSN:-$PICO_DSN_DEFAULT}" "$@" 2>&1 | tr -d '\r'; }

_admin() { # <контейнер> — исполняет lua/sql из stdin
    MSYS_NO_PATHCONV=1 docker exec -i "$1" picodata admin /var/lib/picodata/admin.sock 2>/dev/null
}

# Все геттеры ниже возвращают ПУСТУЮ СТРОКУ, если ответа нет, и всегда код 0.
# Это важно вдвойне: во-первых, вызывающий обязан отличать «нет ответа» от нуля;
# во-вторых, библиотека подключается в скрипты с `set -e`, и ненулевой код от
# внутреннего grep убивал бы вызывающий скрипт. Так и случилось: на момент
# барьера таблицы products ещё нет (её создаёт миграция плагина), grep не
# находил строк — и up.sh падал на диагностической печати.

# Число активных бакетов на узле. Пустая строка = узел не ответил (это НЕ ноль).
cl_buckets_of() {
    _admin "$1" <<'EOF' | grep "active:" | tr -d '[:space:]' | sed 's/active://' || true
\lua
require('vshard').storage.info().bucket
EOF
}

# Строк в products на узле. Пусто, если таблицы ещё нет или узел не ответил.
cl_rows_of() {
    _admin "$1" <<'EOF' | grep -E "^- [0-9]+" | tr -d '[:space:]-' || true
\lua
box.space.products:len()
EOF
}

# Имя репликасета, которому принадлежит узел.
cl_replicaset_of() {
    _admin "$1" <<'EOF' | grep -E '^\s+replicaset_name:' | awk '{print $2}' | tr -d '[:space:]' || true
\lua
pico.instance_info()
EOF
}

# Сколько из перечисленных контейнеров считают себя Raft-лидером.
cl_raft_leaders() {
    local n=0 c role
    for c in "$@"; do
        docker ps --format '{{.Names}}' | grep -qx "$c" || continue
        role="$(_admin "$c" <<'EOF' | grep -E '^\s*raft_state:' | awk '{print $2}'
\lua
pico.raft_status()
EOF
)"
        [ "$role" = "Leader" ] && n=$(( n + 1 ))
    done
    echo "$n"
}

cl_instances() { _psql -tAc "SELECT name, replicaset_name, current_state FROM _pico_instance" || true; }

# --- БАРЬЕР ГОТОВНОСТИ -----------------------------------------------------
# Ждёт, пока кластер станет пригоден для эксперимента, и падает, если не
# дождался. Проверяет ЧЕТЫРЕ условия, а не два:
#   1) все инстансы Online;
#   2) ровно один Raft-лидер;
#   3) все репликасеты ready с ненулевым весом;
#   4) бакеты РАСПРЕДЕЛЕНЫ между репликасетами — ни один не пустой, и сумма
#      сходится с общим числом бакетов кластера.
# Четвёртое — то, чего не хватало: без него стенд принимал раскладку 3000/0.
#
# ОЖИДАЕМОЕ ЧИСЛО РЕПЛИКАСЕТОВ передаётся явно. Без него барьер пропустил бы
# вырожденную топологию: один репликасет из четырёх узлов, у каждого все 3000
# бакетов, сумма по уникальным репликасетам тоже 3000 и строк 200 000 — все
# внутренние сверки сходятся, а шардирования при этом нет вовсе.
#
# usage: cl_wait_ready <инстансов> <репликасетов> <таймаут, с> <контейнер...>
cl_wait_ready() {
    local want_inst="$1" want_rs="$2" timeout="$3"; shift 3
    local nodes=("$@")
    local deadline=$(( $(date +%s) + timeout ))
    local why=""

    while [ "$(date +%s)" -lt "$deadline" ]; do
        why=""
        local inst online total
        inst="$(cl_instances)"
        total="$(echo "$inst" | grep -c '|')"
        online="$(echo "$inst" | grep -c 'Online')"
        if [ "$total" -ne "$want_inst" ] || [ "$online" -ne "$want_inst" ]; then
            why="инстансов ${total}, Online ${online}, ожидалось ${want_inst}"
            sleep 5; continue
        fi

        local leaders; leaders="$(cl_raft_leaders "${nodes[@]}")"
        if [ "$leaders" -ne 1 ]; then
            why="Raft-лидеров ${leaders}, ожидался 1"
            sleep 5; continue
        fi

        # Репликасеты: каждый должен быть ready И иметь НЕНУЛЕВОЙ вес.
        # Проверять «строка содержит ready» недостаточно: при weight=0 состояние
        # тоже может содержать это слово, а репликасет с нулевым весом бакетов
        # не получает — ровно тот случай, ради которого барьер и написан.
        local rs_raw rs_bad=0 rs_count=0 rs_name rs_weight rs_state
        rs_raw="$(_admin "${nodes[0]}" <<'EOF' | grep -E '^\| default_'
\sql
SELECT name, weight, state FROM _pico_replicaset;
EOF
)"
        if [ -z "$rs_raw" ]; then
            why="не удалось прочитать _pico_replicaset"
            sleep 5; continue
        fi
        while IFS= read -r line; do
            [ -n "$line" ] || continue
            rs_name="$(echo "$line"   | awk -F'|' '{print $2}' | tr -d '[:space:]')"
            rs_weight="$(echo "$line" | awk -F'|' '{print $3}' | tr -d '[:space:]')"
            rs_state="$(echo "$line"  | awk -F'|' '{print $4}' | tr -d '[:space:]')"
            rs_count=$(( rs_count + 1 ))
            [ "$rs_state" = "ready" ] || { rs_bad=1; why="репликасет ${rs_name} в состоянии ${rs_state}"; }
            case "$rs_weight" in
                ''|0|0.0) rs_bad=1; why="репликасет ${rs_name} с весом ${rs_weight:-пусто}" ;;
            esac
        done <<< "$rs_raw"
        if [ "$rs_bad" -ne 0 ]; then sleep 5; continue; fi

        # Бакеты: строим карту «репликасет -> число бакетов» и проверяем, что
        # реплики одного репликасета согласованы, а сумма по УНИКАЛЬНЫМ
        # репликасетам равна общему числу бакетов кластера. Без этого барьер
        # пропустил бы раскладку вида 2999/1 или расхождение между репликами.
        local unknown=0 b rs
        declare -A rs_buckets=()
        for c in "${nodes[@]}"; do
            b="$(cl_buckets_of "$c")"; rs="$(cl_replicaset_of "$c")"
            if [ -z "$b" ] || [ -z "$rs" ]; then unknown=$(( unknown + 1 )); continue; fi
            if [ -n "${rs_buckets[$rs]:-}" ] && [ "${rs_buckets[$rs]}" != "$b" ]; then
                rs_bad=1; why="реплики ${rs} расходятся по бакетам: ${rs_buckets[$rs]} vs ${b}"
            fi
            rs_buckets[$rs]="$b"
        done
        if [ "$unknown" -ne 0 ]; then
            why="узлов без ответа: ${unknown}"
            sleep 5; continue
        fi
        if [ "$rs_bad" -ne 0 ]; then sleep 5; continue; fi

        # Число репликасетов должно совпасть и с _pico_replicaset, и с ОЖИДАЕМЫМ.
        # Вторая сверка не даёт принять вырожденную топологию, где все узлы
        # попали в один репликасет: внутренне такая раскладка непротиворечива.
        if [ "${#rs_buckets[@]}" -ne "$rs_count" ]; then
            why="репликасетов в _pico_replicaset ${rs_count}, а по узлам ${#rs_buckets[@]}"
            sleep 5; continue
        fi
        if [ "$rs_count" -ne "$want_rs" ]; then
            why="репликасетов ${rs_count}, ожидалось ${want_rs}"
            sleep 5; continue
        fi

        local sum=0 zero=0
        for rs in "${!rs_buckets[@]}"; do
            sum=$(( sum + rs_buckets[$rs] ))
            [ "${rs_buckets[$rs]}" -eq 0 ] 2>/dev/null && zero=$(( zero + 1 ))
        done
        if [ "$zero" -ne 0 ] || [ "$sum" -ne "$CL_TOTAL_BUCKETS" ]; then
            why="сумма бакетов по репликасетам ${sum}, ожидалось ${CL_TOTAL_BUCKETS}; пустых репликасетов ${zero}"
            sleep 5; continue
        fi

        return 0
    done

    echo "!!! кластер не пришёл в рабочее состояние за ${timeout} с: ${why}" >&2
    cl_dump_state "${nodes[@]}" >&2
    return 1
}

# Печатает текущую раскладку — для диагностики и для отчётов сценариев.
cl_dump_state() {
    local c b r rs
    for c in "$@"; do
        docker ps --format '{{.Names}}' | grep -qx "$c" || { printf "  %-6s (контейнер не запущен)\n" "$c"; continue; }
        b="$(cl_buckets_of "$c")"; r="$(cl_rows_of "$c")"; rs="$(cl_replicaset_of "$c")"
        printf "  %-6s репликасет %-10s бакетов: %-6s строк: %s\n" "$c" "${rs:-?}" "${b:-—}" "${r:-—}"
    done
}

# --- ПРОВЕРКА СОГЛАСОВАННОСТИ ПОСЛЕ ЗАГРУЗКИ -------------------------------
# Данные считаются корректно загруженными, если:
#   а) count(*) равен ожидаемому;
#   б) реплики ОДНОГО репликасета содержат одинаковое число строк;
#   в) сумма строк по УНИКАЛЬНЫМ репликасетам равна ожидаемому — иначе часть
#      данных не разъехалась либо посчитана дважды.
# usage: cl_check_data <ожидаемое число строк> <контейнер...>
cl_check_data() {
    local expected="$1"; shift
    local nodes=("$@")
    local rc=0

    local got; got="$(_psql -tAc 'SELECT count(*) FROM products' | tr -d '[:space:]')"
    if [ "$got" != "$expected" ]; then
        echo "!!! count(*) = ${got}, ожидалось ${expected}" >&2
        rc=1
    fi

    # Карта «репликасет -> строки» + проверка совпадения реплик внутри него.
    declare -A rs_rows=()
    local c rs r
    for c in "${nodes[@]}"; do
        docker ps --format '{{.Names}}' | grep -qx "$c" || continue
        rs="$(cl_replicaset_of "$c")"; r="$(cl_rows_of "$c")"
        [ -n "$rs" ] && [ -n "$r" ] || { echo "!!! ${c} не ответил (репликасет '${rs}', строк '${r}')" >&2; rc=1; continue; }
        if [ -n "${rs_rows[$rs]:-}" ] && [ "${rs_rows[$rs]}" != "$r" ]; then
            echo "!!! реплики репликасета ${rs} расходятся: ${rs_rows[$rs]} vs ${r}" >&2
            rc=1
        fi
        rs_rows[$rs]="$r"
    done

    local sum=0
    for rs in "${!rs_rows[@]}"; do sum=$(( sum + rs_rows[$rs] )); done
    if [ "$sum" != "$expected" ]; then
        echo "!!! сумма строк по уникальным репликасетам ${sum}, ожидалось ${expected}" >&2
        rc=1
    fi

    return "$rc"
}
