#!/usr/bin/env bash
# Общие функции для демо уровня B (k8s-volga). Подключается через source.
#
# ⚠️ БЕЗОПАСНОСТЬ: голый kubectl в этом окружении смотрит в ПРОД-кластер
# k8s-tula. Поэтому демо НИКОГДА не работают с текущим контекстом по умолчанию:
# require_volga требует явный kubeconfig в KUBECONFIG_VOLGA и проверяет, что это
# действительно k8s-volga (по имени контекста и узлов), иначе прерывается.
NS="${NS:-cicd-zdt}"
# Реестр, куда запушены образы v1/v2 (Harbor на volga). Задаётся снаружи.
IMAGE_V1="${IMAGE_V1:?переменная IMAGE_V1 не задана (полное имя образа приложения v1)}"
IMAGE_V2="${IMAGE_V2:?переменная IMAGE_V2 не задана (полное имя образа приложения v2)}"
# Промежуточная версия перехода (пишет обе колонки, читает переходным COALESCE)
# нужна ТОЛЬКО expand-contract-сценарию. Здесь она опциональна намеренно: иначе
# naive-rollout-demo.sh, которому этот образ не нужен, падал бы на загрузке
# библиотеки, хотя запускается ровно по документированным переменным.
# Обязательность требует сам expand-скрипт.
IMAGE_DUAL="${IMAGE_DUAL:-}"

# Паттерн, по которому опознаём узлы/контекст k8s-volga. Уточняется при первом
# живом подключении (kubectl get nodes) и при необходимости переопределяется
# через VOLGA_PATTERN.
VOLGA_PATTERN="${VOLGA_PATTERN:-volga}"

K8S_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# require_volga: единственная точка входа в кластер. Экспортирует KUBECONFIG на
# указанный волговский конфиг и проверяет, что это не прод.
require_volga() {
    : "${KUBECONFIG_VOLGA:?переменная KUBECONFIG_VOLGA не задана (путь к kubeconfig k8s-volga)}"
    [ -r "$KUBECONFIG_VOLGA" ] || { echo "ОШИБКА: kubeconfig $KUBECONFIG_VOLGA не читается." >&2; exit 1; }
    export KUBECONFIG="$KUBECONFIG_VOLGA"

    local ctx nodes
    ctx="$(kubectl config current-context 2>/dev/null || true)"
    case "$ctx" in
        *tula*) echo "ОШИБКА: контекст '$ctx' похож на ПРОД k8s-tula — отказ." >&2; exit 1 ;;
    esac

    nodes="$(kubectl get nodes -o name 2>/dev/null || true)"
    if [ -z "$nodes" ]; then
        echo "ОШИБКА: kubectl не вернул узлов — кластер недоступен по $KUBECONFIG_VOLGA." >&2
        exit 1
    fi
    if ! printf '%s\n' "$nodes" | grep -qi "$VOLGA_PATTERN"; then
        echo "ОШИБКА: узлы не опознаны как k8s-volga (паттерн '$VOLGA_PATTERN'):" >&2
        printf '%s\n' "$nodes" >&2
        echo "Если это точно volga, задай VOLGA_PATTERN под реальные имена узлов." >&2
        exit 1
    fi
    echo "кластер k8s-volga подтверждён (контекст '$ctx', узлы совпали с '$VOLGA_PATTERN')."
}

kc() { kubectl -n "$NS" "$@"; }

# Число НЕготовых подов приложения прямо сейчас (все версии образа).
not_ready_app_pods() {
    kubectl -n "$NS" get pods -l app=zdt-app \
        -o 'jsonpath={range .items[*]}{.status.containerStatuses[0].ready}{"\n"}{end}' 2>/dev/null \
        | grep -c false || true
}

# Число РЕГРЕССИРОВАВШИХ подов ЗАДАННОГО образа прямо сейчас: readiness=false
# И под НЕ завершается (deletionTimestamp пуст). Это точный сигнал
# несовместимости: под, который ДОЛЖЕН обслуживать, но не может.
# Исключаем две нормальные для любого rolling update ситуации:
#   - новые поды, транзиторно НЕготовые при старте (у них другой образ);
#   - уходящие поды в состоянии Terminating (их readiness штатно гаснет при
#     завершении — у них выставлен deletionTimestamp).
#
# Образ передаётся ПАРАМЕТРОМ: при выкате dual -> v2 уходящей версией является
# dual, и фильтр по IMAGE_V1 давал бы неизбежный ноль, ничего не проверяя.
# Возвращает ненулевой код, если ОПРОСИТЬ кластер не удалось. Это принципиально:
# при `kubectl ... 2>/dev/null | wc -l` сбой опроса давал бы 0 — то есть
# «регрессий нет», и отсутствие наблюдения было бы неотличимо от отсутствия
# проблемы. Наблюдение, которое не состоялось, обязано быть ошибкой, а не нулём.
not_ready_image_pods() {
    local img="$1" out
    # --request-timeout обязателен: по умолчанию у kubectl он 0 (без предела).
    # Зависший API-сервер тогда подвесил бы и монитор, и ожидающий его wait —
    # то есть fail-closed превратился бы в «висим молча».
    out="$(kubectl -n "$NS" --request-timeout=5s get pods -l app=zdt-app \
        -o 'jsonpath={range .items[*]}{.spec.containers[0].image}|{.status.containerStatuses[0].ready}|{.metadata.deletionTimestamp}{"\n"}{end}')" || return 1
    printf '%s\n' "$out" | awk -F'|' -v img="$img" '$1==img && $2=="false" && $3==""' | wc -l
}

# Совместимость: старые вызовы без параметра проверяют образ v1.
not_ready_old_pods() { not_ready_image_pods "$IMAGE_V1"; }


# --- монитор регрессий уходящей версии ---------------------------------------
#
# Два свойства, без которых монитор врёт зелёным:
#   1. работает до ЯВНОГО сигнала остановки, а не фиксированное число итераций —
#      выкат может идти дольше окна наблюдения, и его хвост остался бы невиден;
#   2. падает при любом сбое опроса и считает выборки. Наблюдение, которое не
#      состоялось, — это ОШИБКА, а не «регрессий нет».
#
# Результат кладётся в глобальную REGRESSIONS, а не печатается в stdout: вызов
# вида VAR="$(stop_watch)" создаёт субшелл, где `wait` не видит фоновый процесс
# родительского шелла и всегда возвращает ошибку «not a child».
MAXFILE=""; SAMPLES=""; STOPFILE=""; MON=""; REGRESSIONS=0

watch_regressions() {
    local img="$1"
    MAXFILE="${MAXFILE:-$(mktemp)}"; SAMPLES="${SAMPLES:-$(mktemp)}"
    STOPFILE="${STOPFILE:-$(mktemp -u)}"
    echo 0 > "$MAXFILE"; echo 0 > "$SAMPLES"; rm -f "$STOPFILE"
    (
        while [ ! -e "$STOPFILE" ]; do
            nr="$(not_ready_image_pods "$img")" || exit 3   # сбой опроса — не ноль
            cur="$(cat "$MAXFILE")"
            [ "$nr" -gt "$cur" ] && echo "$nr" > "$MAXFILE"
            echo $(( $(cat "$SAMPLES") + 1 )) > "$SAMPLES"
            sleep 0.3
        done
    ) &
    MON=$!
}

stop_watch() {
    : > "$STOPFILE"
    # Ждём завершения с ПРЕДЕЛОМ: голый `wait` без ограничения повис бы вместе
    # с зависшим опросом, и fail-closed не сработал бы.
    local i=0
    while kill -0 "$MON" 2>/dev/null && [ "$i" -lt 100 ]; do sleep 0.1; i=$((i + 1)); done
    if kill -0 "$MON" 2>/dev/null; then
        echo "ОШИБКА: монитор не завершился за 10с — вероятно, опрос кластера завис." >&2
        kill -KILL "$MON" 2>/dev/null || true
        wait "$MON" 2>/dev/null || true
        return 1
    fi
    if ! wait "$MON"; then
        echo "ОШИБКА: монитор регрессий завершился со сбоем — опрос кластера не удался." >&2
        echo "Наблюдение не состоялось; считать это отсутствием регрессий нельзя." >&2
        return 1
    fi
    local n; n="$(cat "$SAMPLES")"
    if [ "$n" -lt 10 ]; then
        echo "ОШИБКА: за окно выката снято лишь ${n} выборок — слишком мало, чтобы судить." >&2
        return 1
    fi
    echo "  (выборок за окно выката: ${n})"
    REGRESSIONS="$(cat "$MAXFILE")"
}


# Аварийный выход: сначала ОСТАНОВИТЬ монитор, только потом удалять файлы.
# Порядок принципиален: цикл монитора крутится, пока НЕТ STOPFILE, поэтому
# удаление файла первым закрепляло бы условие продолжения — процесс остался бы
# опрашивать кластер после падения сценария.
cleanup_watch_files() {
    if [ -n "$MON" ] && kill -0 "$MON" 2>/dev/null; then
        : > "$STOPFILE"                      # штатный сигнал остановки
        local i=0
        while kill -0 "$MON" 2>/dev/null && [ "$i" -lt 30 ]; do sleep 0.1; i=$((i + 1)); done
        # Grace истёк — снимаем жёстко: иначе зависший опрос оставил бы процесс
        # висеть после падения сценария.
        kill -KILL "$MON" 2>/dev/null || true
        wait "$MON" 2>/dev/null || true
    fi
    rm -f "$MAXFILE" "$SAMPLES" "$STOPFILE"
}


# Дождаться, пока все реплики Deployment готовы.
wait_rollout() { kubectl -n "$NS" rollout status deploy/zdt-app --timeout=120s; }

# Убрать namespace стенда целиком.
cleanup_ns() { kubectl delete ns "$NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }

# Применить манифест с подстановкой переменных окружения (IMAGE_V1 и пр.).
apply_tpl() { envsubst < "$1" | kubectl apply -f -; }

# Если реестр приватный (заданы HARBOR_SERVER/USER/PASS) — создать pull-secret и
# повесить его на default-SA namespace. Для публичного проекта Harbor не нужно.
ensure_pull_secret() {
    [ -n "${HARBOR_SERVER:-}" ] || return 0
    kubectl -n "$NS" create secret docker-registry harbor-cred \
        --docker-server="$HARBOR_SERVER" \
        --docker-username="${HARBOR_USER:?HARBOR_USER не задан}" \
        --docker-password="${HARBOR_PASS:?HARBOR_PASS не задан}" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    kubectl -n "$NS" patch serviceaccount default \
        -p '{"imagePullSecrets":[{"name":"harbor-cred"}]}' >/dev/null
}

# Несовместимая миграция: RENAME колонки в БД кластера.
psql_cluster() {
    kubectl -n "$NS" exec deployment/postgres -- psql -U zdt -d zdt "$@"
}

# psql_ddl "SQL" — выполнить ТЯЖЁЛЫЙ DDL под lock_timeout с повторами.
#
# Любой ALTER TABLE, берущий ACCESS EXCLUSIVE (ADD/DROP COLUMN, SET NOT NULL,
# ADD/DROP CONSTRAINT), ждёт завершения активных транзакций и, пока ждёт, копит
# за собой очередь новых запросов. Без lock_timeout одна долгая транзакция
# превращает «мгновенную» операцию в остановку приёма трафика. Поэтому каждый
# такой DDL идёт через этот helper: ограниченное ожидание, ограниченное число
# попыток и fail-closed — не смогли взять блокировку, значит миграция не
# состоялась, а не «как-нибудь потом».
psql_ddl() {
    local sql="$1" attempts="${2:-3}" i out
    for ((i = 1; i <= attempts; i++)); do
        # Присваивание с подстановкой команды ОБЯЗАНО стоять в условном
        # контексте. Голое `out="$(...)"; rc=$?` при set -e не работает:
        # статусом присваивания становится статус команды, и shell завершается
        # ДО строки с $? — ни классификации ошибки, ни печати $out не будет.
        if out="$(psql_cluster -v ON_ERROR_STOP=1 -c "SET lock_timeout = '2s'; $sql" 2>&1)"; then
            return 0
        fi
        # Повторять осмысленно ТОЛЬКО таймаут блокировки. Синтаксическая ошибка,
        # нарушение ограничения или обрыв соединения от повтора не исчезнут, а
        # описывать их как «блокировка не взята» — вводить в заблуждение.
        if printf '%s' "$out" | grep -qiE 'lock timeout|55P03'; then
            echo "  попытка ${i}/${attempts}: блокировка не взята за 2с — ${sql:0:56}…" >&2
            sleep 2
            continue
        fi
        echo "ОШИБКА PostgreSQL (не таймаут блокировки) — повтор бессмыслен:" >&2
        printf '%s\n' "$out" >&2
        return 1
    done
    echo "ОШИБКА: блокировку не удалось взять за ${attempts} попыток: ${sql}" >&2
    echo "Проверь долгие транзакции: SELECT * FROM pg_stat_activity WHERE state <> 'idle';" >&2
    return 1
}


