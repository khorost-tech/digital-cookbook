#!/usr/bin/env bash
#
# colocation-demo.sh — артефакт 2: колокация против репартиционного join.
# Два join'а, отличающиеся РОВНО одной переменной — колоцированы ли
# распределённые таблицы по общему ключу шардирования:
#
#   А: orders JOIN customers USING (customer_id) — обе распределены по
#      customer_id и явно колоцированы (colocate_with => 'customers' в
#      up.sh, одна группа колокации).
#   Б: orders JOIN shipments USING (order_id) — shipments распределена по
#      order_id в ОТДЕЛЬНОЙ группе колокации (colocate_with => 'none' в
#      up.sh). Ключ join'а (order_id) не совпадает с ключом шардирования
#      orders (customer_id) — Citus не может сопоставить шарды напрямую.
#
# Главная измеряемая величина — вид плана EXPLAIN, а не время: у А каждая
# из 32 задач выполняет join ЛОКАЛЬНО на своём шарде (обычный SQL-join
# внутри текста задачи, без MapMergeJob); у Б Citus обязан перераспределить
# (репартиционировать) данные одной из таблиц по ключу join'а, и в плане
# это видно по узлам MapMergeJob (Map Task Count / Merge Task Count) —
# структурный признак, который не спутать с обычным сканированием.
#
# ФАКТ (установлен в Task 1 живьём, см. task-3-citus-brief.md): параметр
# citus.enable_repartition_joins по умолчанию ВЫКЛЮЧЕН. Запрос Б без явного
# включения параметра ОТКАЗЫВАЕТСЯ выполняться:
#   ERROR:  the query contains a join that requires repartitioning
#   HINT:  Set citus.enable_repartition_joins to on to enable repartitioning
# Это не дефект стенда, а часть содержания статьи: Citus не просто
# «медленно делает» репартиционный join, а сначала вообще отказывается его
# планировать, пока читатель явно не согласится на цену репартиции. Скрипт
# фиксирует этот отказ дословно как самостоятельный результат — ПЕРЕД тем,
# как включить параметр и показать, во что превращается план.
#
# Параметр включается СТРОГО на уровне сессии (SET, не ALTER SYSTEM/
# ALTER DATABASE): каждый вызов psql в этом скрипте — новое подключение
# (docker exec запускает новый процесс psql), поэтому SET из одного вызова
# физически не может просочиться в следующий. Стенд не остаётся изменённым
# для следующих артефактов.
#
# Время снимаем ТОЖЕ, но только как порядок величины. ВАЖНО: все три узла
# кластера живут на одном хосте в Docker — сетевой задержки между ними
# практически нет. На узлах в разных стойках/AZ объём пересылки и число
# удалённых взаимодействий для варианта Б вырастут; величина этого эффекта
# здесь НЕ ИЗМЕРЕНА, и направление изменения разрыва между А и Б не
# утверждается. Арифметика вида «строка × RTT» неверна: строки идут потоком,
# а не по одному round-trip на строку. Сравнивать Citus с обычным
# PostgreSQL по скорости здесь НЕЛЬЗЯ —
# в этом артефакте вообще нет запуска ванильного PostgreSQL для сравнения.
#
# Скрипт прогоняет сценарий ДВАЖДЫ и сверяет структурные величины (вид
# плана А, вид плана Б, дословный текст ошибки при выключенном параметре)
# между прогонами — они обязаны совпасть точно. Если оба плана оказались
# бы одного вида (падающий вариант — как выглядело бы, если бы колокация
# не влияла на выполнение) или расходятся между прогонами — скрипт
# объявляет провал демонстрации и завершается ненулевым кодом.
#
# Требует уже поднятого и наполненного стенда (bash scripts/up.sh).
#
# Запуск (из databases/citus/):
#   bash scripts/colocation-demo.sh

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

DB_USER=shard
DB_NAME=shard

log()  { echo "[colocation] $*" >&2; }
fail() { echo "[colocation] ОШИБКА: $*" >&2; exit 1; }

# --- Preflight ---------------------------------------------------------------

command -v docker >/dev/null 2>&1 || fail "docker не найден в PATH."
docker info >/dev/null 2>&1 || fail "docker недоступен (демон не отвечает)."

state="$(docker inspect --format '{{.State.Status}}' citus-coord 2>/dev/null || true)"
[ "$state" = "running" ] || fail "контейнер citus-coord не запущен. Сначала: bash scripts/up.sh"

psql_c() {
  docker exec -i citus-coord psql -X -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" "$@"
}

log "Preflight: проверяем распределение и колокацию customers/orders/shipments…"

for t in customers orders shipments; do
  n="$(psql_c -At -c "SELECT count(*) FROM pg_dist_partition WHERE logicalrelid = '${t}'::regclass;")"
  [ "$n" = "1" ] || fail "таблица ${t} не распределена (pg_dist_partition пуст для ${t}). Сначала: bash scripts/up.sh"
done

coloc_customers="$(psql_c -At -c "SELECT colocationid FROM pg_dist_partition WHERE logicalrelid = 'customers'::regclass;")"
coloc_orders="$(psql_c -At -c "SELECT colocationid FROM pg_dist_partition WHERE logicalrelid = 'orders'::regclass;")"
coloc_shipments="$(psql_c -At -c "SELECT colocationid FROM pg_dist_partition WHERE logicalrelid = 'shipments'::regclass;")"

[ -n "$coloc_customers" ] && [ -n "$coloc_orders" ] && [ -n "$coloc_shipments" ] \
  || fail "не удалось получить colocationid для одной из таблиц из pg_dist_partition."

[ "$coloc_customers" = "$coloc_orders" ] \
  || fail "customers (colocationid=$coloc_customers) и orders (colocationid=$coloc_orders) НЕ в одной группе колокации — сценарий А не покажет локальный join. Проверьте up.sh/наполнение."

[ "$coloc_shipments" != "$coloc_orders" ] \
  || fail "shipments оказалась в ОДНОЙ группе колокации с orders (colocationid=$coloc_shipments) — сценарий Б не потребует репартиции, демонстрация невозможна. Ожидали отдельную группу (colocate_with => 'none' в up.sh)."

log "customers/orders: colocationid=$coloc_orders (общая группа). shipments: colocationid=$coloc_shipments (отдельная группа)."

orders_n="$(psql_c -At -c "SELECT count(*) FROM orders;")"
[ "$orders_n" -ge 4000 ] || fail "ожидали не меньше 4000 строк в orders, нашли: $orders_n. Наполнение не на месте — bash scripts/up.sh"

customers_n="$(psql_c -At -c "SELECT count(*) FROM customers;")"
[ "$customers_n" -ge 200 ] || fail "ожидали не меньше 200 строк в customers, нашли: $customers_n."

shipments_n="$(psql_c -At -c "SELECT count(*) FROM shipments;")"
[ "$shipments_n" -ge 4000 ] || fail "ожидали не меньше 4000 строк в shipments, нашли: $shipments_n."

guc_default="$(psql_c -At -c "SHOW citus.enable_repartition_joins;")"
[ "$guc_default" = "off" ] || log "ВНИМАНИЕ: citus.enable_repartition_joins по умолчанию = '$guc_default', а не 'off' — расходится с фактом из Task 1. Продолжаем, но это важное расхождение (см. самопроверку ниже)."

log "orders=$orders_n customers=$customers_n shipments=$shipments_n. citus.enable_repartition_joins по умолчанию: $guc_default."

# --- Запросы -----------------------------------------------------------------

SQL_A="SELECT c.city, count(*) FROM orders o JOIN customers c USING (customer_id) GROUP BY c.city;"
SQL_B="SELECT s.carrier, count(*) FROM orders o JOIN shipments s USING (order_id) GROUP BY s.carrier;"

# run_explain_A/B <sql> — выполняет EXPLAIN (ANALYZE, VERBOSE) в обычной
# (дефолтной) сессии, печатает план целиком и заполняет глобальные
# TASK_COUNT / EXEC_MS / MAPMERGE_COUNT.
run_explain() {
  local sql="$1" plan
  plan="$(psql_c -c "EXPLAIN (ANALYZE, VERBOSE) $sql")"
  echo "$plan"

  TASK_COUNT="$(echo "$plan" | grep -oP 'Task Count: \K[0-9]+' | head -1 || true)"
  [ -n "$TASK_COUNT" ] || fail "не нашли 'Task Count' в плане для запроса: $sql — формат вывода EXPLAIN изменился?"

  EXEC_MS="$(echo "$plan" | grep -oP 'Execution Time: \K[0-9.]+' | tail -1 || true)"
  [ -n "$EXEC_MS" ] || fail "не нашли 'Execution Time' в плане для запроса: $sql — формат вывода EXPLAIN изменился?"

  MAPMERGE_COUNT="$(echo "$plan" | grep -c 'MapMergeJob' || true)"
}

# run_explain_repartition <sql> — то же самое, но с
# citus.enable_repartition_joins=on НА УРОВНЕ СЕССИИ ЭТОГО ОДНОГО psql-
# подключения (SET + EXPLAIN в одном вызове -c, одна простая query-message,
# одна сессия). Следующий вызов psql_c — уже новое подключение с параметром
# по умолчанию.
run_explain_repartition() {
  local sql="$1" plan
  plan="$(psql_c -c "SET citus.enable_repartition_joins = on; EXPLAIN (ANALYZE, VERBOSE) $sql")"
  echo "$plan"

  TASK_COUNT="$(echo "$plan" | grep -oP 'Task Count: \K[0-9]+' | head -1 || true)"
  [ -n "$TASK_COUNT" ] || fail "не нашли 'Task Count' в плане для запроса (с включённой репартицией): $sql"

  EXEC_MS="$(echo "$plan" | grep -oP 'Execution Time: \K[0-9.]+' | tail -1 || true)"
  [ -n "$EXEC_MS" ] || fail "не нашли 'Execution Time' в плане для запроса (с включённой репартицией): $sql"

  MAPMERGE_COUNT="$(echo "$plan" | grep -c 'MapMergeJob' || true)"
}

# run_explain_expect_fail <sql> — выполняет EXPLAIN в ОБЫЧНОЙ сессии (без
# SET), ожидая отказ планировщика. Не даёт `set -e` уронить скрипт: код
# возврата и вывод (stdout+stderr вперемешку, как их печатает psql)
# сохраняются в глобальные ERR_RC / ERR_TEXT для дальнейшей проверки.
run_explain_expect_fail() {
  local sql="$1"
  set +e
  ERR_TEXT="$(docker exec -i citus-coord psql -X -U "$DB_USER" -d "$DB_NAME" -c "EXPLAIN (ANALYZE, VERBOSE) $sql" 2>&1)"
  ERR_RC=$?
  set -e
}

EXPECTED_ERR="ERROR:  the query contains a join that requires repartitioning"

declare -a TC_A EXEC_A MM_A
declare -a ERR_RC_ARR ERR_TEXT_ARR
declare -a TC_B EXEC_B MM_B

for run in 1 2; do
  echo
  echo "================================================================"
  echo " Прогон $run / 2"
  echo "================================================================"

  echo
  echo "--- А: orders JOIN customers USING (customer_id) — колоцированы ---"
  run_explain "$SQL_A"
  TC_A[$run]="$TASK_COUNT"
  EXEC_A[$run]="$EXEC_MS"
  MM_A[$run]="$MAPMERGE_COUNT"
  log "Прогон $run, А: Task Count=${TC_A[$run]}, MapMergeJob в плане=${MM_A[$run]}, Execution Time=${EXEC_A[$run]} мс"

  echo
  echo "--- Б (по умолчанию, citus.enable_repartition_joins=off): orders JOIN shipments USING (order_id) — НЕ колоцированы ---"
  run_explain_expect_fail "$SQL_B"
  echo "$ERR_TEXT"
  ERR_RC_ARR[$run]="$ERR_RC"
  ERR_TEXT_ARR[$run]="$ERR_TEXT"
  log "Прогон $run, Б (по умолчанию): код возврата psql=${ERR_RC_ARR[$run]}"

  echo
  echo "--- Б (citus.enable_repartition_joins=on на уровне сессии): та же пара таблиц ---"
  run_explain_repartition "$SQL_B"
  TC_B[$run]="$TASK_COUNT"
  EXEC_B[$run]="$EXEC_MS"
  MM_B[$run]="$MAPMERGE_COUNT"
  log "Прогон $run, Б (репартиция включена): Task Count=${TC_B[$run]}, MapMergeJob в плане=${MM_B[$run]}, Execution Time=${EXEC_B[$run]} мс"
done

# --- Сводка ------------------------------------------------------------------

echo
echo "================================================================"
echo " Сводка"
echo "================================================================"
printf '%-10s %-52s %-12s %-16s %-14s\n' "Прогон" "Запрос" "Task Count" "MapMergeJob" "Execution ms"
printf '%-10s %-52s %-12s %-16s %-14s\n' "1" "А: orders+customers (колоцированы)"        "${TC_A[1]}" "${MM_A[1]}" "${EXEC_A[1]}"
printf '%-10s %-52s %-12s %-16s %-14s\n' "1" "Б: orders+shipments (репартиция, on)"      "${TC_B[1]}" "${MM_B[1]}" "${EXEC_B[1]}"
printf '%-10s %-52s %-12s %-16s %-14s\n' "2" "А: orders+customers (колоцированы)"        "${TC_A[2]}" "${MM_A[2]}" "${EXEC_A[2]}"
printf '%-10s %-52s %-12s %-16s %-14s\n' "2" "Б: orders+shipments (репартиция, on)"      "${TC_B[2]}" "${MM_B[2]}" "${EXEC_B[2]}"
echo
echo "Б по умолчанию (citus.enable_repartition_joins=off) в обоих прогонах"
echo "отказалась выполняться — см. дословный текст ошибки выше и в проверке ниже."

echo
echo "================================================================"
echo " Падающий вариант (что было бы, если бы колокация не влияла)"
echo "================================================================"
echo "Если бы Citus мог выполнить join А и join Б одинаково — без разницы,"
echo "колоцированы таблицы или нет — оба плана выглядели бы одинаково: либо"
echo "ни в одном из них не было бы узла MapMergeJob (репартиции никогда не"
echo "требуется), либо он был бы в обоих. То, что MapMergeJob присутствует"
echo "СТРОГО у Б и отсутствует у А — и есть демонстрируемое отличие; именно"
echo "это и проверяется ниже."

# --- Самопроверка: провал демонстрации = ненулевой код -----------------

echo
echo "================================================================"
echo " Самопроверка"
echo "================================================================"

ok=1

if [ "${TC_A[1]}" != "${TC_A[2]}" ]; then
  echo "[FAIL] Task Count для А разошёлся между прогонами: ${TC_A[1]} vs ${TC_A[2]} — структурная величина обязана совпасть точно."
  ok=0
fi
if [ "${MM_A[1]}" != "0" ] || [ "${MM_A[2]}" != "0" ]; then
  echo "[FAIL] В плане А (колоцированный join) нашёлся узел MapMergeJob (прогон 1: ${MM_A[1]}, прогон 2: ${MM_A[2]}) — колоцированный join не должен требовать репартиции."
  ok=0
fi

if [ "${ERR_RC_ARR[1]}" = "0" ] || [ "${ERR_RC_ARR[2]}" = "0" ]; then
  echo "[FAIL] Запрос Б без включения citus.enable_repartition_joins неожиданно выполнился успешно (код возврата 0) хотя бы в одном прогоне — расходится с фактом Task 1 (параметр по умолчанию выключен)."
  ok=0
fi
case "${ERR_TEXT_ARR[1]}" in
  *"$EXPECTED_ERR"*) : ;;
  *)
    echo "[FAIL] Текст ошибки в прогоне 1 не содержит ожидаемую строку «$EXPECTED_ERR». Получено: ${ERR_TEXT_ARR[1]}"
    ok=0
    ;;
esac
case "${ERR_TEXT_ARR[2]}" in
  *"$EXPECTED_ERR"*) : ;;
  *)
    echo "[FAIL] Текст ошибки в прогоне 2 не содержит ожидаемую строку «$EXPECTED_ERR». Получено: ${ERR_TEXT_ARR[2]}"
    ok=0
    ;;
esac
if [ "${ERR_TEXT_ARR[1]}" != "${ERR_TEXT_ARR[2]}" ]; then
  echo "[FAIL] Текст ошибки разошёлся между прогонами — структурная величина (сообщение планировщика) обязана совпасть дословно."
  echo "       Прогон 1: ${ERR_TEXT_ARR[1]}"
  echo "       Прогон 2: ${ERR_TEXT_ARR[2]}"
  ok=0
fi

if [ "${TC_B[1]}" != "${TC_B[2]}" ]; then
  echo "[FAIL] Task Count для Б (репартиция) разошёлся между прогонами: ${TC_B[1]} vs ${TC_B[2]} — структурная величина обязана совпасть точно."
  ok=0
fi
if [ "${MM_B[1]}" = "0" ] || [ "${MM_B[2]}" = "0" ]; then
  echo "[FAIL] В плане Б (репартиционный join, параметр включён) НЕ нашёлся узел MapMergeJob (прогон 1: ${MM_B[1]}, прогон 2: ${MM_B[2]}) — репартиционный join обязан репартиционировать данные."
  ok=0
fi

if [ "${MM_A[1]}" = "${MM_B[1]}" ]; then
  echo "[FAIL] Наличие MapMergeJob у А и Б совпало (${MM_A[1]} = ${MM_B[1]}) — это и есть падающий вариант: демонстрация не показывает разницы между колоцированным и репартиционным join."
  ok=0
fi

if [ "$guc_default" != "off" ]; then
  echo "[FAIL] citus.enable_repartition_joins по умолчанию оказался '$guc_default', а не 'off' — расходится с фактом Task 1. Само по себе это не ломает демонстрацию структурно (проверки выше это уже покрыли бы), но требует внимания в отчёте — не публиковать данные молча, разобраться в причине (версия Citus/конфиг)."
  ok=0
fi

if [ "$ok" != "1" ]; then
  fail "демонстрация провалена (см. [FAIL] выше). Числа не воспроизвели ожидаемую картину — не публиковать эти данные как есть, разбираться в причине (порт/схема/наполнение/версия Citus)."
fi

echo "[OK] Структурные величины воспроизводятся точно между прогонами:"
echo "     А (колоцированы): Task Count=${TC_A[1]}, MapMergeJob=0 — join выполняется локально на каждом шарде."
echo "     Б по умолчанию: отказ планировщика, дословно: «${EXPECTED_ERR}»."
echo "     Б с включённой репартицией: Task Count=${TC_B[1]}, MapMergeJob>0 — join требует переброски данных между воркерами."
echo
echo "Время — только порядок величины. Все узлы на одном хосте; на реальном"
echo "кластере добавятся сетевой объём и удалённые взаимодействия, но их"
echo "величина здесь НЕ измерена, и направление изменения разрыва Б/А"
echo "утверждать нельзя:"
echo "  А ~ ${EXEC_A[1]}/${EXEC_A[2]} мс, Б (репартиция) ~ ${EXEC_B[1]}/${EXEC_B[2]} мс."
echo
echo "citus.enable_repartition_joins включался ИСКЛЮЧИТЕЛЬНО через SET внутри"
echo "одного psql-подключения (одна простая query-message: SET + EXPLAIN)."
echo "Каждый вызов psql в этом скрипте — новое подключение, поэтому изменение"
echo "не переживает вызов и кластер не остаётся в изменённом состоянии для"
echo "следующих артефактов."

log "Готово."
