#!/usr/bin/env bash
# apply.sh <manifest-relative-path> — проверить контекст и применить манифест.
# Rollout НЕ ждёт: ожидание вызывается отдельно (kubectl rollout status) — см. README,
# раздел «Порядок запуска».
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG to k8s-volga kubeconfig}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx (ожидался admin@k8s-volga)"; exit 1; }
f="$(dirname "$0")/$1"
kubectl apply -f "$f"
echo "applied: $1"
