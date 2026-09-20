#!/usr/bin/env bash
# up.sh — поднимает стенд с нуля: источник истины, кластер, датасет, сборка и
# установка плагина. Идемпотентен только частично: кластер поднимается чистым
# (instance-dir живёт внутри контейнера, а не на томе), поэтому пароль
# администратора задаётся заново на каждом подъёме.
set -euo pipefail
STAND_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$STAND_DIR"
# shellcheck source=ops/cluster-lib.sh
source "$STAND_DIR/ops/cluster-lib.sh"
NODES=(pd-1 pd-2 pd-3 pd-4)
EXPECTED_ROWS="${EXPECTED_ROWS:-200000}"

export ORIGIN_DSN="${ORIGIN_DSN:-postgres://picodata:picodata@127.0.0.1:5434/catalog?sslmode=disable}"
VERSION="${1:-0.1.0}"

echo "=== источник истины ==="
docker compose -f compose/origin.yml up -d
sleep 3

if [ "${SKIP_DATASET:-0}" = "1" ]; then
    echo "=== датасет: пропущен (SKIP_DATASET=1) ==="
else
    echo "=== датасет (seed=42, 200 000 товаров) ==="
    ( cd dataset && go run . -seed=42 -products=200000 -load )
fi

echo "=== кластер ==="
docker compose -f compose/cluster.yml up -d
sleep 10

echo "=== пароль администратора ==="
# Picodata требует хотя бы одну заглавную букву в пароле: 'picodata' отвергается
# с сообщением "password should contain at least one uppercase letter".
MSYS_NO_PATHCONV=1 docker exec -i pd-1 picodata admin /var/lib/picodata/admin.sock <<'EOF' >/dev/null
\sql
ALTER USER "admin" PASSWORD 'Picodata1';
EOF

# Дефолтный лимит опкодов VDBE (45 000) не рассчитан на сортировку по десяткам
# тысяч строк: запрос топа по категории tools (33 276 товаров) падает с
# "Reached a limit on max executed vdbe opcodes. Limit: 45000". Причём падает
# одинаково и у внешнего SQL-клиента, и у плагина — распределённую часть
# планирует sbroad, а локальную исполняет VDBE ядра, и лимит считается там.
# БАРЬЕР ГОТОВНОСТИ — всё дальнейшее (плагин, миграции, данные) выполняется
# только на кластере, завершившем инициализацию.
#
# Фиксированной паузы после `compose up` недостаточно: наблюдался прогон, где
# при четырёх Online, одном Raft-лидере и обоих репликасетах в состоянии ready
# ВСЕ 3000 бакетов лежали на ОДНОМ репликасете (3000/0), и туда же уехали все
# 200 000 строк. Rebalancer сообщил, что на момент bootstrap увидел ноль
# активных бакетов, и остановился. Эксперимент в таком состоянии измеряет гонку
# инициализации, а не поведение системы, — и именно так в стенд попадали числа,
# которые легко принять за свойство Picodata.
echo "=== барьер: ждём завершения инициализации кластера ==="
cl_wait_ready 4 2 180 "${NODES[@]}"
echo "  раскладка на момент готовности:"
cl_dump_state "${NODES[@]}"

echo "=== кластерный лимит опкодов VDBE ==="
MSYS_NO_PATHCONV=1 docker exec picodata-origin psql \
    "postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable" \
    -c "ALTER SYSTEM SET sql_vdbe_opcode_max = 5000000 FOR ALL TIERS"

echo "=== сборка плагина ==="
bash builder/build.sh "$VERSION"

echo "=== сверка версий ==="
bash verify/versions.sh

echo "=== установка плагина ==="
bash ops/install-plugin.sh "$VERSION"

# Таблицу products создаёт МИГРАЦИЯ плагина, поэтому наполнять её можно только
# после установки. Загрузчик ходит в обе базы одним и тем же клиентом pgx.
echo "=== загрузка данных в кластер ==="
( cd loader && go run . \
    -origin "$ORIGIN_DSN" \
    -pico "postgres://admin:Picodata1@127.0.0.1:5442/picodata?sslmode=disable" )

echo "=== проверка согласованности после загрузки ==="
cl_check_data "$EXPECTED_ROWS" "${NODES[@]}"
cl_dump_state "${NODES[@]}"

echo
echo "стенд поднят. Контракт подключения:"
echo "  ORIGIN_DSN=$ORIGIN_DSN"
echo "  PICO_DSN=postgres://admin:Picodata1@127.0.0.1:5442/picodata?sslmode=disable"
echo "  PICO_HTTP=http://127.0.0.1:8081"
