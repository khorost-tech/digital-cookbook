#!/usr/bin/env bash
#
# reference-demo.sh — артефакт 3: референсная таблица против распределённой
# копии того же справочника. Один и тот же маленький справочник в двух
# ролях, отличающихся РОВНО одной переменной — способом распределения:
#
#   А: carriers — референсная таблица (create_reference_table в up.sh),
#      копия лежит целиком на каждом ВОРКЕРЕ (координатор — тоже узел
#      кластера, но размещений там нет: их ровно по числу воркеров).
#   Б: carriers_sharded — ТЕ ЖЕ 5 строк, распределённые по своему
#      ключу carrier (create_distributed_table('carriers_sharded',
#      'carrier', colocate_with => 'none')).
#
# carriers_sharded создаёт и наполняет САМ этот скрипт (CREATE TABLE ... AS
# / INSERT ... SELECT * FROM carriers) и удаляет через trap — в схеме
# стенда её быть не должно, она существует только ради контраста.
#
# ФАКТ (установлен ранее живьём, см. task-4-citus-brief.md): Citus требует,
# чтобы колонка распределения входила в PRIMARY KEY. carriers уже объявлена
# как `carrier TEXT PRIMARY KEY` (sql/01-schema.sql), поэтому
# `CREATE TABLE carriers_sharded (LIKE carriers INCLUDING ALL)` сразу даёт
# нужный PRIMARY KEY (carrier) — никакой перестройки ключа не нужно.
#
# ФАКТ: без явного colocate_with => 'none' таблица с ключом того же типа
# автоматически легла бы в существующую группу колокации. Здесь ключ
# carrier — TEXT, тогда как customers/orders/shipments шардированы по
# BIGINT, так что совпадения типа и так не было бы, но colocate_with =>
# 'none' передаётся явно — чтобы не полагаться на это совпадение и получить
# для carriers_sharded заведомо отдельную группу колокации.
#
# ЧЕМ ЭТОТ АРТЕФАКТ ОТЛИЧАЕТСЯ ОТ АРТЕФАКТА 2 (colocation-demo.sh). По одному
# лишь виду плана эти два артефакта неразличимы: там тоже колоцированная ветка
# даёт Task Count=32/MapMergeJob=0, а неколоцированная — Task Count=8/
# MapMergeJob=2. И это не совпадение: и колокация, и референсность добиваются
# ОДНОГО И ТОГО ЖЕ — локального join без переброски данных. Но добиваются они
# этого РАЗНЫМИ способами, и вот эта разница видна не в плане, а в физическом
# размещении данных:
#
#   колокация  — согласованное разложение двух таблиц по общему ключу:
#                у обеих 32 шарда, у каждого шарда ОДНО размещение, парные
#                шарды лежат на одном узле. Репликации нет.
#   референс   — ОДИН шард, скопированный на КАЖДЫЙ ВОРКЕР (на координаторе
#                размещения нет): 1 шард, размещений
#                столько же, сколько воркеров. Копия справочника физически
#                лежит рядом с любыми данными, поэтому join локален с чем
#                угодно, а не только с таблицами из своей группы колокации.
#
# Поэтому скрипт измеряет и печатает ЧИСЛО ШАРДОВ и ЧИСЛО РАЗМЕЩЕНИЙ для обеих
# веток (citus_tables.shard_count, pg_dist_shard + pg_dist_placement) — это и
# есть собственный структурный признак референсной таблицы, которого у
# колокации нет. Проверять один только ярлык citus_table_type = 'reference'
# недостаточно: ярлык — это то, как Citus называет таблицу, а не доказательство
# того, что за словом стоит.
#
# Число воркеров берётся ДИНАМИЧЕСКИ из pg_dist_node (groupid <> 0 отсекает
# координатора), а не константой: стенд позже вырастет до трёх узлов, и
# ожидаемое число размещений референсной таблицы вырастет вместе с ним.
#
# Join carriers/carriers_sharded с shipments по колонке carrier (та же
# колонка, что использовалась при наполнении в up.sh). Главная измеряемая
# величина — вид плана EXPLAIN, а не время:
#
#   А (референсная carriers): каждая из задач shipments выполняет join
#      ЛОКАЛЬНО на своём шарде — у справочника есть полная копия на каждом
#      узле, переброска данных не нужна. В плане нет узла MapMergeJob.
#   Б, citus.enable_repartition_joins по умолчанию (ВЫКЛЮЧЕН, факт из
#      Task 1 — см. colocation-demo.sh): планировщик СРАЗУ отказывается
#      выполнять join, тем же текстом ошибки, что и в артефакте 2:
#        ERROR:  the query contains a join that requires repartitioning
#      Распределённый справочник — не копия на каждом воркере, и join с ним
#      требует такой же переброски данных, как join с любой другой
#      нераспределённой парой таблиц.
#   Б, citus.enable_repartition_joins включён НА УРОВНЕ СЕССИИ: join
#      выполняется, но в плане появляется MapMergeJob — ровно две стадии
#      (Map Task Count / Merge Task Count), как и в артефакте 2.
#
# Параметр включается СТРОГО через SET на уровне одного psql-подключения —
# так же, как в colocation-demo.sh. Каждый вызов psql в этом скрипте — новое
# подключение (docker exec запускает новый процесс), поэтому SET из одного
# вызова физически не может просочиться в следующий, и кластер не остаётся
# в изменённом состоянии для других артефактов.
#
# Падающий вариант: если бы референсность не влияла на выполнение join, оба
# плана (А и Б-с-репартицией) выглядели бы одинаково — либо у обоих был бы
# MapMergeJob, либо ни у одного. То, что MapMergeJob отсутствует СТРОГО у А
# и появляется СТРОГО у Б — и есть демонстрируемое отличие.
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
# Скрипт прогоняет сценарий ДВАЖДЫ и сверяет структурные величины (Task
# Count и наличие MapMergeJob для А, дословный текст ошибки Б по умолчанию,
# Task Count / Map / Merge Task Count и MapMergeJob для Б с включённой
# репартицией, а также число шардов и размещений обеих веток) между прогонами —
# они обязаны совпасть точно. Если оба плана (А и Б-с-репартицией)
# оказались бы одного вида (падающий вариант) или расходятся между
# прогонами — скрипт объявляет провал демонстрации и завершается ненулевым
# кодом. В конце скрипт явно проверяет, что carriers_sharded удалена и
# схема стенда вернулась к исходному состоянию (4 распределённые/
# референсные таблицы: customers, orders, shipments, carriers).
#
# Требует уже поднятого и наполненного стенда (bash scripts/up.sh).
#
# Запуск (из databases/citus/):
#   bash scripts/reference-demo.sh

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

DB_USER=shard
DB_NAME=shard

log()  { echo "[reference] $*" >&2; }
fail() { echo "[reference] ОШИБКА: $*" >&2; exit 1; }

# --- Preflight ---------------------------------------------------------------

command -v docker >/dev/null 2>&1 || fail "docker не найден в PATH."
docker info >/dev/null 2>&1 || fail "docker недоступен (демон не отвечает)."

state="$(docker inspect --format '{{.State.Status}}' citus-coord 2>/dev/null || true)"
[ "$state" = "running" ] || fail "контейнер citus-coord не запущен. Сначала: bash scripts/up.sh"

psql_c() {
  docker exec -i citus-coord psql -X -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" "$@"
}

log "Preflight: проверяем, что carriers — референсная таблица, а shipments распределена и наполнена…"

carriers_type="$(psql_c -At -c "SELECT citus_table_type FROM citus_tables WHERE table_name = 'carriers'::regclass;")"
[ "$carriers_type" = "reference" ] || fail "carriers не является референсной таблицей (citus_table_type=$carriers_type). Сначала: bash scripts/up.sh"

# Число ВОРКЕРОВ — динамически, а не константой: groupid <> 0 отсекает
# координатора (у него groupid = 0), noderole = 'primary' отсекает возможные
# реплики. Стенд позже вырастет до трёх узлов, и ожидаемое число размещений
# референсной таблицы обязано вырасти вместе с ним само.
WORKERS="$(psql_c -At -c "SELECT count(*) FROM pg_dist_node WHERE isactive AND noderole = 'primary' AND groupid <> 0;")"
[ -n "$WORKERS" ] && [ "$WORKERS" -ge 2 ] \
  || fail "в pg_dist_node меньше двух активных воркеров (нашли: ${WORKERS:-нет данных}) — на одном узле разница между референсной и распределённой таблицей по размещениям не видна. Сначала: bash scripts/up.sh"
log "Активных воркеров в pg_dist_node: $WORKERS (число размещений референсной таблицы ожидается равным этому числу)."

carriers_n="$(psql_c -At -c "SELECT count(*) FROM carriers;")"
[ "$carriers_n" -ge 5 ] || fail "ожидали не меньше 5 строк в carriers, нашли: $carriers_n. Наполнение не на месте — bash scripts/up.sh"

shipments_dist="$(psql_c -At -c "SELECT count(*) FROM pg_dist_partition WHERE logicalrelid = 'shipments'::regclass;")"
[ "$shipments_dist" = "1" ] || fail "таблица shipments не распределена (pg_dist_partition пуст для shipments). Сначала: bash scripts/up.sh"

shipments_n="$(psql_c -At -c "SELECT count(*) FROM shipments;")"
[ "$shipments_n" -ge 4000 ] || fail "ожидали не меньше 4000 строк в shipments, нашли: $shipments_n. Наполнение не на месте — bash scripts/up.sh"

coloc_shipments="$(psql_c -At -c "SELECT colocationid FROM pg_dist_partition WHERE logicalrelid = 'shipments'::regclass;")"

guc_default="$(psql_c -At -c "SHOW citus.enable_repartition_joins;")"
[ "$guc_default" = "off" ] || log "ВНИМАНИЕ: citus.enable_repartition_joins по умолчанию = '$guc_default', а не 'off' — расходится с фактом из Task 1. Продолжаем, но это важное расхождение (см. самопроверку ниже)."

log "carriers: $carriers_n строк (референсная). shipments: $shipments_n строк, colocationid=$coloc_shipments. citus.enable_repartition_joins по умолчанию: $guc_default."

# --- carriers_sharded: создаётся и удаляется этим скриптом -------------------
#
# trap гарантирует уборку и при обычном завершении, и при прерывании
# (Ctrl+C, ошибка любой команды под set -e). DROP TABLE IF EXISTS —
# идемпотентен, безопасен даже если таблица уже была удалена раньше.

cleanup() {
  log "Уборка: удаляем carriers_sharded (существует только для этой демонстрации)…"
  docker exec -i citus-coord psql -X -U "$DB_USER" -d "$DB_NAME" \
    -c "DROP TABLE IF EXISTS carriers_sharded;" >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "Preflight: если carriers_sharded осталась от прерванного прогона — удаляем перед началом…"
psql_c -c "DROP TABLE IF EXISTS carriers_sharded;" >/dev/null

log "Создаём carriers_sharded — распределённую копию carriers по ключу carrier…"
psql_c <<'SQL'
CREATE TABLE carriers_sharded (LIKE carriers INCLUDING ALL);
INSERT INTO carriers_sharded SELECT * FROM carriers;
SQL

psql_c -c "SELECT create_distributed_table('carriers_sharded', 'carrier', colocate_with => 'none');" >/dev/null

sharded_n="$(psql_c -At -c "SELECT count(*) FROM carriers_sharded;")"
[ "$sharded_n" = "$carriers_n" ] || fail "carriers_sharded содержит $sharded_n строк, ожидали ровно $carriers_n (столько же, сколько в carriers) — данные веток разошлись."

coloc_carriers_sharded="$(psql_c -At -c "SELECT colocationid FROM pg_dist_partition WHERE logicalrelid = 'carriers_sharded'::regclass;")"
[ -n "$coloc_carriers_sharded" ] || fail "не удалось получить colocationid для carriers_sharded из pg_dist_partition."
[ "$coloc_carriers_sharded" != "$coloc_shipments" ] \
  || fail "carriers_sharded оказалась в ОДНОЙ группе колокации с shipments (colocationid=$coloc_carriers_sharded) — join Б не потребует репартиции, демонстрация невозможна."

log "carriers_sharded создана: $sharded_n строк, colocationid=$coloc_carriers_sharded (отдельная группа от shipments)."

# --- Физическое размещение: шарды и размещения -------------------------------
#
# Собственный структурный признак референсной таблицы, которого у колокации
# НЕТ. Одна и та же пара запросов к системным каталогам для обеих веток:
#
#   citus_tables.shard_count — сколько у таблицы шардов;
#   pg_dist_shard + pg_dist_placement — сколько всего физических размещений
#   этих шардов по узлам кластера.
#
# У референсной таблицы: 1 шард, размещений = числу воркеров (одна и та же
# копия продублирована на каждом воркере). У распределённой: N шардов, ровно по
# одному размещению на шард (репликации нет, каждый шард лежит на одном узле).

shard_count_of() { # $1 = имя таблицы
  psql_c -At -c "SELECT shard_count FROM citus_tables WHERE table_name = '$1'::regclass;"
}

placement_count_of() { # $1 = имя таблицы
  psql_c -At -c "SELECT count(*) FROM pg_dist_shard ps JOIN pg_dist_placement pp ON pp.shardid = ps.shardid WHERE ps.logicalrelid = '$1'::regclass;"
}

SHARDS_A="$(shard_count_of carriers)"
PLACEMENTS_A="$(placement_count_of carriers)"
SHARDS_B="$(shard_count_of carriers_sharded)"
PLACEMENTS_B="$(placement_count_of carriers_sharded)"

for v in SHARDS_A PLACEMENTS_A SHARDS_B PLACEMENTS_B; do
  [ -n "${!v}" ] || fail "не удалось измерить $v из системных каталогов Citus (citus_tables/pg_dist_shard/pg_dist_placement)."
done

log "Размещение А (carriers, референсная):    шардов=$SHARDS_A, размещений=$PLACEMENTS_A (воркеров в кластере: $WORKERS)."
log "Размещение Б (carriers_sharded, распр.): шардов=$SHARDS_B, размещений=$PLACEMENTS_B."

# --- Запросы -------------------------------------------------------------

SQL_A="SELECT c.country, count(*) FROM shipments s JOIN carriers c USING (carrier) GROUP BY c.country;"
SQL_B="SELECT c.country, count(*) FROM shipments s JOIN carriers_sharded c USING (carrier) GROUP BY c.country;"

# run_explain <sql> — выполняет EXPLAIN (ANALYZE, VERBOSE) в обычной
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
# подключения (SET + EXPLAIN в одном вызове -c, одна сессия). Следующий
# вызов psql_c — уже новое подключение с параметром по умолчанию.
run_explain_repartition() {
  local sql="$1" plan
  plan="$(psql_c -c "SET citus.enable_repartition_joins = on; EXPLAIN (ANALYZE, VERBOSE) $sql")"
  echo "$plan"

  TASK_COUNT="$(echo "$plan" | grep -oP 'Task Count: \K[0-9]+' | head -1 || true)"
  [ -n "$TASK_COUNT" ] || fail "не нашли 'Task Count' в плане для запроса (с включённой репартицией): $sql"

  EXEC_MS="$(echo "$plan" | grep -oP 'Execution Time: \K[0-9.]+' | tail -1 || true)"
  [ -n "$EXEC_MS" ] || fail "не нашли 'Execution Time' в плане для запроса (с включённой репартицией): $sql"

  MAPMERGE_COUNT="$(echo "$plan" | grep -c 'MapMergeJob' || true)"

  # Внутренние счётчики стадий репартиции. Нужны, чтобы объяснить читателю,
  # почему верхний Task Count у Б меньше числа шардов: это счётчик ПОСЛЕДНЕЙ
  # (merge) стадии, а не числа просканированных шардов.
  MAP_TASKS="$(echo "$plan" | grep -oP 'Map Task Count: \K[0-9]+' | head -1 || true)"
  MERGE_TASKS="$(echo "$plan" | grep -oP 'Merge Task Count: \K[0-9]+' | head -1 || true)"
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
declare -a TC_B EXEC_B MM_B MAPT_B MERGET_B

for run in 1 2; do
  echo
  echo "================================================================"
  echo " Прогон $run / 2"
  echo "================================================================"

  echo
  echo "--- А: shipments JOIN carriers (референсная) ---"
  run_explain "$SQL_A"
  TC_A[$run]="$TASK_COUNT"
  EXEC_A[$run]="$EXEC_MS"
  MM_A[$run]="$MAPMERGE_COUNT"
  log "Прогон $run, А: Task Count=${TC_A[$run]}, MapMergeJob в плане=${MM_A[$run]}, Execution Time=${EXEC_A[$run]} мс"

  echo
  echo "--- Б (по умолчанию, citus.enable_repartition_joins=off): shipments JOIN carriers_sharded — распределённая копия ---"
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
  MAPT_B[$run]="$MAP_TASKS"
  MERGET_B[$run]="$MERGE_TASKS"
  log "Прогон $run, Б (репартиция включена): Task Count=${TC_B[$run]} (Map Task Count=${MAPT_B[$run]}, Merge Task Count=${MERGET_B[$run]}), MapMergeJob в плане=${MM_B[$run]}, Execution Time=${EXEC_B[$run]} мс"
done

# --- Сводка ------------------------------------------------------------------

echo
echo "================================================================"
echo " Сводка"
echo "================================================================"
printf '%-10s %-58s %-12s %-16s %-14s\n' "Прогон" "Запрос" "Task Count" "MapMergeJob" "Execution ms"
printf '%-10s %-58s %-12s %-16s %-14s\n' "1" "А: shipments+carriers (референсная)"           "${TC_A[1]}" "${MM_A[1]}" "${EXEC_A[1]}"
printf '%-10s %-58s %-12s %-16s %-14s\n' "1" "Б: shipments+carriers_sharded (репартиция, on)" "${TC_B[1]}" "${MM_B[1]}" "${EXEC_B[1]}"
printf '%-10s %-58s %-12s %-16s %-14s\n' "2" "А: shipments+carriers (референсная)"           "${TC_A[2]}" "${MM_A[2]}" "${EXEC_A[2]}"
printf '%-10s %-58s %-12s %-16s %-14s\n' "2" "Б: shipments+carriers_sharded (репартиция, on)" "${TC_B[2]}" "${MM_B[2]}" "${EXEC_B[2]}"
echo
echo "Б по умолчанию (citus.enable_repartition_joins=off) в обоих прогонах"
echo "отказалась выполняться — см. дословный текст ошибки выше и в проверке ниже."

echo
echo "ПРО Task Count=${TC_B[1]} У ВЕТКИ Б. Число меньше 32 шардов не потому, что"
echo "запрос прочитал меньше данных: верхний Task Count репартиционного плана —"
echo "это счётчик ПОСЛЕДНЕЙ (merge) стадии, Merge Task Count=${MERGET_B[1]}. Шарды"
echo "сканируются на первой стадии, и там счётчик другой: Map Task Count=${MAPT_B[1]}"
echo "— ровно по числу шардов shipments. Обе строки видны внутри узлов"
echo "MapMergeJob в плане выше."

echo
echo "================================================================"
echo " Физическое размещение справочника (чем это отличается от артефакта 2)"
echo "================================================================"
printf '%-46s %-10s %-14s\n' "Таблица" "Шардов" "Размещений"
printf '%-46s %-10s %-14s\n' "А: carriers (референсная)"        "$SHARDS_A" "$PLACEMENTS_A"
printf '%-46s %-10s %-14s\n' "Б: carriers_sharded (распределённая)" "$SHARDS_B" "$PLACEMENTS_B"
echo
echo "Воркеров в кластере (pg_dist_node, динамически): $WORKERS."
echo
echo "Вот собственный признак референсной таблицы, которого у колокации НЕТ."
echo "У А один-единственный шард, но размещений у него $PLACEMENTS_A — по одному на"
echo "каждый ВОРКЕР: это ОДНА И ТА ЖЕ копия справочника, продублированная на все"
echo "ВОРКЕРЫ. Координатор — тоже узел кластера, но размещений референсной таблицы"
echo "на нём нет, поэтому «на всех узлах» было бы неверно."
echo "Поэтому join с ней локален с ЧЕМ УГОДНО — копия уже физически лежит"
echo "рядом с любыми данными, независимо от того, по какому ключу те разложены."
echo "У Б $SHARDS_B шардов и ровно $PLACEMENTS_B размещений — по одному на шард, репликации"
echo "нет, каждый кусок справочника лежит на одном узле, и join с ним требует"
echo "переброски."
echo
echo "Артефакт 2 (колокация) добивался локальности ДРУГИМ способом: там у обеих"
echo "таблиц по 32 шарда и по одному размещению на шард, а join локален потому,"
echo "что парные шарды согласованно разложены по общему ключу и лежат на одном"
echo "узле. Репликации там нет вовсе. По виду плана эти два механизма"
echo "неразличимы (в обоих случаях MapMergeJob=0) — различает их именно"
echo "таблица размещений выше."

echo
echo "================================================================"
echo " Падающий вариант (что было бы, если бы референсность не влияла)"
echo "================================================================"
echo "Если бы Citus выполнял join с референсной и с распределённой копией"
echo "справочника одинаково — без разницы, лежит ли копия на каждом воркере или"
echo "справочник распределён по своему ключу — оба плана выглядели бы"
echo "одинаково: либо ни в одном из них не было бы узла MapMergeJob"
echo "(переброска данных никогда не требуется), либо он был бы в обоих. То,"
echo "что MapMergeJob присутствует СТРОГО у Б (репартиция) и отсутствует"
echo "СТРОГО у А (референсная) — и есть демонстрируемое отличие; именно это"
echo "и проверяется ниже."

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
  echo "[FAIL] В плане А (join с референсной carriers) нашёлся узел MapMergeJob (прогон 1: ${MM_A[1]}, прогон 2: ${MM_A[2]}) — join с полной копией справочника на каждом воркере не должен требовать переброски данных."
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
  echo "[FAIL] В плане Б (join с распределённой carriers_sharded, параметр включён) НЕ нашёлся узел MapMergeJob (прогон 1: ${MM_B[1]}, прогон 2: ${MM_B[2]}) — репартиционный join обязан репартиционировать данные."
  ok=0
fi
if [ "${MM_B[1]}" != "${MM_B[2]}" ]; then
  echo "[FAIL] Количество узлов MapMergeJob для Б разошлось между прогонами: ${MM_B[1]} vs ${MM_B[2]} — структурная величина обязана совпасть точно."
  ok=0
fi

if [ "${MM_A[1]}" = "${MM_B[1]}" ]; then
  echo "[FAIL] Наличие MapMergeJob у А и Б совпало (${MM_A[1]} = ${MM_B[1]}) — это и есть падающий вариант: демонстрация не показывает разницы между референсной и распределённой копией справочника."
  ok=0
fi

# --- Самопроверка физического размещения -------------------------------------
#
# Именно эти проверки отличают артефакт 3 от артефакта 2: они утверждают не
# «join локален», а «join локален ИМЕННО ПОТОМУ, что справочник реплицирован».

if [ "$SHARDS_A" != "1" ]; then
  echo "[FAIL] У референсной carriers шардов=$SHARDS_A, ожидали ровно 1 — референсная таблица по определению состоит из одного шарда, скопированного на все ВОРКЕРЫ (не на все узлы: координатор копии не держит)."
  ok=0
fi
if [ "$PLACEMENTS_A" != "$WORKERS" ]; then
  echo "[FAIL] У референсной carriers размещений=$PLACEMENTS_A, ожидали $WORKERS (по числу активных воркеров в pg_dist_node) — копия справочника обязана лежать на КАЖДОМ ВОРКЕРЕ (координатор — тоже узел кластера, но размещения референсной таблицы у него нет), иначе локальность join объясняется не репликацией."
  ok=0
fi
if [ "$PLACEMENTS_B" != "$SHARDS_B" ]; then
  echo "[FAIL] У распределённой carriers_sharded размещений=$PLACEMENTS_B при $SHARDS_B шардах, ожидали ровно по одному размещению на шард — распределённая таблица не реплицируется."
  ok=0
fi
if [ "$SHARDS_B" -le 1 ]; then
  echo "[FAIL] У распределённой carriers_sharded шардов=$SHARDS_B — на одном шарде она структурно неотличима от референсной, контраст пропадает."
  ok=0
fi
if [ "$SHARDS_A" = "$SHARDS_B" ] && [ "$PLACEMENTS_A" = "$PLACEMENTS_B" ]; then
  echo "[FAIL] Число шардов и размещений у А и Б совпало (шардов $SHARDS_A, размещений $PLACEMENTS_A) — это падающий вариант для физического признака: по размещению данных ветки неразличимы, и артефакт не добавляет ничего сверх артефакта 2."
  ok=0
fi

# Счётчики стадий репартиции — нужны для объяснения Task Count в выводе.
if [ -z "${MAPT_B[1]}" ] || [ -z "${MERGET_B[1]}" ]; then
  echo "[FAIL] Не удалось извлечь Map Task Count / Merge Task Count из плана Б — формат вывода EXPLAIN изменился, объяснение Task Count в сводке будет голословным."
  ok=0
elif [ "${MERGET_B[1]}" != "${TC_B[1]}" ]; then
  echo "[FAIL] Merge Task Count=${MERGET_B[1]} не совпал с верхним Task Count=${TC_B[1]} — объяснение «верхний Task Count это счётчик merge-стадии» неверно, не печатать его как факт."
  ok=0
fi
if [ "${MAPT_B[1]}" != "${MAPT_B[2]}" ] || [ "${MERGET_B[1]}" != "${MERGET_B[2]}" ]; then
  echo "[FAIL] Map/Merge Task Count разошлись между прогонами (Map: ${MAPT_B[1]} vs ${MAPT_B[2]}, Merge: ${MERGET_B[1]} vs ${MERGET_B[2]}) — структурные величины обязаны совпасть точно."
  ok=0
fi

if [ "$guc_default" != "off" ]; then
  echo "[FAIL] citus.enable_repartition_joins по умолчанию оказался '$guc_default', а не 'off' — расходится с фактом Task 1. Само по себе это не ломает демонстрацию структурно (проверки выше это уже покрыли бы), но требует внимания в отчёте — не публиковать данные молча, разобраться в причине (версия Citus/конфиг)."
  ok=0
fi

# --- Уборка и проверка, что схема стенда вернулась к исходной ----------------

log "Удаляем carriers_sharded явно (до завершения скрипта, не дожидаясь trap)…"
psql_c -c "DROP TABLE IF EXISTS carriers_sharded;" >/dev/null

remaining="$(psql_c -At -c "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'carriers_sharded';")"
if [ "$remaining" != "0" ]; then
  echo "[FAIL] carriers_sharded не была удалена после демонстрации (найдено таблиц: $remaining) — схема стенда осталась изменённой."
  ok=0
fi

final_dist_count="$(psql_c -At -c "SELECT count(*) FROM citus_tables WHERE table_name::text IN ('customers','orders','shipments','carriers');")"
if [ "$final_dist_count" != "4" ]; then
  echo "[FAIL] После демонстрации в citus_tables найдено $final_dist_count распределённых/референсных таблиц из ('customers','orders','shipments','carriers'), ожидали ровно 4 — схема стенда разошлась с исходной."
  ok=0
fi

if [ "$ok" != "1" ]; then
  fail "демонстрация провалена (см. [FAIL] выше). Числа не воспроизвели ожидаемую картину — не публиковать эти данные как есть, разбираться в причине (порт/схема/наполнение/версия Citus)."
fi

echo "[OK] carriers_sharded удалена, схема стенда вернулась к исходной: 4 распределённые/референсные таблицы (customers, orders, shipments, carriers), лишних объектов нет."
echo
echo "[OK] Структурные величины воспроизводятся точно между прогонами:"
echo "     А (референсная carriers): Task Count=${TC_A[1]}, MapMergeJob=0 — join выполняется локально на каждой задаче shipments, копия справочника уже на месте."
echo "     Б по умолчанию (carriers_sharded): отказ планировщика, дословно: «${EXPECTED_ERR}»."
echo "     Б с включённой репартицией: Task Count=${TC_B[1]} (Map=${MAPT_B[1]}, Merge=${MERGET_B[1]}), MapMergeJob=${MM_B[1]} — join с распределённой копией того же справочника требует переброски данных между воркерами."
echo
echo "[OK] Физическое размещение подтверждает, ПОЧЕМУ join А локален:"
echo "     А (carriers, референсная):    шардов=$SHARDS_A, размещений=$PLACEMENTS_A = число воркеров ($WORKERS) — один шард, скопированный на каждый ВОРКЕР (координатор — тоже узел кластера, но размещения у него нет)."
echo "     Б (carriers_sharded, распр.): шардов=$SHARDS_B, размещений=$PLACEMENTS_B — по одному размещению на шард, репликации нет."
echo "     Локальность join у А объясняется РЕПЛИКАЦИЕЙ справочника, а не колокацией:"
echo "     это то новое, чего не показывает артефакт 2, где локальность достигалась"
echo "     согласованным разложением по общему ключу без единой лишней копии."
echo
echo "Время — только порядок величины. Все узлы на одном хосте; на реальном"
echo "кластере добавятся сетевой объём и удалённые взаимодействия, но их"
echo "величина здесь НЕ измерена, и направление изменения разрыва Б/А"
echo "утверждать нельзя:"
echo "  А ~ ${EXEC_A[1]}/${EXEC_A[2]} мс, Б (репартиция) ~ ${EXEC_B[1]}/${EXEC_B[2]} мс."
echo "Сравнение Citus с обычным PostgreSQL по скорости в этом артефакте не"
echo "проводится — ванильный PostgreSQL здесь не запускается вовсе."
echo
echo "citus.enable_repartition_joins включался ИСКЛЮЧИТЕЛЬНО через SET внутри"
echo "одного psql-подключения (одна простая query-message: SET + EXPLAIN)."
echo "Каждый вызов psql в этом скрипте — новое подключение, поэтому изменение"
echo "не переживает вызов и кластер не остаётся в изменённом состоянии для"
echo "следующих артефактов."

log "Готово."
