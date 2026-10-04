#!/usr/bin/env bash
# Артефакт 5: startingDeadlineSeconds и пропущенные срабатывания.
#
# ⚠️ ЧЕСТНАЯ ГРАНИЦА ДЕМОНСТРАЦИИ (прочитать прежде, чем читать вывод).
# Спека серии формулирует артефакт как «пропуск запусков ПОСЛЕ ПРОСТОЯ
# КОНТРОЛЛЕРА». Настоящий простой kube-controller-manager здесь НЕ
# воспроизводится: на Talos это статический под, управляемый machine-config,
# и его остановка ради демонстрации — вмешательство в общий тестовый
# кластер k8s-volga, которым пользуются и другие работы. Вместо этого
# пропуски создаются штатным средством CronJob API — паузой расписания
# (spec.suspend: true -> пауза ~3 минуты -> spec.suspend: false).
#
# ЧТО ЭТО ЗНАЧИТ ДЛЯ ЧТЕНИЯ ВЫВОДА:
#   - Проверено этим скриптом: контроллер СЧИТАЕТ пропущенные срабатывания
#     от .status.lastScheduleTime (это видно по самому факту, что после
#     паузы догоняющая джоба — если она есть — создаётся немедленно, а не
#     ждёт следующего тика; и по событиям namespace, если контроллер их
#     печатает — см. секцию событий в конце вывода).
#   - НЕ проверено и НЕ утверждается: что код-путь при паузе расписания
#     идентичен код-пути при реальном простое kube-controller-manager.
#     Это правдоподобное предположение (оба случая — «расписание не
#     обслуживалось N минут, затем возобновилось», и оба читают одно и то
#     же поле status.lastScheduleTime), но само по себе оно НЕ проверялось
#     остановкой контроллера — источники здесь и не могли это проверить
#     без вмешательства в общий кластер. Не путать одно с другим ни в
#     тексте статьи, ни при цитировании этого вывода.
#
# ЧТО ДОКАЗЫВАЕТСЯ (см. k8s/deadline.yaml — оба CronJob идентичны и
# отличаются РОВНО полем startingDeadlineSeconds):
#   1. После паузы контроллер запускает ОДНУ догоняющую джобу, а не по
#      одной джобе на каждое пропущенное срабатывание (главное
#      контринтуитивное утверждение — распространено обратное заблуждение).
#   2. При startingDeadlineSeconds=30 не запускается и она: последнее
#      пропущенное срабатывание старше дедлайна — догон отменяется целиком,
#      расписание возобновляется только со следующего штатного тика.
#
# ПАДАЮЩИЙ ВАРИАНТ (что увидели бы, если бы startingDeadlineSeconds не
# влияла): обе ветки после снятия паузы повели бы себя ОДИНАКОВО — либо обе
# дали бы догоняющую джобу, либо ни одна. Различие между ветками при
# идентичных расписании, работе, моменте паузы и моменте снятия — и есть
# доказательство.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

NS=bj-cron
KUBECONFIG_PATH="${KVOL_KUBECONFIG:-G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig}"
KCTL=(kubectl --kubeconfig "$KUBECONFIG_PATH")

PAUSE_SECONDS="${PAUSE_SECONDS:-180}"      # ~3 минуты паузы -> 3 пропущенных тика раз в минуту
PAUSE_PRINT_STEP="${PAUSE_PRINT_STEP:-20}" # печатать прошедшее время каждые 20с во время паузы
MONITOR_SECONDS="${MONITOR_SECONDS:-130}"  # см. обоснование ниже, у цикла наблюдения
MONITOR_INTERVAL="${MONITOR_INTERVAL:-5}"
BASELINE_TIMEOUT="${BASELINE_TIMEOUT:-150}" # ожидание первого штатного lastScheduleTime у обеих веток
# Обязан совпадать со startingDeadlineSeconds у ветки dl-deadline30 в
# k8s/deadline.yaml: от него считается окно снятия паузы (см. Шаг 3). Если
# поменять значение в манифесте и забыть здесь — окно окажется неверным, ветки
# не разойдутся, и демонстрация провалится, а не соврёт (проверка ниже).
DEADLINE_SECONDS="${DEADLINE_SECONDS:-30}"

# =========================================================================
# --- Preflight (тот же приём, что в concurrency-demo.sh) — ГЛАВНАЯ защита
# от удара в прод: голый kubectl в этом окружении бьёт в admin@k8s-tula
# (единственный контекст в ~/.kube/config), поэтому ниже используется
# ИСКЛЮЧИТЕЛЬНО --kubeconfig "$KUBECONFIG_PATH", а кластер дополнительно
# проверяется по именам нод — они обязаны начинаться с "k8s-volga-". ---
# =========================================================================
echo "=== preflight ==="

if [ ! -f "$KUBECONFIG_PATH" ]; then
    echo "ОШИБКА: kubeconfig k8s-volga не найден по пути: $KUBECONFIG_PATH" >&2
    echo "Проверьте путь (можно переопределить переменной KVOL_KUBECONFIG)." >&2
    exit 1
fi
echo "kubeconfig: $KUBECONFIG_PATH — файл существует"

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

echo "=== честная граница демонстрации (см. также комментарий в начале файла) ==="
echo "Воспроизводится ПАУЗА РАСПИСАНИЯ (spec.suspend: true -> false), а НЕ остановка"
echo "kube-controller-manager. Утверждение 'контроллер считает пропуски одинаково в"
echo "обоих случаях' в этом прогоне НЕ проверялось остановкой контроллера — не выдавать"
echo "за проверенный факт ни здесь, ни в статье."
echo

# --- Уборка через trap: удаляем ТОЛЬКО namespace bj-cron. ---
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
"${KCTL[@]}" apply -f k8s/deadline.yaml

CJ_COUNT=$("${KCTL[@]}" get cronjob -n "$NS" --no-headers 2>/dev/null | wc -l | tr -d ' ')
if [ "$CJ_COUNT" != "2" ]; then
    echo "ОШИБКА: после apply в namespace $NS обнаружено CronJob'ов: $CJ_COUNT (ожидалось 2)." >&2
    echo "Манифест не применился полностью — демонстрация невозможна." >&2
    exit 1
fi
echo "созданы 2 CronJob: dl-nodeadline (без startingDeadlineSeconds), dl-deadline30 (=30)"
"${KCTL[@]}" get cronjob -n "$NS"
echo

# --- вспомогательные функции ---
last_schedule_time() {
    "${KCTL[@]}" get cronjob "$1" -n "$NS" -o jsonpath='{.status.lastScheduleTime}' 2>/dev/null
}
job_names() {
    "${KCTL[@]}" get jobs -n "$NS" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null \
        | grep "^$1-" || true
}
job_count() {
    local n
    n=$(job_names "$1")
    if [ -z "$n" ]; then echo 0; else printf '%s\n' "$n" | grep -c .; fi
}
job_created_at() {
    "${KCTL[@]}" get job "$1" -n "$NS" -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null
}

# =========================================================================
# --- Шаг 1: ждём, пока ОБЕ ветки отработают хотя бы одно штатное
# срабатывание — без .status.lastScheduleTime контроллер считал бы
# пропуски от .metadata.creationTimestamp, и ветки стартовали бы из
# разных фактических состояний (обе ветки применены в одном apply, так
# что их первый тик приходится на одну и ту же минуту UTC — это не
# требует дополнительной синхронизации). ---
# =========================================================================
echo "=== жду первое штатное срабатывание у обеих веток (таймаут ${BASELINE_TIMEOUT}с) ==="
WAITED=0
LST_A=""
LST_B=""
while [ "$WAITED" -lt "$BASELINE_TIMEOUT" ]; do
    LST_A=$(last_schedule_time dl-nodeadline)
    LST_B=$(last_schedule_time dl-deadline30)
    if [ -n "$LST_A" ] && [ -n "$LST_B" ]; then
        echo "обе ветки получили lastScheduleTime после ${WAITED}с ожидания"
        break
    fi
    sleep 5
    WAITED=$((WAITED + 5))
    echo "  t+${WAITED}с: dl-nodeadline.lastScheduleTime='${LST_A:-<пусто>}' dl-deadline30.lastScheduleTime='${LST_B:-<пусто>}'"
done
if [ -z "$LST_A" ] || [ -z "$LST_B" ]; then
    echo "ОШИБКА: за ${BASELINE_TIMEOUT}с хотя бы одна ветка не получила lastScheduleTime — демонстрация невозможна." >&2
    exit 1
fi

BASELINE_A=$(job_names dl-nodeadline)
BASELINE_B=$(job_names dl-deadline30)
BASELINE_A_COUNT=$(job_count dl-nodeadline)
BASELINE_B_COUNT=$(job_count dl-deadline30)

echo
echo "=== состояние ДО паузы (baseline) ==="
echo "dl-nodeadline: lastScheduleTime=$LST_A  джоб создано=$BASELINE_A_COUNT"
echo "dl-deadline30: lastScheduleTime=$LST_B  джоб создано=$BASELINE_B_COUNT"
"${KCTL[@]}" get jobs -n "$NS" -o wide
echo

# =========================================================================
# --- Шаг 2: пауза ОБЕИХ веток в один момент. kubectl patch не умеет
# принимать несколько имён или label-селектор (только TYPE NAME) — поэтому
# "один момент" обеспечивается запуском двух patch-вызовов ПАРАЛЛЕЛЬНО (в
# фоне, с последующим wait), а не последовательно. ---
# =========================================================================
echo "=== ставлю обе ветки на паузу (suspend: true) в один момент ==="
PAUSE_START_ISO=$(date -u +%H:%M:%S)
PAUSE_START_TS=$(date -u +%s)
"${KCTL[@]}" patch cronjob dl-nodeadline -n "$NS" --type=merge -p '{"spec":{"suspend":true}}' >/dev/null &
PID1=$!
"${KCTL[@]}" patch cronjob dl-deadline30 -n "$NS" --type=merge -p '{"spec":{"suspend":true}}' >/dev/null &
PID2=$!
wait "$PID1" "$PID2"
echo "пауза поставлена в $PAUSE_START_ISO UTC (обе ветки, параллельными patch-вызовами)"
"${KCTL[@]}" get cronjob -n "$NS" -o custom-columns='NAME:.metadata.name,SUSPEND:.spec.suspend'
echo

echo "=== держу паузу ~${PAUSE_SECONDS}с (~$((PAUSE_SECONDS / 60)) минут, время UTC) ==="
ELAPSED=0
while [ "$ELAPSED" -lt "$PAUSE_SECONDS" ]; do
    STEP=$PAUSE_PRINT_STEP
    REMAIN=$((PAUSE_SECONDS - ELAPSED))
    if [ "$STEP" -gt "$REMAIN" ]; then STEP=$REMAIN; fi
    sleep "$STEP"
    ELAPSED=$((ELAPSED + STEP))
    # Пауза ПРОВЕРЯЕТСЯ на каждом шаге, а не считается выставленной один раз в
    # начале. Без этой проверки замер не отличил бы «пауза держалась, запуски
    # пропущены» от «пауза не применилась, джобы шли штатно» — а счётчик
    # догоняющих в обоих случаях оказался бы ненулевым и выглядел правдоподобно.
    # Заодно видно главное: пока пауза стоит, новых джоб НЕ появляется.
    SUSP_A=$("${KCTL[@]}" get cronjob dl-nodeadline -n "$NS" -o jsonpath='{.spec.suspend}' 2>/dev/null)
    SUSP_B=$("${KCTL[@]}" get cronjob dl-deadline30 -n "$NS" -o jsonpath='{.spec.suspend}' 2>/dev/null)
    JOBS_NOW=$("${KCTL[@]}" get jobs -n "$NS" --no-headers 2>/dev/null | wc -l | tr -d ' ')
    echo "  прошло ${ELAPSED}с из ${PAUSE_SECONDS}с ($(date -u +%H:%M:%S) UTC) — suspend: nodeadline=${SUSP_A:-?} deadline30=${SUSP_B:-?}, джоб всего=${JOBS_NOW}"
    if [ "$SUSP_A" != "true" ] || [ "$SUSP_B" != "true" ]; then
        echo "ОШИБКА: пауза слетела посреди наблюдения (nodeadline=${SUSP_A:-?}, deadline30=${SUSP_B:-?})." >&2
        echo "Замер недействителен: пропущенных срабатываний не было, считать догоняющие бессмысленно." >&2
        exit 1
    fi
done
echo

# =========================================================================
# --- Шаг 3: снятие паузы ОБЕИХ веток в один момент, тем же приёмом.
#
# ⚠️ МОМЕНТ СНЯТИЯ ПАУЗЫ КОНТРОЛИРУЕТСЯ, и это не косметика — без этого
# демонстрация не работает. Контроллер смотрит на БЛИЖАЙШЕЕ пропущенное
# срабатывание, а не на все подряд, и сравнивает с дедлайном именно его
# возраст. При расписании раз в минуту ближайшая пропущенная граница — это
# начало текущей минуты, то есть ей от 0 до 59 секунд. Сними паузу в первые
# 30 секунд минуты — и она моложе дедлайна в 30с, догон пройдёт В ОБЕИХ
# ветках, а различие, ради которого всё затевалось, не проявится.
#
# Так и случилось в первом прогоне: обе ветки дали по одной догоняющей джобе,
# и скрипт честно объявил провал демонстрации. Дефект был в конструкции
# замера, а не в Kubernetes.
#
# Поэтому ждём окна [DEADLINE+10 .. 55] секунд от начала минуты: тогда
# ближайшее пропущенное срабатывание заведомо СТАРШЕ дедлайна в 30с, и ветки
# обязаны разойтись. Верхняя граница 55 — чтобы не упереться в следующую
# минуту, пока идут два patch-вызова.
# =========================================================================
RESUME_WINDOW_LO=$((DEADLINE_SECONDS + 10))
RESUME_WINDOW_HI=55
echo "=== жду окна снятия паузы: ${RESUME_WINDOW_LO}-${RESUME_WINDOW_HI}с от начала минуты ==="
echo "(чтобы ближайшее пропущенное срабатывание было СТАРШЕ дедлайна ${DEADLINE_SECONDS}с)"
while :; do
    SEC=$(date -u +%S); SEC=${SEC#0}
    [ "$SEC" -ge "$RESUME_WINDOW_LO" ] && [ "$SEC" -le "$RESUME_WINDOW_HI" ] && break
    sleep 1
done
echo "окно поймано на ${SEC}с от начала минуты ($(date -u +%H:%M:%S) UTC)"
echo

echo "=== снимаю паузу (suspend: false) в один момент ==="
RESUME_START_ISO=$(date -u +%H:%M:%S)
RESUME_START_TS=$(date -u +%s)
"${KCTL[@]}" patch cronjob dl-nodeadline -n "$NS" --type=merge -p '{"spec":{"suspend":false}}' >/dev/null &
PID1=$!
"${KCTL[@]}" patch cronjob dl-deadline30 -n "$NS" --type=merge -p '{"spec":{"suspend":false}}' >/dev/null &
PID2=$!
wait "$PID1" "$PID2"
echo "пауза снята в $RESUME_START_ISO UTC (обе ветки)"
echo "фактическая длительность паузы: $((RESUME_START_TS - PAUSE_START_TS))с"
echo

# =========================================================================
# --- Шаг 4: наблюдение за новыми джобами. Окно ~130с (шире, чем базовые
# "~90с" из брифа) выбрано намеренно: следующий ШТАТНЫЙ тик после снятия
# паузы может отстоять от момента снятия почти на целую минуту (снятие
# паузы не привязано к границе минуты), плюс период синхронизации
# CronJob-контроллера (~10с) добавляет задержку — 90с окна иногда не
# хватило бы, чтобы застать САМ следующий штатный тик и подтвердить, что
# dl-deadline30 действительно возобновляет расписание нормально (а не
# просто "тихо ничего не делает"). Догоняющая джоба (если она будет)
# ожидается значительно раньше — в первые секунды после снятия паузы,
# видна независимо от ширины окна. ---
# =========================================================================
# Классификация по СЛОТУ РАСПИСАНИЯ, а не по времени появления джобы.
#
# Первая версия считала догоняющей всякую джобу, замеченную в первые 30с после
# снятия паузы. Это неверно и смазывало весь результат: если паузу снять,
# скажем, за 15с до границы минуты, очередное ШТАТНОЕ срабатывание попадает в
# то же окно и засчитывается как догоняющее. Ровно так ветка с дедлайном
# «показала» догоняющую джобу, хотя догона у неё не было.
#
# Kubernetes кодирует слот расписания в имени джобы: суффикс — это номер
# минуты в Unix-времени. Умножив на 60, получаем момент, НА КОТОРЫЙ джоба
# была запланирована. Догоняющая — та, чей слот РАНЬШЕ момента снятия паузы;
# штатная — чей слот позже. Это признак по существу, а не по таймингу
# наблюдателя.
classify_job() {
    local slot_min="${1##*-}"
    case "$slot_min" in
        ''|*[!0-9]*) echo "неизвестно (имя не разобрано)"; return ;;
    esac
    local slot_ts=$((slot_min * 60))
    if [ "$slot_ts" -lt "$RESUME_START_TS" ]; then
        echo "ДОГОНЯЮЩАЯ (слот $(date -u -d "@$slot_ts" +%H:%M:%S) — до снятия паузы)"
    else
        echo "штатная (слот $(date -u -d "@$slot_ts" +%H:%M:%S) — после снятия паузы)"
    fi
}

echo "=== наблюдение ${MONITOR_SECONDS}с за новыми джобами (интервал ${MONITOR_INTERVAL}с) ==="
NEWJOBS_A=""   # строки "имя elapsed_с класс"
NEWJOBS_B=""
SEEN_A=""
SEEN_B=""
MON_ELAPSED=0
while [ "$MON_ELAPSED" -lt "$MONITOR_SECONDS" ]; do
    sleep "$MONITOR_INTERVAL"
    MON_ELAPSED=$((MON_ELAPSED + MONITOR_INTERVAL))

    CUR_A=$(job_names dl-nodeadline)
    CUR_B=$(job_names dl-deadline30)

    for name in $CUR_A; do
        if ! printf '%s\n' "$BASELINE_A" | grep -qx "$name" && ! printf '%s\n' "$SEEN_A" | grep -qx "$name"; then
            SEEN_A="$SEEN_A
$name"
            CREATED=$(job_created_at "$name")
            CLASS=$(classify_job "$name")
            NEWJOBS_A="$NEWJOBS_A
$name|$MON_ELAPSED|$CLASS|$CREATED"
            echo "  [dl-nodeadline] НОВАЯ джоба $name замечена на t+${MON_ELAPSED}с после снятия паузы (creationTimestamp=$CREATED) -> $CLASS"
        fi
    done
    for name in $CUR_B; do
        if ! printf '%s\n' "$BASELINE_B" | grep -qx "$name" && ! printf '%s\n' "$SEEN_B" | grep -qx "$name"; then
            SEEN_B="$SEEN_B
$name"
            CREATED=$(job_created_at "$name")
            CLASS=$(classify_job "$name")
            NEWJOBS_B="$NEWJOBS_B
$name|$MON_ELAPSED|$CLASS|$CREATED"
            echo "  [dl-deadline30] НОВАЯ джоба $name замечена на t+${MON_ELAPSED}с после снятия паузы (creationTimestamp=$CREATED) -> $CLASS"
        fi
    done
done
echo

LST_A_AFTER=$(last_schedule_time dl-nodeadline)
LST_B_AFTER=$(last_schedule_time dl-deadline30)

echo "=== снимок Job-объектов на конец наблюдения ==="
"${KCTL[@]}" get jobs -n "$NS" -o wide
echo

echo "=== события namespace $NS (полный список, по времени) ==="
"${KCTL[@]}" get events -n "$NS" --sort-by=.lastTimestamp
echo
echo "--- события, потенциально относящиеся к пропущенным срабатываниям/созданию джоб ---"
"${KCTL[@]}" get events -n "$NS" --sort-by=.lastTimestamp \
    | grep -iE 'dl-nodeadline|dl-deadline30|miss|deadline|successfulcreate' \
    || echo "(таких строк в событиях не найдено — см. оговорку в разборе ниже)"
echo

# --- подсчёт по классам ---
count_class() {
    # $1 = список "имя|elapsed|класс|created", $2 = префикс класса.
    # Сравнение по ПРЕФИКСУ: класс теперь несёт ещё и расшифровку слота
    # ("ДОГОНЯЮЩАЯ (слот 11:00:00 — до снятия паузы)"), точное равенство не
    # подошло бы.
    printf '%s\n' "$1" | awk -F'|' -v c="$2" 'NF>=3 && index($3, c)==1 {n++} END{print n+0}'
}
CATCHUP_A=$(count_class "$NEWJOBS_A" "ДОГОНЯЮЩАЯ")
NEXT_A=$(count_class "$NEWJOBS_A" "штатная")
CATCHUP_B=$(count_class "$NEWJOBS_B" "ДОГОНЯЮЩАЯ")
NEXT_B=$(count_class "$NEWJOBS_B" "штатная")

echo "=== СВОДКА ==="
echo "dl-nodeadline (startingDeadlineSeconds не задано):"
echo "  lastScheduleTime до паузы:  $LST_A"
echo "  lastScheduleTime после:     $LST_A_AFTER"
echo "  догоняющих джоб (слот РАНЬШЕ снятия паузы): $CATCHUP_A"
echo "  штатных джоб (слот ПОСЛЕ снятия паузы):              $NEXT_A"
echo
echo "dl-deadline30 (startingDeadlineSeconds: 30):"
echo "  lastScheduleTime до паузы:  $LST_B"
echo "  lastScheduleTime после:     $LST_B_AFTER"
echo "  догоняющих джоб (слот РАНЬШЕ снятия паузы): $CATCHUP_B"
echo "  штатных джоб (слот ПОСЛЕ снятия паузы):              $NEXT_B"
echo

if [ "$CATCHUP_A" -gt 1 ]; then
    echo "=== НАХОДКА, не ошибка: у dl-nodeadline догоняющих джоб больше одной (${CATCHUP_A}) ==="
    echo "Бриф прямо предупреждает: это фиксируется как факт, а не подгоняется под ожидание"
    echo "'ровно одна'. Возможные причины требуют отдельного разбора (см. отчёт задачи), а не"
    echo "автоматической правки этого вывода."
    echo
fi

echo "=== ЧЕСТНАЯ ГРАНИЦА (повтор) ==="
echo "Выше воспроизводилась ПАУЗА РАСПИСАНИЯ (suspend), а не остановка kube-controller-manager."
echo "Совпадение код-пути между этими двумя случаями предположено, но НЕ проверено остановкой"
echo "контроллера в этом прогоне."
echo

echo "=== ПАДАЮЩИЙ ВАРИАНТ (что означал бы провал демонстрации) ==="
echo "Если бы startingDeadlineSeconds не влияла на поведение, обе ветки после снятия паузы"
echo "повели бы себя ОДИНАКОВО при идентичных расписании, работе, моменте паузы и моменте"
echo "снятия — либо обе дали бы догоняющую джобу, либо ни одна. Наблюдалось:"
echo "  dl-nodeadline (без дедлайна): догоняющих=${CATCHUP_A}"
echo "  dl-deadline30 (дедлайн 30с):  догоняющих=${CATCHUP_B}"
echo "Различие между ветками при равных прочих условиях и есть доказательство."

# --- самопроверка: провал демонстрации -> ненулевой код выхода, не молчание. ---
FAIL=0
if [ "$CATCHUP_A" -lt 1 ]; then
    echo "ОШИБКА: у dl-nodeadline НЕ зафиксировано ни одной догоняющей джобы — главный тезис не подтвердился." >&2
    FAIL=1
fi
if [ "$CATCHUP_B" -ne 0 ]; then
    echo "ОШИБКА: у dl-deadline30 зафиксирована догоняющая джоба (${CATCHUP_B}) — startingDeadlineSeconds не сработал как ожидалось." >&2
    FAIL=1
fi
if [ "$CATCHUP_A" -eq "$CATCHUP_B" ]; then
    echo "ОШИБКА: число догоняющих джоб у веток совпало (${CATCHUP_A} = ${CATCHUP_B}) — различие не подтвердилось." >&2
    FAIL=1
fi

if [ "$FAIL" -ne 0 ]; then
    echo "ПРОВАЛ ДЕМОНСТРАЦИИ — см. ошибки выше." >&2
    exit 1
fi

echo
echo "Демонстрация подтверждена: при идентичных прочих условиях ветки дали различную картину"
echo "догоняющих джоб после снятия паузы расписания."
