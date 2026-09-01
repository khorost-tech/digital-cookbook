#!/usr/bin/env bash
#
# rebalance-demo.sh — артефакт 5 (последний): добавление узла и ребаланс.
# Кластер стенда живёт на ДВУХ воркерах (citus-w1, citus-w2). Этот артефакт
# временно поднимает ТРЕТИЙ (citus-w3, compose-профиль grow) и отвечает на
# два вопроса:
#
#   1. Само по себе появление нового узла в кластере данные не двигает —
#      Citus не перекладывает шарды автоматически просто потому, что узел
#      стал активным. Если бы двигало, ребаланс был бы не нужен.
#   2. Явный ребаланс (citus_rebalance_start) — двигает, причём во время
#      переноса запросы по ключу шардирования продолжают проходить: перенос
#      шарда идёт логической репликацией (копия + докатка WAL), которая
#      позволяет избежать блокировки ЗАПИСЕЙ на время копирования.
#
# ⚠️ ЧТО ИМЕННО ПРОТИВОПОСТАВЛЯЮТ РЕЖИМЫ (правка по итогам внешнего ревью).
# Документация Citus (citus_move_shard_placement) противопоставляет режимы по
# ЗАПИСЯМ, а не по чтениям: shard_transfer_mode='auto'/'force_logical' ведёт
# перенос логической репликацией и позволяет избежать блокировки ЗАПИСЕЙ, а
# 'block_writes' копирует шард через COPY С БЛОКИРОВКОЙ ЗАПИСЕЙ. Чтение
# доступно в ОБОИХ режимах. Поэтому одна читающая проба структурно не способна
# показать преимущество 'auto': она была бы успешна и при 'block_writes'.
# Прежняя редакция артефакта слала только SELECT и на его успехе объявляла
# «перенос без блокировки чтения» — это подмена доказательства, снята. Теперь в
# цикл опроса идут ДВЕ пробы: читающая и ПИШУЩАЯ (INSERT в тот же переезжающий
# шард + проверка видимости записанного). Главный результат пишущей пробы —
# ОТКАЗ или ПОТЕРЯ ВИДИМОСТИ записанного; длительность печатается как сырое
# число. Диагностировать блокировку по длительности этот стенд НЕ умеет:
# отсчёты не соотнесены с фазой переноса конкретного шарда и включают
# docker exec с подключением psql.
#
# ГЛАВНАЯ ИЗМЕРЯЕМАЯ ВЕЛИЧИНА — распределение шардов/размещений по узлам
# (pg_dist_placement/pg_dist_node), а НЕ время. Три снимка одного и того же
# запроса к системным каталогам: до добавления узла, после добавления (до
# ребаланса), после ребаланса.
#
# ФАКТ (Task 1, api-probe.sh, живьём на Citus 14.1.0-pg17): все нужные
# функции есть под современными именами — citus_add_node, citus_rebalance_start,
# citus_drain_node, get_rebalance_progress, citus_remove_node. Использованы
# без запасного плана на отсутствие функций.
#
# ЖИВАЯ НАХОДКА (Task 6, этот артефакт). citus_rebalance_start() по умолчанию
# (shard_transfer_mode='auto') выбирает логическую репликацию для переноса —
# именно она даёт «без остановки обслуживания». Но она требует
# wal_level=logical на стороне ПУБЛИКАЦИИ (к подписчику такого требования
# нет). Какой узел публикует в конкретной операции переноса — стенд не
# проверяет, а опыт сравнивал только «replica везде» против «logical
# везде», не разделяя ролей. Поэтому здесь logical включён на всех
# сервисах КОНСЕРВАТИВНО; минимально необходимый набор ролей
# экспериментом НЕ установлен. Образ citusdata/citus поднимается
# с wal_level=replica (обычный дефолт PostgreSQL) — с ним ребаланс не падает
# сразу читаемой ошибкой, а ЗАВИСАЕТ: фоновая задача
# 'SELECT replicate_reference_tables(auto)' уходит в ЗАТЯЖНОЙ ПОВТОР с
# ошибкой «logical decoding requires "wal_level" >= "logical"» и блокирует
# собой ВСЕ запланированные перемещения шардов (воспроизведено живьём: job
# держался в состоянии running с 8 повторами и 20 заблокированными задачами
# дольше 5 минут, реального прогресса не было; наблюдение прервано вручную,
# поэтому доказан затяжной повтор без прогресса, а НЕ бесконечный цикл).
#
# ГРАНИЦА ИЗМЕРЕНИЯ. Проверено: с wal_level=replica ВЕЗДЕ перенос не идёт,
# с wal_level=logical ВЕЗДЕ идёт. НЕ проверено: нужен ли logical именно на
# КООРДИНАТОРЕ — эксперимент сравнивал только «везде replica» против «везде
# logical», роль координатора в нём не изолирована, контрольного опыта
# (logical только на воркерах) не ставилось. Включение на всех четырёх
# сервисах — сознательный выбор конфигурации стенда, а не доказанное
# требование к координатору. Это не решение в духе
# «подогнать конструкцию под факт» — это исправление реального пробела
# конфигурации, без которого утверждение статьи («ребаланс не останавливает
# обслуживание») нельзя было бы проверить вообще: с wal_level=replica либо
# зависает (auto), либо пришлось бы принудительно использовать
# shard_transfer_mode='block_writes', который блокировкой ЗАПИСЕЙ как раз
# ломает демонстрируемое утверждение. Поэтому compose/compose.yml правлен
# ЗДЕСЬ ЖЕ (все четыре сервиса получили command: postgres -c
# wal_level=logical) — это правка инфраструктуры стенда, а не костыль внутри
# этого скрипта. Обратной совместимости не нарушает: wal_level=logical —
# чистое расширение возможностей WAL, остальные артефакты (1-4) от него не
# зависят и продолжают работать как прежде.
#
# ПАДАЮЩИЙ ВАРИАНТ (шаг «до/после добавления узла»): если бы добавление узла
# само по себе перераспределяло шарды, снимок ПОСЛЕ добавления уже показал бы
# ненулевое число размещений на citus-w3 — и последующий ребаланс был бы
# бессмысленным шагом. Именно это здесь и проверяется как провал сценария.
#
# ⚠️ pg_dist_placement.shardstate: после ребаланса Citus может оставить
# осиротевшие размещения со shardstate=4 до уборки фоновым процессом. Все
# запросы к pg_dist_placement в этом скрипте фильтруют WHERE shardstate = 1 —
# без фильтра счётчик размещений раздулся бы и картина стала бы ложной.
#
# ⚠️ ЖИВАЯ НАХОДКА (ревью после Task 7): citus_drain_node НЕ трогает копии
# референсных таблиц — carriers остаётся на осушаемом узле до
# citus_remove_node. Цикл ожидания в уборке ниже раньше ждал нуля ВСЕХ
# размещений (в т.ч. референсных) и поэтому ГАРАНТИРОВАННО не дожидался
# нуля — 60 попыток × 5с = 300с впустую на КАЖДОМ штатном прогоне. Исправлено:
# считаем только размещения РАСПРЕДЕЛЁННЫХ таблиц, референсная копия ждёт
# своей очереди у citus_remove_node.
#
# ⚠️ РЕФЕРЕНСНАЯ ТАБЛИЦА (carriers) НА НОВОМ УЗЛЕ — проверяется фактически,
# а не предполагается. Живой ответ (воспроизведён дважды): простое
# citus_add_node НЕ копирует референсные таблицы на новый узел — carriers
# остаётся с 2 размещениями (citus-w1, citus-w2) сразу после добавления
# citus-w3. Копию на третий узел кладёт ИМЕННО ребаланс: получен рабочий
# лог 'replicate_reference_tables' в pg_dist_background_task, выполняемый
# ПЕРВОЙ задачей job'а, ДО переноса обычных шардов (остальные 20 задач
# перемещения шардов явно blocked, пока эта не выполнится). Это подтверждено
# и негативно: пока задача падала из-за wal_level=replica, carriers так и
# оставалась на 2 узлах — перенос НЕ происходит сам по себе без успешного
# ребаланса.
#
# Требует уже поднятого и наполненного стенда на ДВУХ воркерах
# (bash scripts/up.sh, БЕЗ профиля grow — третий воркер поднимает этот
# скрипт сам и сам же его убирает).
#
# УБОРКА. В отличие от carriers_sharded/orders_big в соседних артефактах,
# здесь не таблицу удалить, а вернуть кластер к ДВУМ воркерам: осушить
# citus-w3 (citus_drain_node — переносит его шарды обратно), снять
# регистрацию (citus_remove_node) и остановить контейнер. Это критично:
# reference-demo.sh динамически берёт число воркеров из pg_dist_node и
# ожидает, что размещений референсной таблицы ровно столько же — если стенд
# останется на трёх узлах, тот артефакт начнёт падать. Уборка происходит и
# по нормальному завершению (явным вызовом), и по аварийному (trap EXIT,
# best-effort, идемпотентно).
#
# Запуск (из databases/citus/):
#   bash scripts/rebalance-demo.sh

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

COMPOSE_FILE="compose/compose.yml"
COMPOSE=(docker compose -f "$COMPOSE_FILE")
DB_USER=shard
DB_NAME=shard

# PROBE_KEY НЕ фиксирован константой намеренно. Фиксированный ключ (раньше здесь
# стояло 42) попадает в случайный шард, и этот шард может в план перемещений
# вообще не входить — тогда успешные пробы доказывают лишь доступность
# НЕПОДВИЖНОГО шарда во время фоновой задачи, а не чтение данных В ПРОЦЕССЕ их
# переноса. Это подмена доказательства. Поэтому ключ выбирается на шаге 2 из
# ФАКТИЧЕСКОГО плана перемещений (get_rebalance_table_shards_plan), а членство
# его шарда в множестве перемещаемых проверяется явным гейт-инвариантом.
PROBE_KEY=""          # выбирается динамически, см. шаг 2
PROBE_SHARD=""        # shardid таблицы orders, в который бьют обе пробы

# Диапазон order_id для строк ПИШУЩЕЙ пробы. Наполнение стенда (sql/02-seed.sql)
# использует order_id существенно меньше этого значения, поэтому:
#   - вставки пробы не конфликтуют с PRIMARY KEY (customer_id, order_id);
#   - читающую пробу можно оставить со СТАБИЛЬНЫМ ожидаемым ответом, ограничив
#     её order_id < WPROBE_ID_BASE (иначе каждая вставка меняла бы ожидание
#     читающей пробы, и та начала бы «отказывать» из-за собственной записи);
#   - уборка строк пробы точна: DELETE ровно по этому диапазону, без риска
#     задеть наполнение стенда.
WPROBE_ID_BASE=900000000
PROBE_ROWS_CLEANED=0
POLL_INTERVAL=1        # секунд между опросами статуса job'а и пробным запросом
MAX_POLL_ITERS=1200     # верхняя граница ожидания (1200 x 1с = 20 минут); живой прогон
                        # с wal_level=logical занимал существенно меньше — запас на
                        # случай медленного хоста, а не расчёт на использование до конца

log()  { echo "[rebalance] $*" >&2; }
fail() { echo "[rebalance] ОШИБКА: $*" >&2; exit 1; }

psql_c() {
  docker exec -i citus-coord psql -X -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" "$@"
}
psql_at() { # $1 = один SQL-запрос, ON_ERROR_STOP включён, вывод без заголовков/рамки
  docker exec -i citus-coord psql -X -At -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" -c "$1"
}

# --- Измерение распределения: общие функции ----------------------------------
#
# Один и тот же способ счёта используется на всех трёх снимках (до добавления,
# после добавления, после ребаланса) — pg_dist_placement, отфильтрованный по
# shardstate = 1, присоединённый к pg_dist_node по groupid. Это ГЛАВНАЯ
# измеряемая величина артефакта.

distribution_table() {
  psql_c -c "SELECT n.nodename, count(*) AS placements
               FROM pg_dist_placement p
               JOIN pg_dist_node n ON n.groupid = p.groupid
              WHERE p.shardstate = 1
              GROUP BY n.nodename
              ORDER BY n.nodename;"
}

node_placements() { # $1 = nodename
  psql_at "SELECT count(*) FROM pg_dist_placement p JOIN pg_dist_node n ON n.groupid = p.groupid WHERE p.shardstate = 1 AND n.nodename = '$1';"
}

total_placements() {
  psql_at "SELECT count(*) FROM pg_dist_placement WHERE shardstate = 1;"
}

# Размещения ТОЛЬКО распределённых таблиц (без референсных). Нужны отдельно,
# потому что инварианты у двух видов разные: у распределённых число размещений
# при ребалансе не меняется (шарды переезжают), у референсных — растёт вместе с
# числом ВОРКЕРОВ (копия на каждом воркере; на координаторе её нет).
dist_placements() {
  psql_at "SELECT count(*) FROM pg_dist_placement p
             JOIN pg_dist_shard s ON s.shardid = p.shardid
             JOIN citus_tables c ON c.table_name = s.logicalrelid
            WHERE p.shardstate = 1 AND c.citus_table_type = 'distributed';"
}

carriers_placements_count() {
  psql_at "SELECT count(*) FROM pg_dist_shard s JOIN pg_dist_placement p ON p.shardid = s.shardid WHERE s.logicalrelid = 'carriers'::regclass AND p.shardstate = 1;"
}

carriers_placements_nodes() {
  psql_at "SELECT string_agg(n.nodename, ',' ORDER BY n.nodename) FROM pg_dist_shard s JOIN pg_dist_placement p ON p.shardid = s.shardid JOIN pg_dist_node n ON n.groupid = p.groupid WHERE s.logicalrelid = 'carriers'::regclass AND p.shardstate = 1;"
}

active_worker_count() {
  psql_at "SELECT count(*) FROM pg_dist_node WHERE isactive AND noderole = 'primary' AND groupid <> 0;"
}

active_worker_names() {
  psql_at "SELECT string_agg(nodename, ',' ORDER BY nodename) FROM pg_dist_node WHERE isactive AND noderole = 'primary' AND groupid <> 0;"
}

wait_healthy() { # $1 = имя контейнера, $2 = попыток (по 2с)
  local name="$1" tries="${2:-60}" st
  for _ in $(seq 1 "$tries"); do
    if ! docker inspect "$name" >/dev/null 2>&1; then
      fail "контейнер $name не создан — docker compose up не прошёл (см. docker compose -f $COMPOSE_FILE logs $name)"
    fi
    st="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$name")"
    if [ "$st" = healthy ]; then
      log "$name: healthy"
      return 0
    fi
    sleep 2
  done
  fail "контейнер $name не стал healthy за отведённое время (см. docker compose -f $COMPOSE_FILE logs $name)"
}

# --- Уборка: возврат кластера к ДВУМ воркерам ---------------------------------
#
# Идемпотентна: проверяет фактическое состояние перед каждым шагом, поэтому
# безопасна и как обычный вызов в конце main-флоу, и как аварийный EXIT-trap
# (Ctrl+C, любая ошибка под set -e). CLEANUP_DONE не даёт trap повторно
# гонять уже выполненную (успешную) уборку.

CLEANUP_DONE=0

cleanup_worker3() {
  if [ "$CLEANUP_DONE" = "1" ]; then
    return 0
  fi

  local registered
  registered="$(docker exec -i citus-coord psql -X -At -U "$DB_USER" -d "$DB_NAME" -c "SELECT count(*) FROM pg_dist_node WHERE nodename = 'citus-w3' AND isactive;" 2>/dev/null || echo "")"

  if [ "$registered" = "1" ]; then
    # ⚠️ ПОРЯДОК ЗДЕСЬ КРИТИЧЕН, и первая редакция уборки его нарушала: стенд
    # оставался на трёх узлах, а соседний reference-demo.sh (он берёт число
    # воркеров из pg_dist_node и ждёт столько же размещений референсной
    # таблицы) начинал падать.
    #
    # Два свойства Citus, которые вместе дают клинч:
    #   1. citus_drain_node ставит ФОНОВУЮ задачу и возвращает управление
    #      немедленно. Вызванный сразу за ним citus_remove_node отказывает:
    #      шарды ещё на узле.
    #   2. Слив помечает узел shouldhaveshards=false. Если в этот момент жив
    #      ребаланс, запланировавший перенос шардов НА этот узел, его задачи
    #      начинают падать с «Moving shards to a node that shouldn't have a
    #      shard is not supported» и блокируют слив. Наблюдалось на Citus 14.1:
    #      задача висела в состоянии running, 11 повторов за семь минут, не
    #      сдвинув ни одного шарда. Сколько повторов задача сделала бы дальше,
    #      здесь не проверялось — наблюдение прервано вручную.
    #
    # Отсюда порядок: погасить незавершённые задачи -> слить -> ДОЖДАТЬСЯ
    # фактического опустошения -> снять узел.
    log "Уборка: гасим незавершённые фоновые задачи (иначе слив с ними конфликтует)…"
    for jid in $(docker exec -i citus-coord psql -X -At -U "$DB_USER" -d "$DB_NAME" \
                   -c "SELECT job_id FROM pg_dist_background_job WHERE state = 'running';" 2>/dev/null); do
      [ -n "$jid" ] || continue
      log "Уборка: отменяю фоновую задачу job_id=$jid"
      docker exec -i citus-coord psql -X -U "$DB_USER" -d "$DB_NAME" \
        -c "SELECT citus_job_cancel($jid);" >/dev/null 2>&1 || true
    done
    sleep 3

    log "Уборка: дренируем citus-w3 (citus_drain_node)…"
    docker exec -i citus-coord psql -X -U "$DB_USER" -d "$DB_NAME" \
      -c "SELECT citus_drain_node('citus-w3', 5432, shard_transfer_mode => 'force_logical');" >/dev/null 2>&1 \
      || log "ВНИМАНИЕ: citus_drain_node завершился с ошибкой — всё равно ждём опустошения."

    # Ждём ФАКТИЧЕСКОГО опустошения узла ОТ РАСПРЕДЕЛЁННЫХ таблиц — не от
    # ВСЕХ размещений. citus_drain_node копию РЕФЕРЕНСНОЙ таблицы (carriers)
    # не трогает: она остаётся на узле до citus_remove_node, который снимает
    # узел вместе с её последним размещением. Это установленный факт (см.
    # FIXTURES.md, артефакт 5), а не разовая случайность прогона.
    #
    # ⚠️ ЖИВАЯ НАХОДКА ревью: прежняя версия ждала нуля ВСЕХ размещений
    # (включая референсные) — и ноль был структурно недостижим, пока copy
    # carriers жива на узле. Цикл выжигал все 60 попыток × 5с = 300с
    # (пять минут) на КАЖДОМ штатном прогоне, хотя реальное опустошение от
    # распределённых шардов занимает секунды. Считаем только размещения
    # распределённых таблиц — референсная копия дождётся citus_remove_node.
    local left="1" i=0
    while [ "$i" -lt 60 ]; do
      left="$(docker exec -i citus-coord psql -X -At -U "$DB_USER" -d "$DB_NAME" \
        -c "SELECT count(*) FROM pg_dist_placement p
              JOIN pg_dist_node n ON n.groupid = p.groupid
              JOIN pg_dist_shard s ON s.shardid = p.shardid
              JOIN citus_tables c ON c.table_name = s.logicalrelid
             WHERE n.nodename = 'citus-w3' AND p.shardstate = 1
               AND c.citus_table_type = 'distributed';" 2>/dev/null || echo "1")"
      [ "${left:-1}" = "0" ] && break
      i=$((i + 1))
      sleep 5
    done
    if [ "${left:-1}" != "0" ]; then
      log "ВНИМАНИЕ: на citus-w3 осталось размещений распределённых таблиц: ${left} — снятие узла может отказать."
    fi

    # ⚠️ ЖИВАЯ НАХОДКА (обнаружена при проверке этого исправления): слив
    # оставляет в pg_dist_cleanup ОТЛОЖЕННУЮ запись на удаление старой копии
    # шарда на citus-w3 (policy_type=DEFERRED) — сам перенос это не убирает.
    # Проверено на Citus 14.1 в этом сценарии: если снять регистрацию узла и
    # удалить контейнер ДО того, как запись обработана, она остаётся
    # неразобранной — node_group_id в ней указывает на уже несуществующий
    # узел, и явный вызов citus_cleanup_orphaned_resources() после этого её
    # не почистил (pg_dist_cleanup оставался = 1). Сработает ли какой-то иной
    # путь уборки, здесь не проверялось. Поэтому вызываем очистку ЗДЕСЬ, пока
    # citus-w3 ещё зарегистрирован и его контейнер жив, — в проверенном
    # сценарии это работающее окно.
    log "Уборка: принудительно разбираем отложенные записи pg_dist_cleanup, пока citus-w3 ещё жив…"
    docker exec -i citus-coord psql -X -U "$DB_USER" -d "$DB_NAME" -c "CALL citus_cleanup_orphaned_resources();" >/dev/null 2>&1 \
      || log "ВНИМАНИЕ: citus_cleanup_orphaned_resources() завершился с ошибкой — проверьте pg_dist_cleanup вручную."

    log "Уборка: снимаем регистрацию citus-w3 (citus_remove_node)…"
    docker exec -i citus-coord psql -X -U "$DB_USER" -d "$DB_NAME" -c "SELECT citus_remove_node('citus-w3', 5432);" >/dev/null 2>&1 \
      || log "ВНИМАНИЕ: citus_remove_node('citus-w3') завершился с ошибкой — проверьте pg_dist_node вручную."
  fi

  log "Уборка: останавливаем и удаляем контейнер citus-w3…"
  "${COMPOSE[@]}" --profile grow rm -sf worker3 >/dev/null 2>&1 || true
}

# Уборка строк, вставленных ПИШУЩЕЙ пробой. Идемпотентна, вызывается и явно
# (сразу после цикла опроса), и из EXIT-trap — иначе аварийное завершение
# оставило бы в orders лишние строки, и соседние артефакты (они считают по
# 4000 заказов) начали бы давать другие числа.
cleanup_probe_rows() {
  if [ "$PROBE_ROWS_CLEANED" = "1" ] || [ -z "$PROBE_KEY" ]; then
    return 0
  fi
  docker exec -i citus-coord psql -X -U "$DB_USER" -d "$DB_NAME" \
    -c "DELETE FROM orders WHERE customer_id = $PROBE_KEY AND order_id >= $WPROBE_ID_BASE;" >/dev/null 2>&1 \
    || log "ВНИМАНИЕ: не удалось удалить строки пишущей пробы — проверьте orders вручную."
  PROBE_ROWS_CLEANED=1
}

cleanup_all() {
  cleanup_probe_rows
  cleanup_worker3
}

trap cleanup_all EXIT

# --- Preflight -----------------------------------------------------------------

command -v docker >/dev/null 2>&1 || fail "docker не найден в PATH."
docker info >/dev/null 2>&1 || fail "docker недоступен (демон не отвечает)."

state="$(docker inspect --format '{{.State.Status}}' citus-coord 2>/dev/null || true)"
[ "$state" = "running" ] || fail "контейнер citus-coord не запущен. Сначала: bash scripts/up.sh"

log "Preflight: проверяем, что кластер сейчас на ДВУХ воркерах и citus-w3 не поднят…"

nodes_now="$(active_worker_names)"
[ "$nodes_now" = "citus-w1,citus-w2" ] \
  || fail "pg_dist_node сейчас = '$nodes_now', ожидали ровно 'citus-w1,citus-w2'. Стенд не в исходном состоянии (возможно, прошлый прогон этого скрипта убрался не полностью) — проверьте pg_dist_node и docker ps вручную перед повторным запуском."


# ЖИВАЯ НАХОДКА: `docker inspect --format ... 2>/dev/null || echo missing` на
# этом хосте отдаёт для НЕСУЩЕСТВУЮЩЕГО контейнера пустую строку на stdout
# ДО ошибки — `|| echo missing` тогда даёт значение "\nmissing" вместо
# чистого "missing", и сравнение с "missing" ломается. Поэтому проверка
# существования — ОТДЕЛЬНОЙ командой (код возврата `docker inspect`), а не
# парсингом текста формата.
if docker inspect citus-w3 >/dev/null 2>&1; then
  w3_status="$(docker inspect --format '{{.State.Status}}' citus-w3)"
  fail "контейнер citus-w3 уже существует (статус: $w3_status) — похоже, прошлый прогон не убрался. Уберите вручную: docker compose -f $COMPOSE_FILE --profile grow rm -sf worker3"
fi

FOUND_FUNCS="$(psql_at "SELECT string_agg(proname, ',' ORDER BY proname) FROM pg_proc WHERE proname IN ('citus_add_node','citus_rebalance_start','citus_rebalance_status','citus_drain_node','citus_remove_node','get_rebalance_progress');")"
for fn in citus_add_node citus_rebalance_start citus_rebalance_status citus_drain_node citus_remove_node get_rebalance_progress; do
  case ",$FOUND_FUNCS," in
    *",$fn,"*) : ;;
    *) fail "функция $fn отсутствует в этой версии Citus (ожидали по факту Task 1/api-probe.sh) — конструкция артефакта не подходит, нужен другой план." ;;
  esac
done
log "Все нужные функции ребаланса на месте: $FOUND_FUNCS"

dist_count="$(psql_at "SELECT count(*) FROM citus_tables WHERE table_name::text IN ('customers','orders','shipments','carriers');")"
[ "$dist_count" = "4" ] || fail "ожидали 4 распределённые/референсные таблицы в citus_tables, нашли: $dist_count. Сначала: bash scripts/up.sh"

orders_rows="$(psql_at "SELECT count(*) FROM orders;")"
[ "${orders_rows:-0}" -gt 0 ] || fail "таблица orders пуста (нашли: $orders_rows) — пробный запрос доступности не даст показательного результата. Сначала: bash scripts/up.sh"
log "Наполнение на месте: orders = $orders_rows строк. Ключ пробы будет выбран из фактического плана перемещений (шаг 2)."

RUN_LOG=()   # накапливаем результаты для итоговой сводки/самопроверки этого прогона

# ==============================================================================
# Шаг 1: распределение ДО добавления узла
# ==============================================================================

echo
echo "================================================================"
echo " Шаг 1 / 6: распределение шардов ДО добавления узла"
echo "================================================================"
distribution_table
BEFORE_W1="$(node_placements citus-w1)"
BEFORE_W2="$(node_placements citus-w2)"
BEFORE_TOTAL="$(total_placements)"
BEFORE_DIST="$(dist_placements)"
BEFORE_CARRIERS_N="$(carriers_placements_count)"
BEFORE_CARRIERS_NODES="$(carriers_placements_nodes)"
log "citus-w1=$BEFORE_W1, citus-w2=$BEFORE_W2, итого размещений=$BEFORE_TOTAL. carriers: $BEFORE_CARRIERS_N размещений на [$BEFORE_CARRIERS_NODES]."

[ "$BEFORE_W1" -gt 0 ] && [ "$BEFORE_W2" -gt 0 ] \
  || fail "на одном из исходных воркеров 0 активных размещений (w1=$BEFORE_W1, w2=$BEFORE_W2) — стенд не наполнен как ожидалось."

# ==============================================================================
# Шаг 2: поднимаем третий воркер и регистрируем
# ==============================================================================

echo
echo "================================================================"
echo " Шаг 2 / 6: поднимаем citus-w3 (compose-профиль grow) и регистрируем"
echo "================================================================"

log "docker compose --profile grow up -d worker3…"
"${COMPOSE[@]}" --profile grow up -d worker3
wait_healthy citus-w3 60

log "citus_add_node('citus-w3', 5432)…"
psql_c -c "SELECT citus_add_node('citus-w3', 5432);" >/dev/null

nodes_after_add="$(active_worker_names)"
[ "$nodes_after_add" = "citus-w1,citus-w2,citus-w3" ] \
  || fail "после citus_add_node в pg_dist_node узлы = '$nodes_after_add', ожидали 'citus-w1,citus-w2,citus-w3'."
log "Зарегистрированы активные воркеры: $nodes_after_add."

# ------------------------------------------------------------------------------
# Выбор ключа пробы ПО ФАКТИЧЕСКОМУ ПЛАНУ ПЕРЕМЕЩЕНИЙ
#
# get_rebalance_table_shards_plan() — сигнатура снята на месте (\df+):
#   TABLE(table_name regclass, shardid bigint, shard_size bigint,
#         sourcename text, sourceport integer, targetname text, targetport integer)
# Это ровно тот план, который затем исполнит citus_rebalance_start().
#
# Проба обязана бить в шард, КОТОРЫЙ ПЕРЕЕЗЖАЕТ. Иначе демонстрация показывает
# не то, что заявляет.
# ------------------------------------------------------------------------------
echo
echo "----------------------------------------------------------------"
echo " План перемещений (get_rebalance_table_shards_plan) — до старта ребаланса"
echo "----------------------------------------------------------------"
psql_c -c "SELECT table_name, shardid, sourcename, sourceport, targetname, targetport FROM get_rebalance_table_shards_plan();"

MOVING_ORDERS_SHARDS="$(psql_at "SELECT string_agg(shardid::text, ',' ORDER BY shardid) FROM get_rebalance_table_shards_plan() WHERE table_name::text = 'orders';")"
if [ -z "$MOVING_ORDERS_SHARDS" ]; then
  ALL_MOVING="$(psql_at "SELECT coalesce(string_agg(table_name::text || ':' || shardid::text, ', ' ORDER BY table_name::text, shardid), '(план пуст)') FROM get_rebalance_table_shards_plan();")"
  fail "в плане перемещений НЕТ ни одного шарда таблицы orders (весь план: $ALL_MOVING). Проба по ключу шардирования orders в этом случае физически не может попасть в переезжающий шард — артефакт не может доказать заявленное и обязан упасть, а не молча продолжить."
fi
log "Шарды orders в плане перемещений: $MOVING_ORDERS_SHARDS."

# Подбираем customer_id, чей шард входит в план и у которого есть заказы.
PROBE_KEY="$(psql_at "
  SELECT g.customer_id
    FROM (SELECT customer_id, count(*) AS n FROM orders GROUP BY customer_id) g
   WHERE get_shard_id_for_distribution_column('orders', g.customer_id)
         IN ($MOVING_ORDERS_SHARDS)
   ORDER BY g.n DESC, g.customer_id
   LIMIT 1;")"
[ -n "$PROBE_KEY" ] || fail "не нашёлся ни один customer_id с заказами, чей шард orders входит в план перемещений ($MOVING_ORDERS_SHARDS). Проба била бы в неподвижный шард — это подмена доказательства, поэтому провал."

PROBE_SHARD="$(psql_at "SELECT get_shard_id_for_distribution_column('orders', $PROBE_KEY);")"
# Ожидание читающей пробы намеренно ограничено order_id < WPROBE_ID_BASE:
# в этот же ключ во время ребаланса пишет ПИШУЩАЯ проба, и без ограничения
# читающая начала бы «отказывать» от собственных вставок соседней пробы.
target_n="$(psql_at "SELECT count(*) FROM orders WHERE customer_id = $PROBE_KEY AND order_id < $WPROBE_ID_BASE;")"

# Исходное наполнение — снимок ДО вставок пишущей пробы. Гейт в конце сверяет
# с ним фактическое число строк, чтобы стенд гарантированно вернулся к
# исходному состоянию (штатно 4000 заказов).
ORDERS_TOTAL_BEFORE="$(psql_at "SELECT count(*) FROM orders;")"
PROBE_ROWS_LEFTOVER_BEFORE="$(psql_at "SELECT count(*) FROM orders WHERE order_id >= $WPROBE_ID_BASE;")"
[ "${PROBE_ROWS_LEFTOVER_BEFORE:-0}" = "0" ] \
  || fail "в orders уже есть ${PROBE_ROWS_LEFTOVER_BEFORE} строк(и) с order_id >= $WPROBE_ID_BASE — это остатки пишущей пробы прошлого прогона. Стенд не в исходном состоянии; удалите их (DELETE FROM orders WHERE order_id >= $WPROBE_ID_BASE) и повторите."

# --- ГЕЙТ-ИНВАРИАНТ: шард пробы ОБЯЗАН входить в множество перемещаемых -------
case ",$MOVING_ORDERS_SHARDS," in
  *",$PROBE_SHARD,"*) : ;;
  *) fail "ГЕЙТ НЕ ПРОЙДЕН: шард пробы orders=$PROBE_SHARD (customer_id=$PROBE_KEY) НЕ входит в план перемещений [$MOVING_ORDERS_SHARDS]. Успешные пробы в этом случае доказывали бы доступность НЕПОДВИЖНОГО шарда во время фоновой задачи, а не чтение данных в процессе их переноса. Демонстрация обязана объявить провал." ;;
esac
[ "${target_n:-0}" -gt 0 ] || fail "у customer_id=$PROBE_KEY нет заказов (нашли: $target_n) — проба вернёт 0 и не даст показательного результата."

PROBE_MOVE_ROUTE="$(psql_at "SELECT sourcename || ':' || sourceport || ' -> ' || targetname || ':' || targetport FROM get_rebalance_table_shards_plan() WHERE table_name::text = 'orders' AND shardid = $PROBE_SHARD;")"
# Узел, на который ПЛАН обещает переложить шард пробы. Запоминаем отдельно:
# после завершения job'а второй гейт сверит это обещание с ФАКТОМ по
# pg_dist_placement (см. «пост-гейт» после шага 5).
PROBE_PLAN_TARGET="$(psql_at "SELECT targetname FROM get_rebalance_table_shards_plan() WHERE table_name::text = 'orders' AND shardid = $PROBE_SHARD;")"
[ -n "$PROBE_PLAN_TARGET" ] || fail "не удалось определить target-узел для шарда пробы orders=$PROBE_SHARD из плана перемещений."
echo
echo "[ГЕЙТ OK] Ключ пробы выбран ПО ПЛАНУ, а не константой:"
echo "  customer_id = $PROBE_KEY  ->  orders shardid = $PROBE_SHARD"
echo "  Шард $PROBE_SHARD ВХОДИТ в план перемещений: $PROBE_MOVE_ROUTE"
echo "  Перемещаемые шарды orders: [$MOVING_ORDERS_SHARDS]"
echo "  Ожидаемый ответ пробы: $target_n"
log "Проба бьёт ИМЕННО в переезжающий шард orders=$PROBE_SHARD ($PROBE_MOVE_ROUTE); ожидаемый ответ $target_n."

# ==============================================================================
# Шаг 3: распределение ПОСЛЕ добавления, ДО ребаланса — падающий вариант
# ==============================================================================

echo
echo "================================================================"
echo " Шаг 3 / 6: распределение ПОСЛЕ добавления citus-w3, ДО ребаланса"
echo "================================================================"
echo "Падающий вариант: если бы добавление узла само по себе перераспределяло"
echo "шарды, citus-w3 уже показал бы ненулевое число размещений — и ребаланс"
echo "был бы не нужен. Проверяется ниже."
distribution_table
AFTERADD_W1="$(node_placements citus-w1)"
AFTERADD_W2="$(node_placements citus-w2)"
AFTERADD_W3="$(node_placements citus-w3)"
AFTERADD_TOTAL="$(total_placements)"
AFTERADD_CARRIERS_N="$(carriers_placements_count)"
AFTERADD_CARRIERS_NODES="$(carriers_placements_nodes)"
log "citus-w1=$AFTERADD_W1, citus-w2=$AFTERADD_W2, citus-w3=$AFTERADD_W3, итого=$AFTERADD_TOTAL. carriers: $AFTERADD_CARRIERS_N размещений на [$AFTERADD_CARRIERS_NODES]."

# ==============================================================================
# Шаг 4+6 (совместно): запускаем ребаланс, во время выполнения периодически
# опрашиваем доступность запроса по ключу шардирования
# ==============================================================================

echo
echo "================================================================"
echo " Шаг 4 / 6: запускаем ребаланс (citus_rebalance_start) — асинхронно"
echo "================================================================"

REBALANCE_NOTICE="$(docker exec -i citus-coord psql -X -U "$DB_USER" -d "$DB_NAME" -c "SELECT citus_rebalance_start();" 2>&1 >/dev/null || true)"
JOB_ID="$(psql_at "SELECT max(job_id) FROM pg_dist_background_job;")"
[ -n "$JOB_ID" ] && [ "$JOB_ID" -gt 0 ] 2>/dev/null || fail "не удалось получить job_id ребаланса из pg_dist_background_job. Вывод citus_rebalance_start: $REBALANCE_NOTICE"
log "Ребаланс запущен: job_id=$JOB_ID. ($REBALANCE_NOTICE)"

echo
echo "================================================================"
echo " Шаг 6 / 6 (одновременно с шагом 5): пробы ЧТЕНИЯ и ЗАПИСИ по ключу шардирования"
echo "  во время выполнения ребаланса — фиксируем факт, а не предположение"
echo "================================================================"
echo "Проб ДВЕ, обе бьют в ОДИН и тот же переезжающий шард orders=$PROBE_SHARD:"
echo "  1. ЧИТАЮЩАЯ: SELECT count(*) FROM orders"
echo "               WHERE customer_id = $PROBE_KEY AND order_id < $WPROBE_ID_BASE;  (ожидаем: $target_n)"
echo "  2. ПИШУЩАЯ:  INSERT INTO orders (customer_id, order_id, total)"
echo "               VALUES ($PROBE_KEY, $WPROBE_ID_BASE + N, ...);"
echo "               + проверка ВИДИМОСТИ: последующее чтение обязано вернуть увеличенное количество."
echo "Этот customer_id лежит в шарде orders=$PROBE_SHARD, и этот шард ПЕРЕЕЗЖАЕТ:"
echo "  $PROBE_MOVE_ROUTE"
echo "То есть пробы бьют в ПЕРЕЕЗЖАЮЩИЙ шард, а не в неподвижный."
echo
echo "ГРАНИЦА: доказано, что шард входил в план и после job'а оказался на"
echo "target-узле, а пробы шли всё время работы job'а. НЕ доказано, что хотя бы"
echo "одна проба пришлась ровно на короткое окно переключения ИМЕННО этого"
echo "шарда: скрипт следит за состоянием job'а целиком, а не за фазой"
echo "конкретного шарда. Пробы могли уложиться до или после его cutover."
echo "Для более сильного утверждения нужна корреляция с get_rebalance_progress()"
echo "по выбранному шарду — здесь её нет."
echo
echo "ЗАЧЕМ ПИШУЩАЯ. Режимы переноса Citus противопоставляются по ЗАПИСЯМ:"
echo "логическая репликация ('auto') позволяет избежать блокировки записей,"
echo "'block_writes' копирует шард через COPY с их блокировкой. Чтение доступно"
echo "в ОБОИХ режимах, поэтому одна читающая проба преимущества 'auto' показать"
echo "не может — успешна она была бы в любом случае. Пишущая проба проверяет"
echo "УСПЕШНОЕ ЗАВЕРШЕНИЕ INSERT и ВИДИМОСТЬ записанного."
echo
echo "ЧЕГО ОНА НЕ ОПРЕДЕЛЯЕТ: наличие краткой блокировки. Заблокированный"
echo "INSERT обычно ЖДЁТ и потом завершается успешно — то есть проба его"
echo "засчитает. statement_timeout здесь не задан, поэтому долгая блокировка"
echo "не дала бы ошибку, а подвесила бы docker exec. И отказ сам по себе"
echo "блокировку не доказывает: для этого нужен разбор текста ошибки или"
echo "pg_locks, чего стенд не делает. Длительности печатаются сырыми и с"
echo "фазой переноса конкретного шарда не соотнесены."
echo

PROBE_ITERS=0
PROBE_OK=0
PROBE_FAIL=0
PROBE_MAX_MS=0
PROBE_FAIL_DETAILS=()

# Пишущая проба: счётчики отдельные от читающей.
WPROBE_ITERS=0        # сколько пишущих проб выполнено
WPROBE_OK=0           # INSERT прошёл И записанное сразу видно
WPROBE_FAIL=0         # INSERT отказал ИЛИ записанное не подтвердилось чтением
WPROBE_INSERTED=0     # сколько строк фактически лежит в orders по диапазону пробы
WPROBE_MAX_MS=0       # максимальная длительность одного INSERT
WPROBE_MAX_AT=""      # на какой итерации/в каком состоянии job'а был этот максимум
WPROBE_FAIL_DETAILS=()

FINAL_STATE=""

UNKNOWN_STREAK=0    # подряд идущих нечитаемых/незнакомых состояний
UNKNOWN_LIMIT=5     # после стольких подряд прогон объявляется недостоверным

# Длительность окна опроса измеряется ЧАСАМИ, а не выводится из числа
# итераций: итерация — это sleep 1 ПЛЮС три docker exec, поэтому «N проб ≈ N
# секунд» неверно. Левая граница — момент перед первым опросом состояния,
# правая — момент первого наблюдения 'finished'; правая известна с точностью
# до интервала опроса.
POLL_START_MS="$(date +%s%3N)"
POLL_WALL_MS=0

i=0
while :; do
  i=$((i + 1))
  state="$(psql_at "SELECT state FROM citus_rebalance_status() WHERE job_id = $JOB_ID;" 2>/dev/null || echo "?")"

  # --- ТЕРМИНАЛЬНОЕ СОСТОЯНИЕ ПРОВЕРЯЕТСЯ ДО ПРОБ -----------------------------
  # Раньше эта проверка стояла ПОСЛЕ проб, и последняя итерация успевала
  # выполнить чтение и запись уже при job.state=finished. Такие пробы
  # попадали в счётчики и в максимумы наравне с остальными — то есть в
  # статистику «во время ребаланса» примешивались отсчёты, снятые уже ПОСЛЕ
  # него. На выборке это давало +1 пробу каждого вида за прогон, а
  # опубликованный максимум мог относиться к моменту, когда переносить было
  # уже нечего. Теперь цикл выходит до проб, и все засчитанные пробы сняты
  # при НЕзавершённом job'е.
  # БЕЛЫЙ СПИСОК АКТИВНЫХ СОСТОЯНИЙ. Пробы выполняются, только если состояние
  # ЯВНО одно из активных. Раньше сбой запроса состояния превращался в "?",
  # а "?" не считался терминальным — значит скрипт шёл выполнять пробы. Если
  # запрос состояния отказал уже ПОСЛЕ завершения job'а (или вернул пустоту),
  # посттерминальная проба снова попала бы в статистику — ровно тот дефект,
  # ради устранения которого выход из цикла перенесён до проб. Неизвестное
  # состояние теперь НЕ даёт права на пробу: цикл переспрашивает, а после
  # UNKNOWN_LIMIT подряд объявляет прогон недостоверным.
  case "$state" in
    finished)
      FINAL_STATE="$state"
      POLL_WALL_MS=$(( $(date +%s%3N) - POLL_START_MS ))
      break
      ;;
    failed|cancelled|cancelling|failing)
      FINAL_STATE="$state"
      POLL_WALL_MS=$(( $(date +%s%3N) - POLL_START_MS ))
      log "ВНИМАНИЕ: job ребаланса перешёл в состояние '$state' — это не 'finished', цикл прерван."
      break
      ;;
    scheduled|running)
      UNKNOWN_STREAK=0
      ;;
    *)
      UNKNOWN_STREAK=$((UNKNOWN_STREAK + 1))
      log "ВНИМАНИЕ: состояние job'а не прочиталось или незнакомо (получили '$state'), попытка $UNKNOWN_STREAK из $UNKNOWN_LIMIT — пробы на этой итерации НЕ выполняются (иначе они могли бы оказаться посттерминальными)."
      if [ "$UNKNOWN_STREAK" -ge "$UNKNOWN_LIMIT" ]; then
        fail "состояние job'а $JOB_ID не удалось прочитать $UNKNOWN_LIMIT раз подряд (последнее значение: '$state'). Неизвестно, идёт ли ещё ребаланс, поэтому засчитывать пробы как снятые «во время переноса» нельзя — прогон объявлен недостоверным."
      fi
      sleep "$POLL_INTERVAL"
      continue
      ;;
  esac

  # --- ЧИТАЮЩАЯ проба ---------------------------------------------------------
  # ⚠️ rc снимается через `&& rc=0 || rc=$?`, а НЕ через `$?` после присваивания:
  # при `set -e` неуспешное присваивание из подстановки команд уронило бы скрипт
  # ровно в тот момент, ради которого проба и заведена, — и отказ пробы вместо
  # записи в результат превратился бы в обрыв демонстрации.
  probe_start_ms="$(date +%s%3N)"
  probe_out="$(docker exec -i citus-coord psql -X -At -U "$DB_USER" -d "$DB_NAME" -c "SELECT count(*) FROM orders WHERE customer_id = $PROBE_KEY AND order_id < $WPROBE_ID_BASE;" 2>&1)" && probe_rc=0 || probe_rc=$?
  probe_end_ms="$(date +%s%3N)"
  probe_ms=$(( probe_end_ms - probe_start_ms ))
  # if, а не `[ ... ] && ...`: при set -e ложное условие в && -списке как
  # самостоятельной команде даёт ненулевой код всей строки и роняет скрипт.
  if [ "$probe_ms" -gt "$PROBE_MAX_MS" ]; then PROBE_MAX_MS="$probe_ms"; fi

  PROBE_ITERS=$((PROBE_ITERS + 1))
  if [ "$probe_rc" = "0" ] && [ "$probe_out" = "$target_n" ]; then
    PROBE_OK=$((PROBE_OK + 1))
    ok_mark="OK"
  else
    PROBE_FAIL=$((PROBE_FAIL + 1))
    ok_mark="FAIL"
    PROBE_FAIL_DETAILS+=("[$i] state=$state rc=$probe_rc вывод: $probe_out")
  fi

  # --- ПИШУЩАЯ проба ----------------------------------------------------------
  # INSERT в ТОТ ЖЕ переезжающий шард (тот же PROBE_KEY, значит та же маршрутизация),
  # затем проверка ВИДИМОСТИ: чтение диапазона пробы обязано вернуть увеличенное
  # количество. Проба доказывает не «INSERT не отдал ошибку», а «записанное видно».
  w_id=$(( WPROBE_ID_BASE + i ))
  w_start_ms="$(date +%s%3N)"
  w_out="$(docker exec -i citus-coord psql -X -At -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" \
            -c "INSERT INTO orders (customer_id, order_id, total) VALUES ($PROBE_KEY, $w_id, 1.00);" 2>&1)" && w_rc=0 || w_rc=$?
  w_end_ms="$(date +%s%3N)"
  w_ms=$(( w_end_ms - w_start_ms ))
  WPROBE_ITERS=$((WPROBE_ITERS + 1))
  if [ "$w_ms" -gt "$WPROBE_MAX_MS" ]; then
    WPROBE_MAX_MS="$w_ms"
    WPROBE_MAX_AT="итерация $i, job.state=$state"
  fi

  w_expected=$(( WPROBE_INSERTED + 1 ))   # ожидание при успешной вставке
  w_seen="$(docker exec -i citus-coord psql -X -At -U "$DB_USER" -d "$DB_NAME" \
            -c "SELECT count(*) FROM orders WHERE customer_id = $PROBE_KEY AND order_id >= $WPROBE_ID_BASE;" 2>&1)" && w_seen_rc=0 || w_seen_rc=$?

  if [ "$w_rc" = "0" ] && [ "$w_seen_rc" = "0" ] && [ "$w_seen" = "$w_expected" ]; then
    WPROBE_OK=$((WPROBE_OK + 1))
    w_mark="OK"
  else
    WPROBE_FAIL=$((WPROBE_FAIL + 1))
    w_mark="FAIL"
    WPROBE_FAIL_DETAILS+=("[$i] state=$state insert_rc=$w_rc (${w_ms} мс) вывод: $w_out | видимость: rc=$w_seen_rc ожидали=$w_expected увидели='$w_seen'")
  fi

  # Пересинхронизация счётчика по ФАКТУ, а не по намерению. Если INSERT отдал
  # ошибку клиенту, но на самом деле закоммитился (или наоборот), фиксированный
  # счётчик разъехался бы с таблицей и все последующие проверки видимости
  # посыпались бы каскадом ложных отказов. Отказ выше уже записан — здесь
  # счётчик приводится к тому, что реально лежит в таблице.
  case "$w_seen" in
    ''|*[!0-9]*) : ;;                       # чтение не удалось — счётчик не трогаем
    *) WPROBE_INSERTED="$w_seen" ;;
  esac

  if [ $(( i % 5 )) -eq 1 ] || [ "$ok_mark" = "FAIL" ] || [ "$w_mark" = "FAIL" ]; then
    echo "  [$i] job.state=$state | чтение: $ok_mark (rc=$probe_rc, ${probe_ms} мс, ответ='$probe_out') | запись: $w_mark (rc=$w_rc, ${w_ms} мс, видно='$w_seen'/ожидали='$w_expected')"
  fi

  if [ "$i" -ge "$MAX_POLL_ITERS" ]; then
    FINAL_STATE="timeout(state=$state)"
    POLL_WALL_MS=$(( $(date +%s%3N) - POLL_START_MS ))
    log "ВНИМАНИЕ: ребаланс не завершился за $((MAX_POLL_ITERS * POLL_INTERVAL))с (последнее известное состояние: $state) — прекращаем опрос по таймауту."
    break
  fi

  sleep "$POLL_INTERVAL"
done

echo
log "Опрос завершён. Итоговое состояние job'а: $FINAL_STATE."
log "ЧТЕНИЕ: $PROBE_ITERS попыток, успешных=$PROBE_OK, неуспешных=$PROBE_FAIL, максимум ${PROBE_MAX_MS} мс."
log "ЗАПИСЬ: $WPROBE_ITERS попыток, успешных=$WPROBE_OK, неуспешных=$WPROBE_FAIL, максимум ${WPROBE_MAX_MS} мс (${WPROBE_MAX_AT:-—})."
if [ "$PROBE_FAIL" -gt 0 ]; then
  log "Детали неуспешных ЧИТАЮЩИХ проб (записаны как есть, без сглаживания):"
  for d in "${PROBE_FAIL_DETAILS[@]}"; do
    log "  $d"
  done
fi
if [ "$WPROBE_FAIL" -gt 0 ]; then
  log "Детали неуспешных ПИШУЩИХ проб (записаны как есть, без сглаживания):"
  for d in "${WPROBE_FAIL_DETAILS[@]}"; do
    log "  $d"
  done
fi

# Строки пишущей пробы убираются СРАЗУ после цикла: дальше идут снимки и уборка
# узла, и наполнение стенда к этому моменту обязано быть исходным.
ORDERS_WITH_PROBE_ROWS="$(psql_at "SELECT count(*) FROM orders;")"
cleanup_probe_rows
ORDERS_TOTAL_AFTER="$(psql_at "SELECT count(*) FROM orders;")"
PROBE_ROWS_LEFTOVER_AFTER="$(psql_at "SELECT count(*) FROM orders WHERE order_id >= $WPROBE_ID_BASE;")"
log "Наполнение orders: было $ORDERS_TOTAL_BEFORE, во время пробы $ORDERS_WITH_PROBE_ROWS, после удаления строк пробы $ORDERS_TOTAL_AFTER (остаток строк пробы: $PROBE_ROWS_LEFTOVER_AFTER)."

# ==============================================================================
# Шаг 5: распределение ПОСЛЕ ребаланса
# ==============================================================================

echo
echo "================================================================"
echo " Шаг 5 / 6: распределение шардов ПОСЛЕ ребаланса"
echo "================================================================"
distribution_table
AFTER_W1="$(node_placements citus-w1)"
AFTER_W2="$(node_placements citus-w2)"
AFTER_W3="$(node_placements citus-w3)"
AFTER_TOTAL="$(total_placements)"
AFTER_CARRIERS_N="$(carriers_placements_count)"
AFTER_DIST="$(dist_placements)"
AFTER_NODES_N="$(active_worker_count)"
AFTER_CARRIERS_NODES="$(carriers_placements_nodes)"
log "citus-w1=$AFTER_W1, citus-w2=$AFTER_W2, citus-w3=$AFTER_W3, итого=$AFTER_TOTAL. carriers: $AFTER_CARRIERS_N размещений на [$AFTER_CARRIERS_NODES]."

# ------------------------------------------------------------------------------
# ПОСТ-ГЕЙТ: план обещал — факт обязан подтвердить.
#
# Первый гейт (шаг 2) проверял членство шарда пробы в ПРЕДВАРИТЕЛЬНОМ плане
# get_rebalance_table_shards_plan(). Но между расчётом плана и
# citus_rebalance_start() план теоретически может разойтись с тем, что
# исполнено на самом деле, — и тогда успешные пробы снова доказывали бы не то,
# что заявлено (чтение шарда В ПРОЦЕССЕ ПЕРЕНОСА). Поэтому после завершения
# job'а сверяем ФАКТИЧЕСКОЕ активное размещение (shardstate = 1) шарда пробы
# с target-узлом из плана.
#
# Проверяем именно PROBE_SHARD — шард таблицы orders, в который била проба.
# orders колоцирована с customers (colocation_id = 1), перемещения идут группой
# колокации, и ведущим в плане может стоять шард customers; факт нужен ровно
# про тот шард, который читала проба.
#
# Снимок обязан быть снят ДО уборки: cleanup_worker3 сливает citus-w3 и
# переносит его шарды обратно на w1/w2, после чего этот факт уже не проверить.
# ------------------------------------------------------------------------------
PROBE_ACTUAL_NODES="$(psql_at "
  SELECT coalesce(string_agg(n.nodename, ',' ORDER BY n.nodename), '(нет активных размещений)')
    FROM pg_dist_placement p
    JOIN pg_dist_node n ON n.groupid = p.groupid
   WHERE p.shardid = $PROBE_SHARD AND p.shardstate = 1;")"

echo
echo "----------------------------------------------------------------"
echo " ПОСТ-ГЕЙТ: фактическое размещение шарда пробы после ребаланса"
echo "----------------------------------------------------------------"
echo "  Шард пробы (orders):            $PROBE_SHARD  (customer_id = $PROBE_KEY)"
echo "  План обещал target-узел:        $PROBE_PLAN_TARGET"
echo "  Факт (pg_dist_placement, shardstate=1): $PROBE_ACTUAL_NODES"

if [ "$PROBE_ACTUAL_NODES" = "$PROBE_PLAN_TARGET" ]; then
  POSTGATE_OK=1
  echo "  [ПОСТ-ГЕЙТ OK] План и факт совпали — проба действительно читала шард, который переезжал на $PROBE_PLAN_TARGET."
else
  POSTGATE_OK=0
  echo "  [ПОСТ-ГЕЙТ FAIL] План и факт РАЗОШЛИСЬ."
fi

echo
echo "================================================================"
echo " Сводка распределения по трём снимкам"
echo "================================================================"
printf '%-30s %-10s %-10s %-10s %-10s\n' "Снимок" "citus-w1" "citus-w2" "citus-w3" "Итого"
printf '%-30s %-10s %-10s %-10s %-10s\n' "1. До добавления узла"      "$BEFORE_W1"   "$BEFORE_W2"   "-"            "$BEFORE_TOTAL"
printf '%-30s %-10s %-10s %-10s %-10s\n' "3. После добавления узла"   "$AFTERADD_W1" "$AFTERADD_W2" "$AFTERADD_W3" "$AFTERADD_TOTAL"
printf '%-30s %-10s %-10s %-10s %-10s\n' "5. После ребаланса"         "$AFTER_W1"    "$AFTER_W2"    "$AFTER_W3"    "$AFTER_TOTAL"
echo
printf '%-30s %-10s %-10s\n' "carriers (референсная)" "размещений" "узлы"
printf '%-30s %-10s %-10s\n' "1. До добавления узла"    "$BEFORE_CARRIERS_N"   "$BEFORE_CARRIERS_NODES"
printf '%-30s %-10s %-10s\n' "3. После добавления узла" "$AFTERADD_CARRIERS_N" "$AFTERADD_CARRIERS_NODES"
printf '%-30s %-10s %-10s\n' "5. После ребаланса"       "$AFTER_CARRIERS_N"    "$AFTER_CARRIERS_NODES"

# ==============================================================================
# Уборка: явный вызов ДО самопроверки (trap остаётся страховкой на аварийный выход)
# ==============================================================================

echo
echo "================================================================"
echo " Уборка: возвращаем кластер к ДВУМ воркерам"
echo "================================================================"

cleanup_worker3
CLEANUP_DONE=1

nodes_final="$(active_worker_names)"
log "pg_dist_node после уборки: $nodes_final."

if docker inspect citus-w3 >/dev/null 2>&1; then
  w3_status_final="$(docker inspect --format '{{.State.Status}}' citus-w3)"
else
  w3_status_final="missing"
fi
log "Контейнер citus-w3 после уборки: $w3_status_final."

FINAL_W1="$(node_placements citus-w1)"
FINAL_W2="$(node_placements citus-w2)"
FINAL_TOTAL="$(total_placements)"
FINAL_CARRIERS_N="$(carriers_placements_count)"
FINAL_CARRIERS_NODES="$(carriers_placements_nodes)"
FINAL_CLEANUP_N="$(psql_at "SELECT count(*) FROM pg_dist_cleanup;")"
log "После уборки: citus-w1=$FINAL_W1, citus-w2=$FINAL_W2, итого=$FINAL_TOTAL. carriers: $FINAL_CARRIERS_N размещений на [$FINAL_CARRIERS_NODES]. pg_dist_cleanup=$FINAL_CLEANUP_N."

# ==============================================================================
# Самопроверка: провал демонстрации = ненулевой код
# ==============================================================================

echo
echo "================================================================"
echo " Самопроверка"
echo "================================================================"

ok=1

# 1. Падающий вариант: добавление узла само по себе НЕ должно двигать шарды.
if [ "$AFTERADD_W3" != "0" ]; then
  echo "[FAIL] После добавления citus-w3 (ДО ребаланса) на нём уже $AFTERADD_W3 размещений — это и есть падающий вариант: добавление узла само по себе переместило данные, и ребаланс в сценарии избыточен."
  ok=0
fi
if [ "$AFTERADD_W1" != "$BEFORE_W1" ] || [ "$AFTERADD_W2" != "$BEFORE_W2" ]; then
  echo "[FAIL] Распределение на w1/w2 изменилось от одного факта добавления узла: было w1=$BEFORE_W1/w2=$BEFORE_W2, стало w1=$AFTERADD_W1/w2=$AFTERADD_W2 — ожидали, что добавление узла ничего не тронет."
  ok=0
fi
if [ "$AFTERADD_TOTAL" != "$BEFORE_TOTAL" ]; then
  echo "[FAIL] Итоговое число активных размещений изменилось от добавления узла: было $BEFORE_TOTAL, стало $AFTERADD_TOTAL — размещения не должны теряться/дублироваться на этом шаге."
  ok=0
fi

# 2. Референсная таблица carriers: ДО ребаланса новый узел копию не получает.
if [ "$AFTERADD_CARRIERS_N" != "$BEFORE_CARRIERS_N" ] || [ "$AFTERADD_CARRIERS_NODES" != "$BEFORE_CARRIERS_NODES" ]; then
  echo "[FAIL] Размещения carriers изменились от одного факта добавления узла (было $BEFORE_CARRIERS_N на [$BEFORE_CARRIERS_NODES], стало $AFTERADD_CARRIERS_N на [$AFTERADD_CARRIERS_NODES]) — по факту citus_add_node не должен копировать референсные таблицы на новый узел сам."
  ok=0
fi

# 3. Ребаланс обязан реально завершиться (state=finished), а не зависнуть/упасть.
if [ "$FINAL_STATE" != "finished" ]; then
  echo "[FAIL] Job ребаланса не дошёл до состояния 'finished' (итоговое состояние: $FINAL_STATE) — демонстрацию нельзя считать состоявшейся."
  ok=0
fi

# 4. После ребаланса на citus-w3 обязаны появиться размещения.
if [ "$AFTER_W3" -le 0 ] 2>/dev/null; then
  echo "[FAIL] После ребаланса на citus-w3 размещений=$AFTER_W3, ожидали >0 — перераспределения на три узла не произошло."
  ok=0
fi

# 5. Ребаланс не должен терять/дублировать размещения.
#
# ⚠️ СЧИТАТЬ РАЗДЕЛЬНО, и первая редакция этой проверки этого не делала — она
# сравнивала ОБЩЕЕ число размещений до и после, видела 98 против 99 и объявляла
# «данные потерялись или задвоились». На самом деле разница в единицу — это
# копия РЕФЕРЕНСНОЙ таблицы, приехавшая на новый узел, то есть ровно та находка,
# которую сам этот артефакт и демонстрирует. Проверка объявляла провалом
# собственный результат.
#
# Правильные инварианты разные для двух видов таблиц:
#   - у распределённых число размещений обязано остаться ТЕМ ЖЕ (шарды
#     переезжают, но не появляются и не исчезают);
#   - у референсных оно обязано СТАТЬ РАВНЫМ числу ВОРКЕРОВ (копия на каждом
#     воркере). Координатор — тоже узел кластера, но размещения референсной
#     таблицы считаются по воркерам, поэтому «на каждый узел» здесь неверно.
if [ "$AFTER_DIST" != "$BEFORE_DIST" ]; then
  echo "[FAIL] Размещений РАСПРЕДЕЛЁННЫХ таблиц после ребаланса стало $AFTER_DIST вместо $BEFORE_DIST — шарды потерялись или задвоились."
  ok=0
fi
if [ "$AFTER_CARRIERS_N" != "$AFTER_NODES_N" ]; then
  echo "[FAIL] Размещений РЕФЕРЕНСНОЙ carriers после ребаланса: $AFTER_CARRIERS_N при $AFTER_NODES_N воркерах — копия обязана лежать на каждом ВОРКЕРЕ (координатор — тоже узел кластера, но копии там нет)."
  ok=0
fi

# 6. Ребаланс обязан фактически разгрузить хотя бы один из исходных узлов —
#    иначе новый узел не участвует в распределении по существу.
if [ "$AFTER_W1" -ge "$BEFORE_W1" ] 2>/dev/null && [ "$AFTER_W2" -ge "$BEFORE_W2" ] 2>/dev/null; then
  echo "[FAIL] После ребаланса ни citus-w1 (было $BEFORE_W1, стало $AFTER_W1), ни citus-w2 (было $BEFORE_W2, стало $AFTER_W2) не разгрузились — размещения на citus-w3 взялись не с исходных узлов."
  ok=0
fi

# 7. Референсная таблица: ПОСЛЕ ребаланса ожидаем копию на всех трёх ВОРКЕРАХ.
if [ "$AFTER_CARRIERS_N" != "3" ]; then
  echo "[FAIL] После ребаланса у carriers размещений=$AFTER_CARRIERS_N, ожидали ровно 3 (по числу ВОРКЕРОВ, включая новый; координатор в этот счёт не входит) — референсная таблица должна получить копию на каждом активном воркере."
  ok=0
fi
if [ "$AFTER_CARRIERS_NODES" != "citus-w1,citus-w2,citus-w3" ]; then
  echo "[FAIL] После ребаланса carriers размещена на узлах [$AFTER_CARRIERS_NODES], ожидали [citus-w1,citus-w2,citus-w3]."
  ok=0
fi

# 8. Доступность во время ребаланса: должна быть измерена не менее нескольких раз.
if [ "$PROBE_ITERS" -lt 3 ]; then
  echo "[FAIL] Читающая проба выполнена только $PROBE_ITERS раз(а) за время ребаланса — этого недостаточно, чтобы утверждать что-либо о чтении за время жизни job'а. Ребаланс завершился слишком быстро для содержательного наблюдения, или цикл опроса сломан."
  ok=0
fi

# 8a. Пишущая проба обязана быть выполнена столько же раз, сколько читающая:
#     иначе утверждение про ЗАПИСИ (а режимы переноса противопоставляются именно
#     по ним) опирается на меньшую выборку, чем заявлено.
if [ "$WPROBE_ITERS" -lt 3 ]; then
  echo "[FAIL] Пишущая проба выполнена только $WPROBE_ITERS раз(а) — недостаточно, чтобы утверждать что-либо о записи за время жизни job'а."
  ok=0
fi
if [ "$WPROBE_ITERS" != "$PROBE_ITERS" ]; then
  echo "[FAIL] Число пишущих проб ($WPROBE_ITERS) не совпало с числом читающих ($PROBE_ITERS) — цикл опроса выполняет пробы неодинаково, сравнивать их результаты некорректно."
  ok=0
fi

# 8b. САМ РЕЗУЛЬТАТ ПРОБ. Пункты 8/8a проверяют только КОЛИЧЕСТВО запусков —
#     без этих двух гейтов скрипт печатал бы детали неуспешных проб и всё
#     равно завершался кодом 0 с итоговым [OK]. То есть демонстрация могла
#     отчитаться об успехе при отказавших SELECT/INSERT или при потере
#     видимости записанного — ровно тот ложноположительный исход, ради
#     исключения которого весь этот блок самопроверки и существует.
#     Утверждение об отсутствии отказов за время жизни job'а имеет право
#     звучать ТОЛЬКО при нуле отказов у ОБЕИХ проб. Заметь формулировку:
#     «за время жизни job'а», а не «во время переноса» — с фазой переноса
#     конкретного шарда пробы не соотнесены.
if [ "$PROBE_FAIL" != "0" ] || [ "$PROBE_OK" != "$PROBE_ITERS" ]; then
  echo "[FAIL] Читающая проба: успешных=$PROBE_OK из $PROBE_ITERS, неуспешных=$PROBE_FAIL. Значит этот прогон НЕ подтверждает отсутствия отказов чтения за время жизни job'а. Причина отказа здесь НЕ установлена — скрипт её не выясняет. Детали напечатаны выше и не сглажены; разбирайтесь в причине, а не публикуйте вывод."
  ok=0
fi
if [ "$WPROBE_FAIL" != "0" ] || [ "$WPROBE_OK" != "$WPROBE_ITERS" ]; then
  echo "[FAIL] Пишущая проба: успешных=$WPROBE_OK из $WPROBE_ITERS, неуспешных=$WPROBE_FAIL. Значит этот прогон НЕ подтверждает отсутствия отказов записи (или потери видимости) за время жизни job'а. Причина здесь НЕ установлена: связать отказ с блокировкой режима переноса скрипт не может — для этого нужны pg_locks и привязка к фазе конкретного шарда, ничего этого нет. Это законный результат эксперимента, его надо зафиксировать и разобрать, а не считать прогон успешным."
  ok=0
fi

# 8c. Строки пишущей пробы обязаны быть убраны, а наполнение — вернуться к
#     исходному. Иначе соседние артефакты (они считают по 4000 заказов) начнут
#     давать другие числа.
if [ "$PROBE_ROWS_LEFTOVER_AFTER" != "0" ]; then
  echo "[FAIL] После уборки в orders осталось $PROBE_ROWS_LEFTOVER_AFTER строк(и) пишущей пробы (order_id >= $WPROBE_ID_BASE) — наполнение стенда испорчено."
  ok=0
fi
if [ "$ORDERS_TOTAL_AFTER" != "$ORDERS_TOTAL_BEFORE" ]; then
  echo "[FAIL] Наполнение orders не вернулось к исходному: было $ORDERS_TOTAL_BEFORE, стало $ORDERS_TOTAL_AFTER."
  ok=0
fi

# 8b. ПОСТ-ГЕЙТ: шард пробы обязан ФАКТИЧЕСКИ оказаться на том узле, который
#     предварительный план указал как target. Проверка членства в плане (шаг 2)
#     сама по себе этого не гарантирует: план считается ДО citus_rebalance_start()
#     и теоретически может разойтись с исполненным.
if [ "${POSTGATE_OK:-0}" != "1" ]; then
  echo "[FAIL] ПОСТ-ГЕЙТ НЕ ПРОЙДЕН: план перемещений обещал перенести шард пробы orders=$PROBE_SHARD (customer_id=$PROBE_KEY) на узел '$PROBE_PLAN_TARGET', но фактическое активное размещение (pg_dist_placement, shardstate=1) после завершения job'а = '${PROBE_ACTUAL_NODES:-(не снято)}'. План разошёлся с фактом: значит, шард пробы во время ребаланса переезжал не туда (или не переезжал вовсе), и успешные пробы больше НИЧЕГО не доказывают про чтение данных в процессе их переноса — это ровно та подмена доказательства, от которой защищает гейт шага 2."
  ok=0
fi

# 9. Уборка: кластер обязан вернуться ровно к двум воркерам.
if [ "$nodes_final" != "citus-w1,citus-w2" ]; then
  echo "[FAIL] После уборки pg_dist_node = '$nodes_final', ожидали ровно 'citus-w1,citus-w2' — стенд НЕ вернулся к исходному состоянию, следующий прогон (и соседние артефакты, напр. reference-demo.sh) стартуют из другого состояния."
  ok=0
fi
if [ "$w3_status_final" != "missing" ]; then
  echo "[FAIL] Контейнер citus-w3 после уборки всё ещё существует (статус: $w3_status_final), ожидали, что он будет удалён."
  ok=0
fi
if [ "$FINAL_TOTAL" != "$BEFORE_TOTAL" ]; then
  echo "[FAIL] После уборки итоговое число активных размещений = $FINAL_TOTAL, ожидали вернуться к исходным $BEFORE_TOTAL — данные пропали или задвоились при осушении citus-w3."
  ok=0
fi
if [ "$FINAL_CARRIERS_N" != "2" ] || [ "$FINAL_CARRIERS_NODES" != "citus-w1,citus-w2" ]; then
  echo "[FAIL] После уборки carriers размещена как [$FINAL_CARRIERS_NODES] ($FINAL_CARRIERS_N размещений), ожидали ровно [citus-w1,citus-w2] (2 размещения) — референсная таблица не вернулась к исходному размещению."
  ok=0
fi
if [ "$FINAL_CLEANUP_N" != "0" ]; then
  echo "[FAIL] После уборки pg_dist_cleanup содержит $FINAL_CLEANUP_N запись(ей), ожидали 0 — отложенная запись на удаление старого шарда с citus-w3 не разобрана, и после удаления контейнера citus-w3 она уже не разберётся штатно (узел, на который она ссылается, больше не существует)."
  ok=0
fi

if [ "$ok" != "1" ]; then
  fail "демонстрация провалена (см. [FAIL] выше). Не публиковать эти данные как есть — разбираться в причине (wal_level, версия Citus, состояние стенда)."
fi

echo "[OK] Падающий вариант подтверждён: добавление узла само по себе НЕ переместило ни одного размещения (citus-w3=0 сразу после citus_add_node)."
echo "[OK] Ребаланс перераспределил размещения на три узла: было w1=$BEFORE_W1/w2=$BEFORE_W2 (всего $BEFORE_TOTAL), стало w1=$AFTER_W1/w2=$AFTER_W2/w3=$AFTER_W3 (всего $AFTER_TOTAL, без потерь)."
echo "[OK] Референсная таблица carriers: копию на новый узел кладёт ИМЕННО ребаланс, не citus_add_node — до ребаланса 2 размещения на [citus-w1,citus-w2], после 3 на [citus-w1,citus-w2,citus-w3]."
echo "[OK] ЧТЕНИЕ во время ребаланса: $PROBE_ITERS попыток, успешных=$PROBE_OK, неуспешных=$PROBE_FAIL, максимум ${PROBE_MAX_MS} мс."
echo "[OK] ЗАПИСЬ во время ребаланса: $WPROBE_ITERS попыток, успешных=$WPROBE_OK, неуспешных=$WPROBE_FAIL, максимум ${WPROBE_MAX_MS} мс (${WPROBE_MAX_AT:-—})."
echo "     Успех пишущей пробы = INSERT прошёл И записанное сразу видно последующим чтением, а не просто «не отдал ошибку»."
echo "     Строки пробы удалены: orders вернулась к $ORDERS_TOTAL_AFTER (исходное $ORDERS_TOTAL_BEFORE)."
echo "     ⚠️ Именно ЗАПИСЬ отличает режимы переноса: логическая репликация ('auto')"
echo "     позволяет избежать блокировки записей, 'block_writes' копирует шард через"
echo "     COPY с их блокировкой. Чтение доступно в ОБОИХ режимах, поэтому одной"
echo "     читающей пробы для утверждения о преимуществе 'auto' не хватает."
echo "     Обе пробы били в ПЕРЕЕЗЖАЮЩИЙ шард: customer_id=$PROBE_KEY -> orders shardid=$PROBE_SHARD, $PROBE_MOVE_ROUTE (план перемещений orders: [$MOVING_ORDERS_SHARDS])."
echo "[OK] ПОСТ-ГЕЙТ: план и факт сошлись — после завершения job'а активное размещение (shardstate=1) шарда пробы orders=$PROBE_SHARD находится на '$PROBE_ACTUAL_NODES', ровно на том узле, который предварительный план указал как target ('$PROBE_PLAN_TARGET'). Проверка членства в плане подкреплена фактом."
if [ "$WPROBE_FAIL" = "0" ]; then
  echo "     Ни одна ПИШУЩАЯ проба не отказала, и каждая записанная строка была видна следующим же чтением."
else
  echo "     ВНИМАНИЕ: часть ПИШУЩИХ проб отказала или записанное не подтвердилось чтением (детали выше) — записано как есть."
fi
# Раньше здесь стояла эвристика: «если максимум записи больше максимума
# чтения втрое — похоже на задержку записей при переключении шарда». Она
# УБРАНА как несостоятельная. Максимумы двух проб независимы и меряются в
# разные моменты; в длительность входят docker exec и подключение psql, а
# не только сам INSERT; с фазой конкретного шарда ничто не соотнесено.
# Наблюдения это подтвердили: по принятому набору прогонов
# (22/23/24/26/27/29/30) максимум записи оказывался и НИЖЕ максимума чтения
# (183 против 241 в прогоне 22), и ВЫШЕ (1301 против 894 в прогоне 24),
# и практически вровень (316 против 314 в прогоне 26), и вшестеро выше
# (959 против 145 в прогоне 27), и снова вровень (190 против 163 в
# прогоне 29), и умеренно выше (538 против 507 в каноническом прогоне 30)
# — при нуле отказов во всех случаях.
# (Прежняя редакция этого комментария ссылалась на пары 468/1975 и
# 1265/988 — числа из прогонов, ИСКЛЮЧЁННЫХ из набора как посттерминальные;
# см. FIXTURES.md, «ВСЕ ПРОГОНЫ ДО job_id=22 ИЗ НАБОРА ИСКЛЮЧЕНЫ».)
# То есть соотношение максимумов
# не диагностирует блокировку. Чем эти всплески вызваны на самом деле —
# здесь НЕ установлено; списывать их на «шум хоста» тоже было бы догадкой.
echo "     Максимумы: чтение ${PROBE_MAX_MS} мс, запись ${WPROBE_MAX_MS} мс (${WPROBE_MAX_AT:-—}). Это сырые числа; природа отдельных задержек здесь НЕ установлена — в длительность входят docker exec и подключение, с фазой переноса конкретного шарда они не соотнесены."
if [ "$PROBE_FAIL" = "0" ]; then
  echo "     Ни один ЧИТАЮЩИЙ пробный запрос не отказал. Точная формулировка: во всех засчитанных итерациях job.state был НЕтерминальным, а цикл завершился при первом наблюдении 'finished'. Набор пройденных состояний скрипт не сохраняет и промежуточные фазы мог пропустить между опросами — утверждать, что job прошёл через все, нельзя."
  echo
  echo "     ⚠️ ГРАНИЦА ЭТОГО УТВЕРЖДЕНИЯ. Наблюдений здесь ${PROBE_ITERS}, по одному примерно"
  echo "     раз в секунду (итерация — это sleep 1 ПЛЮС три обращения через docker exec,"
  echo "     поэтому число проб к числу секунд не приравнивается). Длительность окна опроса"
  echo "     ИЗМЕРЕНА часами в этом прогоне: ${POLL_WALL_MS} мс от первого опроса состояния до"
  echo "     первого наблюдения 'finished' (правая граница известна с точностью до интервала"
  echo "     опроса). Это НЕ время фактического переноса шардов — см. оговорку выше."
  echo "     Эти пробы покрывают ВЕСЬ ребаланс на игрушечном объёме: 4000 заказов, ~20 перемещений."
  echo "     Читать это следует как «в коротком прогоне на маленьких данных ни одна"
  echo "     проба не отказала», а НЕ как гарантию доступности вообще. На реальных"
  echo "     объёмах ребаланс идёт часами, перемещений тысячи, и окно, в котором"
  echo "     что-то может пойти не так, несоизмеримо шире."
  echo
  echo "     Отдельно про соблазн набрать побольше наблюдений: в одном из ранних"
  echo "     прогонов этого стенда набралось 1200 успешных проб — но лишь потому,"
  echo "     что ребаланс тогда ЗАВИС и почти всё это время шарды не двигались."
  echo "     Большая выборка измеряла в основном простой. Здесь ${PROBE_ITERS} проб"
  echo "     покрывают время жизни job'а от постановки до первого наблюдения его"
  echo "     завершения. Это НЕ равно времени фактического переноса шардов: за"
  echo "     прогрессом отдельных перемещений скрипт не следит, и внутрь окна попадают"
  echo "     ожидание планировщика, копирование справочника (replicate_reference_tables)"
  echo "     и возможные паузы между moves; правая граница известна с точностью до"
  echo "     интервала опроса. Что доказано — многоминутного зависания в этом окне не"
  echo "     было: этого хватает, чтобы отличить прогон от того самого, с 1200 пробами"
  echo "     по зависшему ребалансу, и большего утверждать нельзя."
else
  echo "     ВНИМАНИЕ: часть ЧИТАЮЩИХ пробных запросов отказала (см. детали выше) — это записано как есть, а не сглажено."
fi
echo "[OK] Уборка подтверждена: pg_dist_node вернулся к [citus-w1,citus-w2], контейнер citus-w3 удалён, число размещений (в т.ч. carriers) совпадает с исходным снимком до добавления узла."

log "Готово."
