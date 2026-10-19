#!/usr/bin/env bash
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx"; exit 1; }
kubectl -n cookbook-k8s delete all,secret,secretstore,externalsecret,sealedsecret,sa,role,rolebinding -l stand=cookbook-secrets --ignore-not-found
echo "объекты стенда secrets (label stand=cookbook-secrets) в namespace cookbook-k8s удалены"
# namespace cookbook-k8s НЕ удаляется: общий для всех стендов серии (resources, stateful и т.д.).
# Операторы (ESO external-secrets, sealed-secrets-controller) и их CRD/ключи не трогаем —
# они вне жизненного цикла этого стенда, общие для кластера.
# Чужие объекты без label stand=cookbook-secrets (StatefulSet demo и другое) не затрагиваются.
