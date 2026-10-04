#!/usr/bin/env bash
# Артефакт 7: очередь на PostgreSQL — худший случай для MVCC. Одна джоба
# переписывает СТРОКУ несколько раз (INSERT -> claim -> ... -> complete), и
# state (queued/running/done) участвует в предикатах ДВУХ частичных индексов
# (jobs_claim_idx, jobs_lease_idx, см. sql/01-schema.sql). Из-за этого каждое
# изменение state — НЕ HOT-обновление: постгрес заводит новую версию строки И
# правит индексы, а старая версия становится мёртвой. На джобу без heartbeat
# (см. ниже, почему в этом прогоне его нет) это минимум 2 таких перезаписи —
# claim и complete, — то есть 2 мёртвые версии на 1 живую джобу.
#
# Ключевой момент, на котором легко соврать: обычный VACUUM НЕ уменьшает файл
# на диске. Он освобождает место ВНУТРИ файла для повторного использования.
# Поэтому n_dead_tup после VACUUM упадёт, а pg_total_relation_size — нет.
# VACUUM FULL мог бы вернуть место ОС, но переписывает таблицу целиком под
# ACCESS EXCLUSIVE-локом — на живой очереди неприменим, здесь не выполняется.
#
# Почему без heartbeat: -work 1ms короче первого тика heartbeat (-lease/3 =
# 10с), поэтому heartbeat в этом прогоне ни разу не срабатывает — на джобу
# приходится РОВНО 2 перезаписи (claim, complete), не больше. Это НЕ ослабляет
# демонстрацию: даже эти два неизбежных UPDATE уже дают вдвое больше мёртвых
# версий, чем джоб, и именно эту нижнюю границу и меряем. Будь героя-джоба
# длиннее lease, heartbeat добавил бы ещё версий и соотношение выросло бы —
# см. артефакты 1-2 (lease-demo.sh) про сам heartbeat.
#
# n_dead_tup из pg_stat_user_tables — асинхронная оценка (обновляется backend'ом
# при выходе/окончании транзакции, а не мгновенно на каждый UPDATE). Проверено
# отдельно (см. отчёт задачи): в этом скрипте к моменту чтения все воркеры и
# psql-сессии уже завершились (bash `wait` дожидается конца процессов), их
# статистика уже отправлена — свежих чисел ждать не нужно. Дополнительно ниже
# число мёртвых версий ДО VACUUM сверяется с тем, что сам VACUUM фактически
# нашёл и вычистил из индексов ("dead item identifiers") — если бы статистика
# отставала или врала, эти два независимых числа разошлись бы.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export GOPROXY=https://go.khorost.tech,direct

PSQL=(docker exec bj-postgres psql -U jobs -d jobs -t -A -F'|')
JOBS=2000

# --- Проверка окружения. Без неё недоступный Postgres или несобранный
# воркер выглядели бы как «бесполезные нули» — молчаливый ложный результат
# демонстрации, а не её явный отказ. ---
if ! docker exec bj-postgres pg_isready -U jobs -d jobs >/dev/null 2>&1; then
    echo "ОШИБКА: PostgreSQL (контейнер bj-postgres) недоступен." >&2
    echo "Подними стенд: docker compose -f compose/compose.yml up -d" >&2
    exit 1
fi

size() { "${PSQL[@]}" -c "SELECT pg_size_pretty(pg_total_relation_size('jobs'))"; }
size_bytes() { "${PSQL[@]}" -c "SELECT pg_total_relation_size('jobs')"; }
stat() { "${PSQL[@]}" -c "SELECT n_live_tup, n_dead_tup, last_autovacuum IS NOT NULL AS autovac FROM pg_stat_user_tables WHERE relname='jobs'"; }
dead_tup() { "${PSQL[@]}" -c "SELECT n_dead_tup FROM pg_stat_user_tables WHERE relname='jobs'"; }

WORKER_BIN="/tmp/bj-bloat-demo-worker.$$"
# trap делает две вещи: гасит воркеров, если скрипт прервётся ДО штатного
# `wait` (иначе они останутся жить фоном), и, ГЛАВНОЕ, возвращает autovacuum —
# не строкой в конце, а именно через trap. При падении посреди прогона
# (set -e) настройка осталась бы выключенной и молча испортила бы все
# последующие демонстрации на этом стенде: таблица jobs общая для всей серии.
restore() {
    kill $(jobs -p) 2>/dev/null || true
    docker exec bj-postgres psql -U jobs -d jobs -q \
        -c "ALTER TABLE jobs RESET (autovacuum_enabled)" >/dev/null 2>&1 || true
    rm -f "$WORKER_BIN"
}
trap restore EXIT

echo "--- сборка воркера ---"
if ! ( cd worker && go build -o "$WORKER_BIN" . ); then
    echo "ОШИБКА: воркер не собрался — демонстрация невозможна." >&2
    exit 1
fi
# Собранный бинарник напрямую (не go run): у go run воркер стал бы дочерним
# процессом обёртки go build/run (см. остальные скрипты серии, задачи 3-7).

echo
echo "=== АРТЕФАКТ 7: bloat на очереди ==="
docker exec bj-postgres psql -U jobs -d jobs -q -c "TRUNCATE jobs RESTART IDENTITY"

docker exec bj-postgres psql -U jobs -d jobs -q -c "ALTER TABLE jobs SET (autovacuum_enabled = off)"
echo "autovacuum на таблице ВЫКЛЮЧЕН на время демонстрации — иначе он мог бы"
echo "подчистить мёртвые версии ДО того, как мы их измерим, и эффект был бы не виден в моменте"
echo "размер пустой:               $(size)"

bash scripts/seed.sh "$JOBS" >/dev/null
echo "после вставки ${JOBS} джоб:     размер=$(size) статистика: $(stat)"

"$WORKER_BIN" -id b1 -work 1ms -lease 30s -for 40s >/tmp/bj-bloat-b1.log 2>&1 &
"$WORKER_BIN" -id b2 -work 1ms -lease 30s -for 40s >/tmp/bj-bloat-b2.log 2>&1 &
wait

DEAD_BEFORE=$(dead_tup)
SIZE_BEFORE=$(size_bytes)
RATIO=$(awk -v d="$DEAD_BEFORE" -v j="$JOBS" 'BEGIN { printf "%.1f", d / j }')
echo "после обработки:             размер=$(size) статистика: $(stat)"
echo "мёртвых версий на джобу: ${DEAD_BEFORE} / ${JOBS} = ${RATIO} — джоб выполнено ${JOBS},"
echo "а мёртвых версий строк накопилось в ${RATIO} раза больше (claim и complete — по одной"
echo "версии каждый; heartbeat в этом прогоне не успевает сработать, см. комментарий вверху файла)"

echo
echo "--- принудительный VACUUM (autovacuum всё ещё выключен) ---"
VACUUM_OUT=$(docker exec bj-postgres psql -U jobs -d jobs -c "VACUUM (VERBOSE, ANALYZE) jobs" 2>&1)
echo "$VACUUM_OUT" | grep -iE "removed|dead" | head -3
DEAD_REMOVED=$(echo "$VACUUM_OUT" | grep -oE '[0-9]+ dead item identifiers' | head -1 | grep -oE '[0-9]+')
SIZE_AFTER=$(size_bytes)
echo "после VACUUM:                размер=$(size) статистика: $(stat)"

echo
echo "--- сверка: не отстала ли статистика n_dead_tup ---"
echo "n_dead_tup ДО VACUUM был ${DEAD_BEFORE}; сам VACUUM независимо насчитал и вычистил из"
echo "индексов ${DEAD_REMOVED} «dead item identifiers» — числа независимые (одно из статистики,"
echo "другое из фактического протокола VACUUM) и совпадают, значит n_dead_tup не был устаревшим"
echo "размер в байтах: до VACUUM=${SIZE_BEFORE}, после VACUUM=${SIZE_AFTER}"
if [ "$SIZE_AFTER" -lt "$SIZE_BEFORE" ]; then
    echo "(после VACUUM файл СТАЛ МЕНЬШЕ — это не типичное поведение обычного VACUUM,"
    echo "стоит перепроверить прогон отдельно)"
else
    echo "(файл на диске не уменьшился — VACUUM освободил место ВНУТРИ файла для"
    echo "повторного использования, а не вернул его операционной системе)"
fi

echo
echo "Вывод: state участвует в предикатах двух частичных индексов, поэтому claim и"
echo "complete — не HOT-обновления: каждое заводит новую версию строки и мёртвую"
echo "старую. На ${JOBS} джоб это дало ${DEAD_BEFORE} мёртвых версий (${RATIO}x к числу джоб) —"
echo "и это НИЖНЯЯ граница: у джоб с heartbeat (долгая работа при коротком lease,"
echo "см. lease-demo.sh) версий на джобу ещё больше. VACUUM убрал мёртвые версии из"
echo "статистики и из индексов, но размер файла на диске не сократился — сравните"
echo "${SIZE_BEFORE} и ${SIZE_AFTER} байт выше."
echo
echo "ПАДАЮЩИЙ ВАРИАНТ: если бы очередь не порождала мёртвых версий (например,"
echo "если бы claim/complete были HOT-обновлениями), n_dead_tup после обработки"
echo "остался бы близок к нулю, а не к ${DEAD_BEFORE}, и сверка с VACUUM выше показала бы"
echo "0 «dead item identifiers», а не ${DEAD_REMOVED}."
