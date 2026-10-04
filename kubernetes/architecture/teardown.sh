#!/usr/bin/env bash
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx"; exit 1; }
kubectl -n cookbook-k8s delete all,cm,secret,sa,role,rolebinding -l stand=cookbook-architecture --ignore-not-found
echo "объекты стенда architecture (label stand=cookbook-architecture) в namespace cookbook-k8s удалены"
# namespace cookbook-k8s НЕ удаляется: общий для всех стендов серии (resources, stateful и т.д.).
# kube-system и control plane этот скрипт не трогает вовсе — стенд их только читает,
# ничего в них не создаёт и, соответственно, ничего там не удаляет.
