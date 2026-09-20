#!/usr/bin/env bash
# scale.sh — расширение кластера: что происходит, когда добавляешь узлы.
#
# Проверяется:
#   1) новые инстансы сами собираются в репликасет и получают вес — вручную
#      ничего регистрировать не нужно;
#   2) когда на них реально приезжают бакеты: наблюдаем окно WAIT_SECONDS и
#      фиксируем момент появления первого ненулевого значения. На кластере,
#      прошедшем барьер готовности, это происходило сразу.
#
# Узлы добавляются ПАРОЙ: при факторе репликации 2 одиночный инстанс образует
# неполный репликасет и не получит бакетов вообще.
#
# ВАЖНО ПРО ЧИСТОТУ СТЕНДА. Сценарий необратимо меняет топологию: остановленные
# контейнеры остаются в кластере записями со статусом Offline, а штатное
# исключение (`picodata expel`) требует UUID инстанса и учётных данных. Поэтому
# по завершении сценарий ПЕРЕСОЗДАЁТ кластер с нуля — иначе следующие сценарии
# (verify.sh, observability.sh) запускаются на грязной топологии и падают.
# Отключить пересоздание можно через KEEP_CLUSTER=1, но тогда убирать за собой
# придётся вручную.
set -uo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
STAND="$(cd "$(dirname "$0")/.." && pwd)"
# Windows-форма пути для аргумента `docker compose -f`: MSYS_NO_PATHCONV=1 нужен
# ниже для DSN в `docker exec psql`, но он же ЗАПРЕЩАЕТ конвертацию Unix-пути в
# Windows, и docker (нативный) получает "/g/7/..." и коверкает его в
# "G:\g\7\...". Из-за этого compose не находил файл, а узлы не поднимались —
# при том что та же команда из интерактивной оболочки работала. Отдельный
# путь в Windows-форме снимает конфликт.
STAND_WIN="$(cd "$(dirname "$0")/.." && (pwd -W 2>/dev/null || pwd))"
# shellcheck source=ops/cluster-lib.sh
source "$STAND/ops/cluster-lib.sh"
mkdir -p "$DIR/out"
export MSYS_NO_PATHCONV=1

# Вывод дублируется в файл через перенаправление, а НЕ через `{ ... } | tee`:
# в пайплайне тело блока идёт в субшелле, и `exit 1` из него не завершает
# скрипт. Тот же дефект уже ловился в failure.sh.
exec > >(tee "$DIR/out/scale.txt") 2>&1

DSN="postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable"
WAIT_SECONDS="${WAIT_SECONDS:-300}"
PLUGIN_VERSION="${PLUGIN_VERSION:-0.2.0}"
EXPECTED=200000
fail=0

instances() {
    docker exec picodata-origin psql "$DSN" -tAc \
        "SELECT name, replicaset_name, current_state FROM _pico_instance" 2>&1 | tr -d '\r'
}

# Сколько узлов сообщают о себе как о Raft-лидере. Здоровье управляющей плоскости
# нельзя выводить из Online: должен быть ровно ОДИН лидер.
raft_leaders() {
    local n=0 c
    for c in "$@"; do
        role="$(docker exec -i "$c" picodata admin /var/lib/picodata/admin.sock <<'EOF' 2>/dev/null | grep -E '^\s*raft_state:' | awk '{print $2}'
\lua
pico.raft_status()
EOF
)"
        [ "$role" = "Leader" ] && n=$(( n + 1 ))
    done
    echo "$n"
}

buckets_of() {
    docker exec -i "$1" picodata admin /var/lib/picodata/admin.sock <<'EOF' 2>/dev/null | grep "active:" | tr -d '[:space:]' | sed 's/active://'
\lua
require('vshard').storage.info().bucket
EOF
}

# --- предусловие -----------------------------------------------------------
# Сценарий имеет смысл только на исходной топологии: четыре инстанса, все
# Online. На гряз­ной топологии его выводы недействительны, поэтому выходим.
echo "=== предусловие: полный барьер готовности кластера ==="
# Не только Online и лидер: проверяются состояние репликасетов и РАСПРЕДЕЛЕНИЕ
# бакетов. Наблюдался кластер, где при Online и лидере все 3000 бакетов лежали
# на одном репликасете — сценарий расширения на таком состоянии измерял бы не
# ребалансировку, а незавершённую инициализацию.
if ! cl_wait_ready 4 2 120 pd-1 pd-2 pd-3 pd-4; then
    echo "    пересоздайте кластер: bash ops/up.sh ${PLUGIN_VERSION}" >&2
    exit 1
fi
if ! cl_check_data "$EXPECTED" pd-1 pd-2 pd-3 pd-4; then
    echo "!!! данные не в согласованном состоянии — прогон недействителен" >&2
    exit 1
fi
cl_dump_state pd-1 pd-2 pd-3 pd-4

    echo "=== 1. до расширения ==="
    for c in pd-1 pd-2; do printf "  %-6s бакетов: %s\n" "$c" "$(buckets_of $c)"; done

    echo
    echo "=== 2. добавляем пару инстансов ==="
    docker compose -f "$STAND_WIN/compose/scale.yml" up -d 2>&1 | tail -2

    echo
    echo "=== 3. ждём, пока новые инстансы РЕАЛЬНО поднимутся ==="
    # Ключевой момент, на котором прежняя версия давала ложный вывод: пока pd-5
    # ещё стартует, его admin-сокет молчит и buckets_of возвращает пустую
    # строку. Трактовать её как «ноль бакетов» нельзя — иначе «узел не готов»
    # выдаётся за «ребалансировка не началась». Сначала дожидаемся, что оба
    # новых инстанса видны кластеру как Online.
    up_deadline=$(( $(date +%s) + 240 ))
    while [ "$(date +%s)" -lt "$up_deadline" ]; do
        online6="$(instances | grep -c 'Online')"
        [ "$online6" -ge 6 ] && break
        sleep 5
    done
    instances | sed 's/^/  /'
    online6="$(instances | grep -c 'Online')"
    if [ "$online6" -lt 6 ]; then
        echo "!!! за 240 с не поднялись все 6 инстансов (Online: ${online6})" >&2
        fail=1
    fi
    echo "  веса репликасетов:"
    docker exec -i pd-1 picodata admin /var/lib/picodata/admin.sock <<'EOF' 2>/dev/null | grep -E "default_" | sed 's/^/    /'
\sql
SELECT name, weight, state FROM _pico_replicaset;
EOF

    echo
    echo "=== 4. поехали ли бакеты на новый репликасет ==="
    echo "  наблюдаем до ${WAIT_SECONDS} с (buckets_of отличает 'не отвечает' от нуля):"
    deadline=$(( $(date +%s) + WAIT_SECONDS ))
    moved=0
    first_seen=""
    while [ "$(date +%s)" -lt "$deadline" ]; do
        b5="$(buckets_of pd-5)"; b6="$(buckets_of pd-6)"
        # Пустая строка = узел не ответил; отделяем её от честного нуля.
        show5="${b5:-—}"; show6="${b6:-—}"
        printf "    %s pd-5: %s  pd-6: %s\n" "$(date +%H:%M:%S)" "$show5" "$show6"
        if [ -n "$b5" ] && [ "$b5" -gt 0 ] 2>/dev/null; then
            moved=1; first_seen="$(date +%H:%M:%S)"; break
        fi
        sleep 15
    done

    echo
    if [ "$moved" -eq 1 ]; then
        echo "  бакеты поехали на новый репликасет (первое ненулевое значение в ${first_seen})."
    else
        # Это ПРОВАЛ, а не просто наблюдение: статья утверждает, что на кластере,
        # прошедшем барьер, перераспределение начинается сразу. Если оно не
        # началось — расходятся стенд и текст, и сценарий обязан это показать.
        echo "  !!! за ${WAIT_SECONDS} с бакеты на pd-5 не появились — это расходится" >&2
        echo "      с утверждением статьи о немедленном перераспределении." >&2
        docker logs pd-1 2>&1 | grep -i "rebalanc" | tail -2 | sed 's/^/      /' >&2
        fail=1
    fi
    echo
    echo "  Трактовка: на кластере, прошедшем барьер готовности, бакеты уезжали"
    echo "  на новый репликасет СРАЗУ (ненулевое значение уже при первой пробе)."
    echo "  Прежние наблюдения «не двигались минутами» снимались с кластеров БЕЗ"
    echo "  барьера либо с неподнявшимися узлами (баг пути compose под MSYS) —"
    echo "  им доверять нельзя. Готовность узла определять по числу бакетов, а не"
    echo "  по статусу Online и не по выставленному весу репликасета."

    echo
    echo "=== 5. итоговое распределение бакетов и целостность ==="
    # Ожидаем ТРИ репликасета по 1000 бакетов; на узлах сумма вдвое больше, так
    # как каждый бакет лежит на двух репликах. Обе величины проверяются, а не
    # просто печатаются: иначе сценарий завершится успехом даже при перекошенном
    # или неполном распределении.
    EXPECTED_PER_NODE=1000
    EXPECTED_TOTAL=$(( CL_TOTAL_BUCKETS * 2 ))
    total_b=0
    for c in pd-1 pd-2 pd-3 pd-4 pd-5 pd-6; do
        b="$(buckets_of $c)"; b="${b:-0}"
        printf "  %-6s бакетов: %s\n" "$c" "$b"
        total_b=$(( total_b + b ))
        if [ "$b" -ne "$EXPECTED_PER_NODE" ]; then
            echo "  !!! на ${c} бакетов ${b}, ожидалось ${EXPECTED_PER_NODE}" >&2
            fail=1
        fi
    done
    echo "  сумма бакетов по узлам: ${total_b} (в кластере ${CL_TOTAL_BUCKETS}, каждый бакет на 2 репликах = ${EXPECTED_TOTAL})"
    if [ "$total_b" -ne "$EXPECTED_TOTAL" ]; then
        echo "  !!! сумма бакетов ${total_b}, ожидалось ${EXPECTED_TOTAL}" >&2
        fail=1
    fi
    got="$(docker exec picodata-origin psql "$DSN" -tAc 'SELECT count(*) FROM products' 2>&1 | tr -d '[:space:]')"
    echo "  count(*) = ${got}"
    if [ "$got" != "$EXPECTED" ]; then
        echo "  !!! целостность нарушена: ожидалось ${EXPECTED}" >&2
        fail=1
    fi

# --- уборка ----------------------------------------------------------------
echo
if [ "${KEEP_CLUSTER:-0}" = "1" ]; then
    echo "=== KEEP_CLUSTER=1: кластер оставлен грязным ==="
    echo "    в нём остались записи о добавленных инстансах; следующие сценарии"
    echo "    на нём запускать нельзя"
else
    echo "=== уборка: пересоздаём кластер ==="
    docker compose -f "$STAND_WIN/compose/scale.yml" down >/dev/null 2>&1
    docker compose -f "$STAND_WIN/compose/cluster.yml" down -v >/dev/null 2>&1
    SKIP_DATASET=1 bash "$STAND/ops/up.sh" "$PLUGIN_VERSION" >/dev/null 2>&1
    # Уборка проверяется тем же барьером, что и предусловие: Online и лидера
    # мало, нужны ещё распределение бакетов и согласованность данных — иначе
    # следующий сценарий унаследует неготовый кластер.
    if cl_wait_ready 4 2 120 pd-1 pd-2 pd-3 pd-4 && cl_check_data "$EXPECTED" pd-1 pd-2 pd-3 pd-4; then
        echo "  ok: кластер восстановлен и готов"
        cl_dump_state pd-1 pd-2 pd-3 pd-4
    else
        echo "!!! после пересоздания кластер не в рабочем состоянии" >&2
        exit 1
    fi
fi

if [ "$fail" -ne 0 ]; then
    echo "ПРОВАЛ: см. сообщения выше" >&2
    exit 1
fi
