#!/usr/bin/env bash
# Артефакт 9: N реплик планировщика, тик выполняется РОВНО ОДИН раз — координация
# через pg_try_advisory_xact_lock (транзакционный вариант локов, см. комментарий в
# scheduler/main.go про пул соединений).
#
# Демонстрация ниже — не только счастливый путь. Сначала прогон с локом (рабочий
# вариант), затем ТЕ ЖЕ три реплики, тот же период, та же длительность и ТА ЖЕ
# схема таблицы, но с -lock=false. Единственное различие между прогонами — флаг.
#
# Почему в tick_log нет UNIQUE(slot). Ограничение само по себе гарантирует «одна
# строка на слот» средствами БД, независимо от лока: с ним прогон БЕЗ лока даёт
# ровно тот же результат (всего_тиков = уникальных_слотов), что и прогон С локом.
# Проверено живьём — при UNIQUE(slot) и -lock=false выходит 2|2, то есть
# заявленный инвариант держится вообще без координации, и демонстрация доказывала
# бы работу ограничения, а не лока. Урок при этом полезный и его стоит помнить:
# если «ровно один раз» нужно именно как строка в таблице, UNIQUE — более простое
# и более дешёвое решение, чем распределённый лок. Но проверяется здесь другое,
# поэтому страховка убрана и механизм показан без подпорок.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export GOPROXY=https://go.khorost.tech,direct

PSQL=(docker exec bj-postgres psql -U jobs -d jobs -t -A -F'|')

# --- Проверка окружения. Без неё недоступный Postgres или несобранный
# планировщик выглядели бы как «0 тиков записано» — молчаливый ложный успех
# демонстрации, а не её явный отказ. ---
if ! docker exec bj-postgres pg_isready -U jobs -d jobs >/dev/null 2>&1; then
    echo "ОШИБКА: PostgreSQL (контейнер bj-postgres) недоступен." >&2
    echo "Подними стенд: docker compose -f compose/compose.yml up -d" >&2
    exit 1
fi

SCHED_BIN="/tmp/bj-scheduler-demo.$$"
# tick_log — таблица этой демонстрации, живущая в общей БД стенда. Безопасное
# состояние после прогона теперь — ОТСУТСТВИЕ таблицы, а не «пустая таблица со
# страхующим UNIQUE»: планировщик сам создаёт её (CREATE TABLE IF NOT EXISTS) в
# правильной схеме при следующем запуске. Так исключено главное, что могло бы
# исказить следующий прогон, — таблица, оставшаяся от прошлого раза со СТАРОЙ
# схемой или с чужими строками (счёт тиков ведётся за текущий прогон, см. подсчёт
# ниже; строки от предыдущего запуска сломали бы и его, и проверку NOT EXISTS).
# trap также гасит фоновые реплики, если скрипт прервётся до штатного wait.
cleanup() {
    kill $(jobs -p) 2>/dev/null || true
    docker exec bj-postgres psql -U jobs -d jobs -q -c "DROP TABLE IF EXISTS tick_log" >/dev/null 2>&1 || true
    rm -f "$SCHED_BIN"
}
trap cleanup EXIT

# Единая схема для ОБЕИХ веток — без UNIQUE(slot), см. заголовок файла.
create_tick_log() {
    docker exec bj-postgres psql -U jobs -d jobs -q -c "DROP TABLE IF EXISTS tick_log"
    docker exec bj-postgres psql -U jobs -d jobs -q -c "
        CREATE TABLE tick_log (
            id     BIGSERIAL PRIMARY KEY,
            slot   BIGINT      NOT NULL,
            runner TEXT        NOT NULL,
            at     TIMESTAMPTZ NOT NULL DEFAULT now()
        )"
}

echo "--- сборка планировщика ---"
if ! ( cd scheduler && go build -o "$SCHED_BIN" . ); then
    echo "ОШИБКА: планировщик не собрался — демонстрация невозможна." >&2
    exit 1
fi
# Собранный бинарник напрямую (не go run): у go run реплика стала бы дочерним
# процессом обёртки go build/run (см. worker-скрипты серии, задачи 4-8). Здесь это
# важно ещё и потому, что все реплики должны стартовать ОДНОВРЕМЕННО: компиляция
# внутри go run разнесла бы их старты и гонка могла бы не состояться вовсе.

echo
echo "=== РАБОЧИЙ ВАРИАНТ: 3 реплики, период 500 мс, длительность 5 с, лок включён ==="
create_tick_log
for i in 1 2 3; do
  "$SCHED_BIN" -id "s$i" -every 500ms -for 5s &
done
wait

TOTAL_OK=$("${PSQL[@]}" -c "SELECT count(*) FROM tick_log")
SLOTS_OK=$("${PSQL[@]}" -c "SELECT count(DISTINCT slot) FROM tick_log")
echo "--- сколько тиков записано и кем ---"
docker exec bj-postgres psql -U jobs -d jobs -c \
  "SELECT count(*) AS всего_тиков, count(DISTINCT slot) AS уникальных_слотов FROM tick_log"
docker exec bj-postgres psql -U jobs -d jobs -c \
  "SELECT runner, count(*) FROM tick_log GROUP BY runner ORDER BY runner"

if [ "$TOTAL_OK" != "$SLOTS_OK" ]; then
    echo "ОШИБКА: рабочий вариант не дал 'всего_тиков = уникальных_слотов' (${TOTAL_OK} vs ${SLOTS_OK})" >&2
    echo "        — демонстрация провалена, лок не даёт эксклюзивности." >&2
    exit 1
fi
echo "всего_тиков=${TOTAL_OK} уникальных_слотов=${SLOTS_OK}"
echo "Вывод: всего_тиков = уникальных_слотов — ни один слот не выполнен дважды,"
echo "хотя реплик три и UNIQUE(slot) в таблице НЕТ: одну строку на слот произвёл"
echo "именно лок. Каждый слот достаётся ровно одной реплике (см. разбивку по"
echo "runner) — НЕ обязательно поровну и не обязательно всем трём за короткий"
echo "прогон: шанс, что кто-то не выиграет ни разу, реален и наблюдался вживую."

echo
echo "=== ПАДАЮЩИЙ ВАРИАНТ: те же 3 реплики, тот же период, длительность и СХЕМА, лок ВЫКЛЮЧЕН ==="
create_tick_log
for i in 1 2 3; do
  "$SCHED_BIN" -id "s$i" -every 500ms -for 5s -lock=false &
done
wait

TOTAL_BAD=$("${PSQL[@]}" -c "SELECT count(*) FROM tick_log")
SLOTS_BAD=$("${PSQL[@]}" -c "SELECT count(DISTINCT slot) FROM tick_log")
RATIO=$(awk -v t="$TOTAL_BAD" -v s="$SLOTS_BAD" 'BEGIN { if (s>0) printf "%.2f", t/s; else print "n/a" }')
echo "--- сколько тиков записано и кем (без лока) ---"
docker exec bj-postgres psql -U jobs -d jobs -c \
  "SELECT count(*) AS всего_тиков, count(DISTINCT slot) AS уникальных_слотов FROM tick_log"
docker exec bj-postgres psql -U jobs -d jobs -c \
  "SELECT runner, count(*) FROM tick_log GROUP BY runner ORDER BY runner"

if [ "$TOTAL_BAD" -le "$SLOTS_BAD" ]; then
    echo "ОШИБКА: падающий вариант не показал превышения всего_тиков над уникальных_слотов" >&2
    echo "        (${TOTAL_BAD} vs ${SLOTS_BAD}) — контраст не подтверждён." >&2
    exit 1
fi
echo "всего_тиков=${TOTAL_BAD} уникальных_слотов=${SLOTS_BAD} отношение=${RATIO}x"
echo "Без лока проверка «слот ещё не записан» и вставка перестают быть одной"
echo "неделимой операцией: реплики читают «слота нет» до того, как любая из них"
echo "закоммитит, и пишут все — один и тот же слот выполнен несколько раз."

echo
echo "=== ИТОГ ==="
echo "с локом:  всего_тиков=${TOTAL_OK} уникальных_слотов=${SLOTS_OK} (равны)"
echo "без лока: всего_тиков=${TOTAL_BAD} уникальных_слотов=${SLOTS_BAD} (в ${RATIO} раза больше)"
echo "Единственное отличие между прогонами — флаг -lock: число реплик (3), период"
echo "(500 мс), длительность (5 с) и схема tick_log (без UNIQUE(slot)) одинаковы."
echo "Разница в числах — измеренное, а не постулированное доказательство:"
echo "эксклюзивность даёт pg_try_advisory_xact_lock, а не ограничение таблицы и не"
echo "случайное отсутствие пересечений реплик по времени."
