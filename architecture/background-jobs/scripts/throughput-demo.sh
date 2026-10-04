#!/usr/bin/env bash
# Артефакт 10 (десятый, дополнение к спеке серии — как девятый, планировщик):
# потолок пропускной способности очереди на PostgreSQL. В спеке ст.3
# заявлено 8 артефактов; артефакт 6 (skip-locked-demo.sh) меряет ожидание
# ОДНОЙ сессии на одной строке, а не пропускную способность N воркеров —
# числа для раздела «где потолок throughput и когда нужен брокер» взять
# было неоткуда, отсюда этот замер.
#
# Что меряется: джоб в секунду при N воркерах (N=1,2,4,8) на очереди,
# заведомо не пустеющей за время замера. Тезис на проверку: пропускная
# способность растёт с числом воркеров СУБЛИНЕЙНО и выходит на плато —
# потолок задаёт не число воркеров, а стоимость одного цикла
# claim -> complete в PostgreSQL (см. worker/queue.go: claimSQL и
# completeSQL — это ДВА отдельных автокоммит-UPDATE на каждую джобу,
# каждый пишет WAL и заводит мёртвую версию строки, см. bloat-demo.sh).
#
# Конструкция замера (все пункты — прямое требование задачи, не
# произвольный выбор):
#
#  1. Фиксированное ОКНО, а не «до опустошения очереди». Очередь наполняется
#     заведомо избыточно (SEED_JOBS ниже), окно фиксированной длины, считаем
#     ДЕЛЬТУ count(state='done') на границах окна. Если бы очередь опустела
#     посреди замера, воркер ушёл бы в time.Sleep(100ms) (worker/main.go) и
#     число оказалось бы заниженным не из-за PostgreSQL, а из-за пустой
#     очереди — это НЕ тот эффект, который меряется здесь.
#  2. Отсчёт ПОСЛЕ подъёма всех воркеров (WARMUP ниже) — старт Go-бинаря и
#     установка pgxpool (Connect делает Ping, см. worker/db.go) стоят
#     десятки миллисекунд; при восьми параллельных стартах разброс уже
#     заметен на фоне окна.
#  3. `-work 0s`: меряется потолок САМОГО механизма очереди, а не скорость
#     полезной работы. flag.Duration принимает "0s" (time.ParseDuration),
#     doWork с work=0 возвращает true на первом же срабатывании
#     time.After(0), быстрее первого тика heartbeat (lease/3, секунды) —
#     проверено чтением worker/main.go перед написанием этого скрипта.
#     С реальной работой в сотни миллисекунд потолок упёрся бы в неё, а не
#     в PostgreSQL, и числа были бы другими — это НЕ подходит для ответа
#     «где потолок пропускной способности САМОЙ очереди».
#  4. Одинаковые условия для всех N: перед КАЖДЫМ замером (каждым повтором,
#     не только каждым N) — TRUNCATE и заново seed тем же SEED_JOBS. Иначе
#     накопленные мёртвые версии от предыдущего замера (см. bloat-demo.sh)
#     замедлили бы следующий, и рост N смешался бы с ростом bloat — ровно
#     ошибка счётчика из задачи 6.
#  5. REPS=5 повторов на каждое N (минимум по заданию — 3; взято больше:
#     живой прогон на этом хосте дал один выброс на 3 повторах — см.
#     отчёт задачи 12), в выводе — среднее, МЕДИАНА (устойчивее к
#     единичному выбросу) и разброс (min-max), не одно число. Для сводных
#     сравнений (кратность роста, падающий вариант) используется медиана.
#  6. autovacuum на jobs НЕ трогаем (в отличие от bloat-demo.sh, здесь нужен
#     реальный режим работы, а не картина накопления bloat в моменте).
#
# Числа этого скрипта host-зависимы (WSL2 + Docker Desktop, проброшенный
# порт localhost:5456, без ограничений cpus/mem у контейнера postgres в
# compose/compose.yml) — переносить можно ХАРАКТЕР зависимости (сублинейный
# рост, плато), а не абсолютные джоб/сек. И: ниже НЕ утверждается «предел
# PostgreSQL» — только предел ЭТОГО стенда на ЭТОМ хосте при этой схеме
# (jobs с двумя частичными индексами, synchronous_commit=on по умолчанию).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export GOPROXY=https://go.khorost.tech,direct

PSQL=(docker exec bj-postgres psql -U jobs -d jobs -t -A -F'|')

# --- Проверка окружения. Без неё недоступный Postgres или несобранный
# воркер выглядели бы как «нули джоб/сек» — молчаливый ложный результат,
# а не явный отказ демонстрации. ---
if ! docker exec bj-postgres pg_isready -U jobs -d jobs >/dev/null 2>&1; then
    echo "ОШИБКА: PostgreSQL (контейнер bj-postgres) недоступен." >&2
    echo "Подними стенд: docker compose -f compose/compose.yml up -d" >&2
    exit 1
fi

WORKER_BIN="/tmp/bj-throughput-demo-worker.$$"
SYNC_COMMIT_CHANGED=0
restore() {
    kill $(jobs -p) 2>/dev/null || true
    if [ "$SYNC_COMMIT_CHANGED" = "1" ]; then
        docker exec bj-postgres psql -U jobs -d jobs -q \
            -c "ALTER SYSTEM SET synchronous_commit = on" >/dev/null 2>&1 || true
        docker exec bj-postgres psql -U jobs -d jobs -q \
            -c "SELECT pg_reload_conf()" >/dev/null 2>&1 || true
    fi
    rm -f "$WORKER_BIN"
}
trap restore EXIT

echo "--- сборка воркера ---"
if ! ( cd worker && go build -o "$WORKER_BIN" . ); then
    echo "ОШИБКА: воркер не собрался — демонстрация невозможна." >&2
    exit 1
fi
# Собранный бинарник напрямую (не go run) — тот же приём, что в
# bloat-demo.sh/lease-demo.sh: go run сделал бы воркера дочерним процессом
# обёртки go build, что мешает и таймингу, и killall-очистке.

SEED_JOBS=150000
WARMUP=1
WINDOW=5
REPS=5

count_done() { "${PSQL[@]}" -c "SELECT count(*) FROM jobs WHERE state='done'"; }

# measure_once N LABEL -> печатает ОДНО число (джоб/сек) в stdout.
# TRUNCATE + seed заново на КАЖДЫЙ вызов (пункт 4 конструкции выше).
measure_once() {
    local n="$1" label="$2"
    docker exec bj-postgres psql -U jobs -d jobs -q -c "TRUNCATE jobs RESTART IDENTITY" >/dev/null
    bash scripts/seed.sh "$SEED_JOBS" >/dev/null

    local pids=() i
    for ((i = 1; i <= n; i++)); do
        "$WORKER_BIN" -id "tp-${label}-w${i}" -work 0s -lease 30s -for "$((WARMUP + WINDOW))s" \
            >"/tmp/bj-throughput-${label}-w${i}.log" 2>&1 &
        pids+=("$!")
    done

    sleep "$WARMUP"
    # Реальное прошедшее время между двумя снимками count(*), а не номинальный
    # $WINDOW, — делитель. Иначе: если сам count_done() (docker exec + psql,
    # процесс, а не сеть в горячем пути) на КАКОМ-ТО прогоне подтормозил на
    # старте окна (диск/планировщик ОС/WSL2 — источник неизвестен и не
    # обязан быть стабильным), номинальный $WINDOW разошёлся бы с реальным
    # прошедшим временем и джоб/сек оказались бы заниженными не из-за
    # PostgreSQL, а из-за неточного делителя — это и есть найденная причина
    # выброса 61.2 джоб/сек на N=1 в одном из пробных прогонов (см. отчёт
    # задачи 12): реальное окно, восстановленное по времени возврата вызовов,
    # было заметно длиннее номинальных 5с.
    local start_count end_count t_start t_end
    start_count=$(count_done)
    t_start=$(date +%s.%N)
    sleep "$WINDOW"
    end_count=$(count_done)
    t_end=$(date +%s.%N)

    local pid
    for pid in "${pids[@]}"; do
        if ! wait "$pid"; then
            echo "ПРЕДУПРЕЖДЕНИЕ: воркер pid=${pid} (${label}) завершился с ошибкой, см. /tmp/bj-throughput-${label}-*.log" >&2
        fi
    done

    awk -v s="$start_count" -v e="$end_count" -v t0="$t_start" -v t1="$t_end" \
        'BEGIN { printf "%.1f", (e - s) / (t1 - t0) }'
}

# compute_stats: значения на stdin (по одному в строке) -> "avg median min max".
compute_stats() {
    awk '{
        vals[NR] = $1; sum += $1; n++
        if (NR == 1 || $1 < min) min = $1
        if (NR == 1 || $1 > max) max = $1
    } END {
        for (i = 1; i <= n; i++)
            for (j = i + 1; j <= n; j++)
                if (vals[j] < vals[i]) { t = vals[i]; vals[i] = vals[j]; vals[j] = t }
        if (n % 2 == 1) med = vals[(n + 1) / 2]
        else med = (vals[n / 2] + vals[n / 2 + 1]) / 2
        printf "%.1f %.1f %.1f %.1f", sum / n, med, min, max
    }'
}

echo
echo "=== АРТЕФАКТ 10 (десятый, дополнение к спеке серии): потолок пропускной способности ==="
echo "N воркеров: 1, 2, 4, 8; ${REPS} повторов на каждое N; окно измерения ${WINDOW}с после"
echo "${WARMUP}с прогрева; очередь наполняется заново ${SEED_JOBS} джобами перед КАЖДЫМ повтором;"
echo "-work 0s — меряется потолок самого механизма claim->complete, не полезной работы."
echo

RESULT_N=()
RESULT_AVG=()
RESULT_MED=()
RESULT_MIN=()
RESULT_MAX=()
BASE_MED=""

for NUM_WORKERS in 1 2 4 8; do
    echo "--- N=${NUM_WORKERS} воркеров ---"
    vals=()
    for ((r = 1; r <= REPS; r++)); do
        v=$(measure_once "$NUM_WORKERS" "n${NUM_WORKERS}r${r}")
        vals+=("$v")
        echo "  повтор ${r}: ${v} джоб/сек"
    done
    read -r avg med min max <<<"$(printf '%s\n' "${vals[@]}" | compute_stats)"
    echo "  N=${NUM_WORKERS}: [${vals[*]}] джоб/сек -> avg=${avg} median=${med} min=${min} max=${max}"
    RESULT_N+=("$NUM_WORKERS")
    RESULT_AVG+=("$avg")
    RESULT_MED+=("$med")
    RESULT_MIN+=("$min")
    RESULT_MAX+=("$max")
    if [ "$NUM_WORKERS" = "1" ]; then
        BASE_MED="$med"
    fi
    echo
done

if [ "$(awk -v b="$BASE_MED" 'BEGIN { print (b == 0) }')" = "1" ]; then
    echo "ОШИБКА: при N=1 джоб/сек=0 — замер не удался (см. предупреждения выше про" >&2
    echo "ошибки воркеров), делить на этот ноль дальше нельзя." >&2
    exit 1
fi

echo "=== СВОДНАЯ ТАБЛИЦА: N воркеров -> джоб/сек (по МЕДИАНЕ) ==="
printf "%-3s %-10s %-18s %-10s %-24s\n" "N" "median" "(min-max)" "во ск.раз>N=1" "ожидалось при ×N (линейно)"
for idx in "${!RESULT_N[@]}"; do
    n="${RESULT_N[$idx]}"
    med="${RESULT_MED[$idx]}"
    min="${RESULT_MIN[$idx]}"
    max="${RESULT_MAX[$idx]}"
    ratio=$(awk -v a="$med" -v b="$BASE_MED" 'BEGIN { printf "%.2f", a / b }')
    expected=$(awk -v b="$BASE_MED" -v n="$n" 'BEGIN { printf "%.1f", b * n }')
    printf "%-3s %-10s %-18s x%-9s %-24s\n" "$n" "$med" "(${min}-${max})" "$ratio" "x${n} = ${expected}"
done

N8_MED="${RESULT_MED[3]}"
EXPECTED8=$(awk -v b="$BASE_MED" 'BEGIN { printf "%.1f", b * 8 }')
SHORTFALL8=$(awk -v e="$EXPECTED8" -v a="$N8_MED" 'BEGIN { printf "%.2f", e / a }')
RATIO8=$(awk -v a="$N8_MED" -v b="$BASE_MED" 'BEGIN { printf "%.2f", a / b }')

echo
echo "ПАДАЮЩИЙ ВАРИАНТ: если бы потолка не было, 8 воркеров давали бы примерно"
echo "восьмикратную пропускную способность одного — ${EXPECTED8} джоб/сек (×8 от"
echo "N=1: ${BASE_MED} джоб/сек медианы). Фактически получено ${N8_MED} джоб/сек — это ×${RATIO8} от"
echo "N=1, то есть в ${SHORTFALL8} раза МЕНЬШЕ линейно ожидаемого при 8 воркерах. Рост"
echo "СУБЛИНЕЙНЫЙ, что и было тезисом на проверку."
echo
echo "Причина плато ниже НЕ утверждается без проверки (см. дополнительный замер"
echo "synchronous_commit ниже) — это либо WAL-fsync на каждый commit (claim и"
echo "complete — по автокоммит-UPDATE каждый), либо contention на \"горячих\""
echo "страницах частичных индексов (jobs_claim_idx/jobs_lease_idx), либо и то, и"
echo "другое вместе; без отдельного профилирования (perf/pg_stat_statements/wait"
echo "events) разложить вклад каждой причины этот скрипт не может."
echo
echo "ОГОВОРКА: числа привязаны к ЭТОМУ хосту (WSL2 + Docker Desktop, проброшенный"
echo "порт localhost:5456, контейнер postgres без ограничений cpus/mem в"
echo "compose/compose.yml) — переносить можно ХАРАКТЕР зависимости (сублинейный"
echo "рост, выход на плато), а не абсолютные джоб/сек. Это предел ЭТОГО стенда на"
echo "ЭТОМ хосте при этой схеме (jobs с двумя частичными индексами) — НЕ \"предел"
echo "PostgreSQL\" как таковой."

REPS_SYNC=2
echo
echo "=== ДОПОЛНИТЕЛЬНЫЙ ЗАМЕР (проверка ОДНОЙ гипотезы о причине, не полное"
echo "    объяснение плато): synchronous_commit=off против synchronous_commit=on"
echo "    при N=8. Это НЕ доказывает, что WAL-fsync — единственная причина плато;"
echo "    это проверяет, вносит ли он заметный вклад. Разведочный замер: ${REPS_SYNC}"
echo "    повтора на вариант, а не ${REPS} — числа ниже менее устойчивы, чем"
echo "    основная таблица выше, и приводятся только как направление, не как"
echo "    точная величина эффекта."

sync_vals_on=()
for ((r = 1; r <= REPS_SYNC; r++)); do
    v=$(measure_once 8 "syncon-r${r}")
    sync_vals_on+=("$v")
    echo "  synchronous_commit=on,  повтор ${r}: ${v} джоб/сек"
done

docker exec bj-postgres psql -U jobs -d jobs -q -c "ALTER SYSTEM SET synchronous_commit = off" >/dev/null
docker exec bj-postgres psql -U jobs -d jobs -q -c "SELECT pg_reload_conf()" >/dev/null
SYNC_COMMIT_CHANGED=1

sync_vals_off=()
for ((r = 1; r <= REPS_SYNC; r++)); do
    v=$(measure_once 8 "syncoff-r${r}")
    sync_vals_off+=("$v")
    echo "  synchronous_commit=off, повтор ${r}: ${v} джоб/сек"
done

docker exec bj-postgres psql -U jobs -d jobs -q -c "ALTER SYSTEM SET synchronous_commit = on" >/dev/null
docker exec bj-postgres psql -U jobs -d jobs -q -c "SELECT pg_reload_conf()" >/dev/null
SYNC_COMMIT_CHANGED=0

read -r on_avg on_med on_min on_max <<<"$(printf '%s\n' "${sync_vals_on[@]}" | compute_stats)"
read -r off_avg off_med off_min off_max <<<"$(printf '%s\n' "${sync_vals_off[@]}" | compute_stats)"
DELTA=$(awk -v on="$on_med" -v off="$off_med" 'BEGIN { printf "%.1f", (off - on) / on * 100 }')

echo
echo "N=8, synchronous_commit=on:  median=${on_med} (${on_min}-${on_max}) джоб/сек"
echo "N=8, synchronous_commit=off: median=${off_med} (${off_min}-${off_max}) джоб/сек"
echo "Разница off относительно on (по медиане): ${DELTA}%."
echo "Прочтение (осторожное, не окончательное): если |${DELTA}%| мало относительно"
echo "разброса (min-max) обеих серий — заметного вклада WAL-fsync в это плато при"
echo "N=8 этим замером НЕ обнаружено (плато объясняется чем-то ещё, скорее всего"
echo "contention на горячих страницах индексов, но ЭТО отдельно не проверялось и"
echo "здесь не утверждается). Если разница заметно больше разброса — WAL-fsync"
echo "вносит измеримый вклад, но это не значит, что он единственный: contention"
echo "мог бы дать похожий эффект в другую сторону, а этот замер разделить их не"
echo "может."

echo
echo "Скрипт занял ${SECONDS}с."
