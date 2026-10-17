#!/usr/bin/env bash
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx"; exit 1; }
kubectl delete namespace cookbook-net --ignore-not-found
echo "namespace cookbook-net удалён"
# cookbook-k8s и kube-system НЕ трогаем: в cookbook-k8s работают стенды фаз 1-2
# (resources/autoscaling/stateful/secrets), kube-system — системный namespace кластера
# (там живёт Cilium, который этот стенд только читает). Сеть — собственный namespace,
# ей же и удаляется целиком: default-deny в этом стенде не должен затрагивать чужие поды.
