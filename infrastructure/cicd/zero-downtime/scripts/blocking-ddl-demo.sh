#!/usr/bin/env bash
# Раздел zero-downtime, уровень A. Блокирующий DDL против CONCURRENTLY.
#
# CREATE INDEX берёт на таблицу блокировку SHARE, которая конфликтует с
# ROW EXCLUSIVE любой записи: пока индекс строится, все INSERT/UPDATE ЖДУТ.
# На большой таблице это простой записи на всё время построения. CREATE INDEX
# CONCURRENTLY строит индекс, не мешая записям, — ценой более долгой и сложной
# операции.
#
# Меряем время, которое ждёт ОДНА запись, стартовавшая во время построения
# индекса, и число ожиданий блокировок в логе PostgreSQL (дельта за прогон).
# Падающий вариант — сам блокирующий CREATE INDEX: запись встаёт.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

require_pg

# Отдельный крупный сид: на 5000 строках индекс строится за миллисекунды и лок
# незаметен. Здесь нужна таблица, где построение занимает ощутимое время.
echo "=== подготовка: таблица orders на 800000 строк (чтобы построение индекса заняло время) ==="
docker exec "$CONTAINER" psql -U zdt -d zdt -q -c "DROP TABLE IF EXISTS orders CASCADE"
docker exec "$CONTAINER" psql -U zdt -d zdt -q -c "CREATE TABLE orders (id bigserial PRIMARY KEY, customer text NOT NULL, amount_cents integer NOT NULL, created_at timestamptz DEFAULT now())"
docker exec "$CONTAINER" psql -U zdt -d zdt -q -c "INSERT INTO orders (customer, amount_cents) SELECT 'customer-' || (g % 1000), (g * 7) % 100000 FROM generate_series(1, 800000) AS g"

# Счётчик ожиданий блокировок из лога (дельта, а не сумма с начала контейнера).
count_lock_waits() { docker logs "$CONTAINER" 2>&1 | grep -ci "still waiting for" || true; }

# Фоновые psql-построители индекса живут ВНУТРИ контейнера (docker exec -d) —
# при прерывании скрипта их надо снять явно, иначе индекс достроится и лок
# провисит. Плюс дропаем оба индекса.
cleanup() {
    docker exec "$CONTAINER" psql -U zdt -d zdt -q -c \
        "SELECT pg_terminate_backend(pid) FROM pg_stat_activity
         WHERE datname='zdt' AND query LIKE 'CREATE INDEX%' AND pid <> pg_backend_pid()" >/dev/null 2>&1 || true
    docker exec "$CONTAINER" psql -U zdt -d zdt -q -c "DROP INDEX IF EXISTS idx_block" >/dev/null 2>&1 || true
    docker exec "$CONTAINER" psql -U zdt -d zdt -q -c "DROP INDEX IF EXISTS idx_conc" >/dev/null 2>&1 || true
}
trap cleanup EXIT

measure_insert() {
    local start end
    start=$(date +%s%3N)
    docker exec "$CONTAINER" psql -U zdt -d zdt -q -c \
        "INSERT INTO orders (customer, amount_cents) VALUES ('probe', 1)" >/dev/null
    end=$(date +%s%3N)
    echo $((end - start))
}

echo
echo "=== (0) КОНТРОЛЬ: та же запись БЕЗ всякого DDL ==="
# Без этого замера нельзя утверждать, сколько в числах варианта B — задержка от
# индекса, а сколько — накладные расходы самой оснастки (старт docker exec +
# psql). Контроль даёт нижнюю границу измерения.
BASE1=$(measure_insert); BASE2=$(measure_insert); BASE3=$(measure_insert)
BASELINE=$(( (BASE1 + BASE2 + BASE3) / 3 ))
echo "три контрольных INSERT: ${BASE1}, ${BASE2}, ${BASE3} мс -> среднее ${BASELINE} мс"
echo "(это пол измерения: столько стоит сама оснастка замера, без всякого DDL)"

echo
echo "=== (A) БЛОКИРУЮЩИЙ CREATE INDEX: запись во время построения ЖДЁТ ==="
BASE_A=$(count_lock_waits)
docker exec -d "$CONTAINER" psql -U zdt -d zdt -c "CREATE INDEX idx_block ON orders (customer)"
sleep 0.15   # дать построителю взять блокировку SHARE
WAIT_BLOCK=$(measure_insert)
echo "запись ждала: ${WAIT_BLOCK} мс (пока строился idx_block)"
# дождаться завершения построения, чтобы лог успел записать ожидание
sleep 1
AFTER_A=$(count_lock_waits)
WAITS_BLOCK=$((AFTER_A - BASE_A))

echo
echo "=== (B) CREATE INDEX CONCURRENTLY: запись НЕ ждёт ==="
BASE_B=$(count_lock_waits)
docker exec -d "$CONTAINER" psql -U zdt -d zdt -c "CREATE INDEX CONCURRENTLY idx_conc ON orders (customer)"
sleep 0.15
WAIT_CONC=$(measure_insert)
echo "запись ждала: ${WAIT_CONC} мс (idx_conc строится не мешая записям)"
sleep 2
AFTER_B=$(count_lock_waits)
WAITS_CONC=$((AFTER_B - BASE_B))

echo
echo "=== СВОДКА ==="
echo "контроль (без DDL):         запись ${BASELINE} мс — пол измерения"
echo "блокирующий CREATE INDEX:   запись ждала ${WAIT_BLOCK} мс, ожиданий блокировок в логе: ${WAITS_BLOCK}"
echo "CREATE INDEX CONCURRENTLY:  запись ждала ${WAIT_CONC} мс, ожиданий блокировок в логе: ${WAITS_CONC}"
echo
echo "Сколько из этого — собственно задержка от DDL (за вычетом пола оснастки):"
echo "  блокирующий:  $((WAIT_BLOCK - BASELINE)) мс сверх контроля"
echo "  CONCURRENTLY: $((WAIT_CONC - BASELINE)) мс сверх контроля"
echo
echo "ПАДАЮЩИЙ ВАРИАНТ — блокирующий CREATE INDEX (A): под ним запись простаивает"
echo "всё время построения индекса. CONCURRENTLY (B) снимает этот простой. Если бы"
echo "разницы не было, время ожидания записи совпадало бы между A и B."
echo
echo "⚠️ CONCURRENTLY не бесплатен: он не блокирует запись, но делает два прохода"
echo "по таблице, ждёт завершения старых транзакций, грузит систему дольше обычного"
echo "построения, а при сбое оставляет INVALID-индекс, который надо удалять вручную."

# Самопроверка: блокирующий должен задержать запись заметно дольше, чем CONCURRENTLY.
awk -v b="$WAIT_BLOCK" -v c="$WAIT_CONC" 'BEGIN{ exit !(b > c * 3 && b > 100) }' || {
    echo "ОШИБКА: блокирующий CREATE INDEX не дал заметной задержки против CONCURRENTLY" >&2
    echo "(ждал ${WAIT_BLOCK} мс против ${WAIT_CONC} мс) — контраст не воспроизвёлся." >&2
    exit 1
}
echo
echo "OK: блокирующий CREATE INDEX задержал запись на ${WAIT_BLOCK} мс против ${WAIT_CONC} мс у CONCURRENTLY."
