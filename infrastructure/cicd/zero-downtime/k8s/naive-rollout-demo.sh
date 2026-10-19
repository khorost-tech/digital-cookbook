#!/usr/bin/env bash
# Раздел zero-downtime, уровень B (k8s-volga). ПАДАЮЩИЙ ВАРИАНТ: наивный выкат с
# несовместимой миграцией роняет ещё живые старые поды прямо в окне rolling
# update.
#
# Сценарий как в жизни: деплой несёт и миграцию схемы, и новый код. Применяем
# RENAME amount_cents -> total_cents и переключаем образ на v2. Пока rolling
# update не заменил все реплики, часть подов ещё бежит как v1 и обращается к
# пропавшей amount_cents — их readiness падает (/healthz 503), и Service
# перестаёт слать на них трафик. Считаем максимум одновременно НЕготовых подов
# приложения за окно выката.
#
# ⚠️ Требует доступ к k8s-volga через KUBECONFIG_VOLGA и образы IMAGE_V1/IMAGE_V2
# в доступном кластеру реестре. Голый kubectl смотрел бы в ПРОД — см. lib.sh.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

require_volga

echo "=== чистый старт: удаляем прежний namespace, если остался ==="
kubectl delete ns "$NS" --ignore-not-found --wait=true --timeout=120s

echo "=== развёртывание: namespace, PostgreSQL, приложение v1 (3 реплики) ==="
kubectl apply -f namespace.yaml
ensure_pull_secret
kubectl apply -f postgres.yaml
kubectl -n "$NS" rollout status deployment/postgres --timeout=120s
apply_tpl app.yaml
wait_rollout
echo "стартовое состояние: НЕготовых подов приложения = $(not_ready_app_pods) (ожидаем 0)"

echo
echo "=== мониторинг регрессии уходящих v1-подов во время выката ==="
trap cleanup_watch_files EXIT
watch_regressions "$IMAGE_V1"

echo "--- несовместимая миграция: RENAME amount_cents -> total_cents ---"
psql_ddl "ALTER TABLE orders RENAME COLUMN amount_cents TO total_cents"
echo "--- выкат нового образа: kubectl set image -> v2 ---"
kc set image deploy/zdt-app app="$IMAGE_V2"
wait_rollout || true

stop_watch
MAX_NOT_READY="$REGRESSIONS"

echo
echo "=== ИТОГ ==="
echo "максимум одновременно НЕготовых СТАРЫХ (v1) подов за окно выката: ${MAX_NOT_READY}"
echo
echo "ПАДАЮЩИЙ ВАРИАНТ (этот): несовместимая миграция + выкат. Ожидаем НЕготовые"
echo "СТАРЫЕ поды в окне — это ещё живые v1-реплики, обращающиеся к пропавшей"
echo "amount_cents (их readiness падает). Транзиторно НЕготовые новые v2-поды при"
echo "старте сюда не входят — считаем только регрессию старых."
echo "Рабочий путь — expand-contract-rollout-demo.sh: тот же выкат, 0 НЕготовых."

echo "--- уборка namespace ---"
cleanup_ns

if [ "${MAX_NOT_READY:-0}" -eq 0 ]; then
    echo "ОШИБКА: ни один под не стал НЕготовым — демонстрация не воспроизвела проблему." >&2
    echo "(возможно выкат прошёл слишком быстро для шага опроса; увеличь частоту или замедли rollout.)" >&2
    exit 1
fi
echo "OK: наивный выкат оставил до ${MAX_NOT_READY} НЕготовых подов в окне."
