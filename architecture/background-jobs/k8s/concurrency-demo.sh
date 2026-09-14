#!/usr/bin/env bash
# Артефакт 4: три CronJob (cc-allow/cc-forbid/cc-replace), отличающиеся ТОЛЬКО
# полем concurrencyPolicy (см. k8s/concurrency.yaml — построчная сверка того,
# что манифесты различаются исключительно именем и concurrencyPolicy, сделана
# отдельно при написании манифеста). Расписание у всех "* * * * *", работа —
# sleep 100с: заведомо дольше интервала (60с), поэтому перекрытие следующего
# тика с ещё идущим предыдущим запуском гарантировано конструкцией, а не
# случайным стечением обстоятельств.
#
# Ожидаемая картина (её же скрипт проверяет в самом конце и падает, если она
# не подтвердилась):
#   Allow   — несколько джоб активны ОДНОВРЕМЕННО (.status.active растёт);
#   Forbid  — активна НЕ БОЛЕЕ ОДНОЙ джобы в любой момент, очередной тик,
#             заставший предыдущую джобу ещё активной, просто пропускается;
#   Replace — активна НЕ БОЛЕЕ ОДНОЙ джобы, НО идущая джоба на каждом тике
#             убивается контроллером и заменяется новой — это отдельно
#             проверяется по факту (исчезновение объекта Job ДО того, как он
#             успел бы доработать положенные 100с и получить Complete=True),
#             а не заявляется на веру.
#
# ПАДАЮЩИЙ ВАРИАНТ (что увидели бы, если бы concurrencyPolicy не влияла):
# при идентичных расписании/работе/моменте создания все три ветки дали бы
# ОДИНАКОВУЮ картину — либо везде максимум 1 активная джоба, либо везде
# несколько сразу. Различие между ветками при этих равных условиях и есть
# доказательство того, что доказывает артефакт.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

NS=bj-cron
KUBECONFIG_PATH="${KVOL_KUBECONFIG:-G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig}"
KCTL=(kubectl --kubeconfig "$KUBECONFIG_PATH")

DURATION="${DURATION:-300}"   # секунд наблюдения (~5 минут по умолчанию)
INTERVAL="${INTERVAL:-15}"    # секунд между снимками

# =========================================================================
# --- Preflight. Без него недоступный кластер, чужой кластер (прод
# k8s-tula!) или неприменившийся манифест выглядели бы как «тихий провал»,
# а не как явный отказ. Это ГЛАВНАЯ защита от удара в прод: голый kubectl
# в этом окружении бьёт в admin@k8s-tula (единственный контекст в
# ~/.kube/config), поэтому ниже используется ИСКЛЮЧИТЕЛЬНО
# --kubeconfig "$KUBECONFIG_PATH", а кластер дополнительно проверяется по
# именам нод — они обязаны начинаться с "k8s-volga-". ---
# =========================================================================
echo "=== preflight ==="

if [ ! -f "$KUBECONFIG_PATH" ]; then
    echo "ОШИБКА: kubeconfig k8s-volga не найден по пути: $KUBECONFIG_PATH" >&2
    echo "Проверьте путь (можно переопределить переменной KVOL_KUBECONFIG)." >&2
    exit 1
fi
echo "kubeconfig: $KUBECONFIG_PATH — файл существует"

# Проверка связи и проверка кластера объединены в один вызов: get nodes
# нам всё равно нужен для сверки имён, а отдельный get --raw=/version на
# этом API-сервере ведёт себя нестандартно (см. отчёт задачи) и только
# добавляет лишнюю точку отказа.
NODE_NAMES=$("${KCTL[@]}" get nodes -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)
if [ -z "$NODE_NAMES" ]; then
    echo "ОШИБКА: кластер по этому kubeconfig недоступен, либо не отдал ни одной ноды" >&2
    echo "(kubectl --kubeconfig \"$KUBECONFIG_PATH\" get nodes не вернул результата)." >&2
    exit 1
fi
echo "kubectl отвечает, ноды получены"
BAD_NODE=""
for n in $NODE_NAMES; do
    case "$n" in
        k8s-volga-*) ;;
        *) BAD_NODE="$n" ;;
    esac
done
if [ -n "$BAD_NODE" ]; then
    echo "ОШИБКА: это НЕ тестовый кластер k8s-volga — среди нод есть '$BAD_NODE', её имя не" >&2
    echo "начинается с 'k8s-volga-'. Это и есть защита от случайного удара в прод (k8s-tula)." >&2
    echo "Прерываю выполнение, ничего не создано." >&2
    exit 1
fi
echo "кластер подтверждён как k8s-volga по именам нод: $NODE_NAMES"

CURRENT_CTX=$("${KCTL[@]}" config current-context 2>/dev/null || echo "?")
echo "текущий контекст этого kubeconfig: $CURRENT_CTX"

if "${KCTL[@]}" get namespace "$NS" >/dev/null 2>&1; then
    echo "ОШИБКА: namespace '$NS' уже существует — похоже, предыдущий прогон не убрался за собой." >&2
    echo "Удалите вручную перед повторным запуском:" >&2
    echo "  kubectl --kubeconfig \"$KUBECONFIG_PATH\" delete namespace $NS" >&2
    exit 1
fi
echo "namespace '$NS' свободен"
echo

# --- Уборка через trap: намеренно удаляем ТОЛЬКО namespace bj-cron, ---
# ничего больше в кластере не трогаем. Срабатывает и при обычном ---
# завершении, и при прерывании (Ctrl+C, ошибка под set -e). ---
cleanup() {
    local rc=$?
    echo
    echo "=== уборка: удаляю namespace $NS ==="
    "${KCTL[@]}" delete namespace "$NS" --wait=true --ignore-not-found >/dev/null 2>&1 || true
    echo "namespace $NS удалён (или не создавался)"
    exit "$rc"
}
trap cleanup EXIT

# =========================================================================
echo "=== применяю манифест ==="
"${KCTL[@]}" create namespace "$NS"
"${KCTL[@]}" apply -f k8s/concurrency.yaml

CJ_COUNT=$("${KCTL[@]}" get cronjob -n "$NS" --no-headers 2>/dev/null | wc -l | tr -d ' ')
if [ "$CJ_COUNT" != "3" ]; then
    echo "ОШИБКА: после apply в namespace $NS обнаружено CronJob'ов: $CJ_COUNT (ожидалось 3)." >&2
    echo "Манифест не применился полностью — демонстрация невозможна." >&2
    exit 1
fi
echo "созданы 3 CronJob: cc-allow, cc-forbid, cc-replace"
"${KCTL[@]}" get cronjob -n "$NS"
echo

# --- вспомогательные функции ---
active_names() {
    "${KCTL[@]}" get cronjob "$1" -n "$NS" -o jsonpath='{range .status.active[*]}{.name}{"\n"}{end}' 2>/dev/null
}
active_count() {
    local n
    n=$(active_names "$1")
    if [ -z "$n" ]; then echo 0; else printf '%s\n' "$n" | grep -c .; fi
}
total_created_snapshot() {
    "${KCTL[@]}" get jobs -n "$NS" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null \
        | grep -c "^$1-" || true
}

declare -A REPLACE_FIRST_SEEN
declare -A REPLACE_LAST_SEEN
PREV_REPLACE_NAME=""
INTERRUPTED_JOBS=""
STILL_PRESENT_ANOMALY=""
# Отдельный счётчик, а не ${#REPLACE_FIRST_SEEN[@]}: под `set -u` разворот
# @ пустого ассоциативного массива в этой сборке bash (5.2.37, msys)
# падает как "unbound variable", даже если массив явно объявлен.
REPLACE_SEEN_COUNT=0

MAX_ACTIVE_ALLOW=0
MAX_ACTIVE_FORBID=0
MAX_ACTIVE_REPLACE=0

ITERATIONS=$(( DURATION / INTERVAL ))
START_TS=$(date -u +%s)

echo "=== наблюдение: ${ITERATIONS} снимков каждые ${INTERVAL}с, всего ~$((DURATION / 60)) минут, время UTC ==="
echo "(расписание кластера идёт по UTC — .spec.timeZone нигде не задан)"
echo

for i in $(seq 1 "$ITERATIONS"); do
    sleep "$INTERVAL"
    NOW_ISO=$(date -u +%H:%M:%S)
    ELAPSED=$(( $(date -u +%s) - START_TS ))

    A_ACTIVE=$(active_count cc-allow)
    F_ACTIVE=$(active_count cc-forbid)
    R_NAMES=$(active_names cc-replace)
    if [ -z "$R_NAMES" ]; then R_ACTIVE=0; else R_ACTIVE=$(printf '%s\n' "$R_NAMES" | grep -c .); fi
    R_NAME=$(printf '%s\n' "$R_NAMES" | head -1)

    A_TOTAL=$(total_created_snapshot cc-allow)
    F_TOTAL=$(total_created_snapshot cc-forbid)

    [ "$A_ACTIVE" -gt "$MAX_ACTIVE_ALLOW" ] && MAX_ACTIVE_ALLOW=$A_ACTIVE
    [ "$F_ACTIVE" -gt "$MAX_ACTIVE_FORBID" ] && MAX_ACTIVE_FORBID=$F_ACTIVE
    [ "$R_ACTIVE" -gt "$MAX_ACTIVE_REPLACE" ] && MAX_ACTIVE_REPLACE=$R_ACTIVE

    if [ -n "$R_NAME" ]; then
        REPLACE_LAST_SEEN[$R_NAME]=$ELAPSED
        if [ -z "${REPLACE_FIRST_SEEN[$R_NAME]:-}" ]; then
            REPLACE_FIRST_SEEN[$R_NAME]=$ELAPSED
            REPLACE_SEEN_COUNT=$((REPLACE_SEEN_COUNT + 1))
        fi
    fi

    # --- смена активной джобы у cc-replace: проверяем судьбу ПРЕДЫДУЩЕЙ
    # немедленно, пока признаки свежие. Решающая проверка: если джоба
    # больше НЕ СУЩЕСТВУЕТ как объект (не Complete, не Failed — её просто
    # больше нет), при этом прожила заметно меньше заявленных 100с работы —
    # это прямое наблюдаемое доказательство прерывания, а не завершения. ---
    if [ -n "$PREV_REPLACE_NAME" ] && [ "$PREV_REPLACE_NAME" != "$R_NAME" ]; then
        if "${KCTL[@]}" get job "$PREV_REPLACE_NAME" -n "$NS" >/dev/null 2>&1; then
            COND=$("${KCTL[@]}" get job "$PREV_REPLACE_NAME" -n "$NS" \
                -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}' 2>/dev/null)
            echo "  [cc-replace] АНОМАЛИЯ: джоба $PREV_REPLACE_NAME всё ещё существует после смены активной (Complete=${COND:-<нет>})"
            STILL_PRESENT_ANOMALY="$STILL_PRESENT_ANOMALY $PREV_REPLACE_NAME"
        else
            LIFETIME=$(( ${REPLACE_LAST_SEEN[$PREV_REPLACE_NAME]:-0} - ${REPLACE_FIRST_SEEN[$PREV_REPLACE_NAME]:-0} ))
            echo "  [cc-replace] джоба $PREV_REPLACE_NAME БОЛЬШЕ НЕ СУЩЕСТВУЕТ (объект удалён контроллером) —"
            echo "               наблюдалась активной ~${LIFETIME}с из заявленных 100с работы, Complete НИКОГДА не видели"
            INTERRUPTED_JOBS="$INTERRUPTED_JOBS $PREV_REPLACE_NAME"
        fi
    fi
    PREV_REPLACE_NAME="$R_NAME"

    printf "[%s] (t+%3ds)  allow: активно=%d всего=%-3d  forbid: активно=%d всего=%-3d  replace: активно=%d текущая=%s\n" \
        "$NOW_ISO" "$ELAPSED" "$A_ACTIVE" "$A_TOTAL" "$F_ACTIVE" "$F_TOTAL" "$R_ACTIVE" "${R_NAME:-нет}"
done

echo
echo "=== снимок Job-объектов на конец наблюдения ==="
"${KCTL[@]}" get jobs -n "$NS" -o wide
echo
echo "=== события namespace $NS (полный список, по времени) ==="
"${KCTL[@]}" get events -n "$NS" --sort-by=.lastTimestamp
echo
echo "--- события, прямо относящиеся к прерыванию/удалению джоб cc-replace ---"
"${KCTL[@]}" get events -n "$NS" --sort-by=.lastTimestamp \
    | grep -iE 'cc-replace|kill|delet' || echo "(таких строк в событиях не найдено)"

# --- финальный подсчёт "всего создано" для cc-replace: реплейс-джобы
# удаляются, поэтому финальный `kubectl get jobs` их уже не покажет —
# считаем по множеству имён, увиденных активными хотя бы в одном из
# снимков за время наблюдения (сэмплинг 15с надёжно ловит каждую джобу:
# при интервале тика 60с и работе 100с каждая живёт активной ~60с,
# то есть минимум 3-4 попадания в сетку 15с). ---
R_TOTAL=$REPLACE_SEEN_COUNT
if [ -z "$INTERRUPTED_JOBS" ]; then
    INTERRUPTED_COUNT=0
else
    INTERRUPTED_COUNT=$(printf '%s\n' $INTERRUPTED_JOBS | grep -c .)
fi

A_TOTAL_FINAL=$(total_created_snapshot cc-allow)
F_TOTAL_FINAL=$(total_created_snapshot cc-forbid)

echo
echo "=== СВОДКА ==="
echo "allow:   максимум одновременно активных=${MAX_ACTIVE_ALLOW}   всего создано джоб=${A_TOTAL_FINAL}"
echo "forbid:  максимум одновременно активных=${MAX_ACTIVE_FORBID}   всего создано джоб=${F_TOTAL_FINAL}"
echo "replace: максимум одновременно активных=${MAX_ACTIVE_REPLACE}   всего создано джоб=${R_TOTAL}   из них прервано (объект удалён до Complete)=${INTERRUPTED_COUNT}"
if [ -n "$STILL_PRESENT_ANOMALY" ]; then
    echo "АНОМАЛИЯ: у cc-replace были случаи, когда предыдущая джоба НЕ исчезла после смены активной:$STILL_PRESENT_ANOMALY"
fi
echo

echo "=== ПАДАЮЩИЙ ВАРИАНТ (что означал бы провал демонстрации) ==="
echo "Если бы concurrencyPolicy не влияла на поведение, при идентичных расписании,"
echo "работе и моменте создания все три ветки дали бы ОДИНАКОВУЮ картину: либо везде"
echo "максимум активных <= 1, либо везде > 1, и у replace не нашлось бы ни одной джобы,"
echo "исчезнувшей раньше 100с работы без Complete. Наблюдалось иное:"
echo "  allow   максимум=${MAX_ACTIVE_ALLOW} (>1)      -- копит активные джобы;"
echo "  forbid  максимум=${MAX_ACTIVE_FORBID} (<=1)      -- очередной тик, заставший предыдущую джобу активной, пропущен;"
echo "  replace максимум=${MAX_ACTIVE_REPLACE} (<=1), прервано=${INTERRUPTED_COUNT} (>=1) -- идущая джоба убита и заменена новой."
echo "Различие между ветками при равных прочих условиях и есть доказательство."

# --- самопроверка: если ожидаемое неравенство не подтвердилось живым
# прогоном, это провал демонстрации, а не «почти получилось» — падаем
# с ненулевым кодом, а не молчим. ---
FAIL=0
if [ "$MAX_ACTIVE_ALLOW" -le 1 ]; then
    echo "ОШИБКА: у cc-allow (Allow) ни разу не было больше 1 активной джобы одновременно — перекрытие не подтвердилось." >&2
    FAIL=1
fi
if [ "$MAX_ACTIVE_FORBID" -gt 1 ]; then
    echo "ОШИБКА: у cc-forbid (Forbid) была замечена более чем 1 активная джоба одновременно — политика не сработала." >&2
    FAIL=1
fi
if [ "$MAX_ACTIVE_REPLACE" -gt 1 ]; then
    echo "ОШИБКА: у cc-replace (Replace) была замечена более чем 1 активная джоба одновременно — политика не сработала." >&2
    FAIL=1
fi
if [ "$INTERRUPTED_COUNT" -lt 1 ]; then
    echo "ОШИБКА: ни одна джоба cc-replace не была зафиксирована прерванной (удалённой до Complete) — демонстрация замены не подтвердилась." >&2
    FAIL=1
fi
if [ -n "$STILL_PRESENT_ANOMALY" ]; then
    echo "ОШИБКА: у cc-replace обнаружены джобы, пережившие смену активной без удаления — см. АНОМАЛИЮ выше." >&2
    FAIL=1
fi

if [ "$FAIL" -ne 0 ]; then
    echo "ПРОВАЛ ДЕМОНСТРАЦИИ — см. ошибки выше." >&2
    exit 1
fi

echo "Демонстрация подтверждена: три ветки при идентичных прочих условиях дали различную картину."
