#!/usr/bin/env bash
# teardown.sh — уборка стенда hardening.
#
# Стенд живёт в ДВУХ постоянных собственных namespace и удаляет их ЦЕЛИКОМ:
#   1) cookbook-hardening          — polygon с уровнем Pod Security restricted;
#   2) cookbook-hardening-baseline — polygon с уровнем Pod Security baseline.
# Оба создаются manifests/00-namespaces.yaml и здесь сносятся вместе со всем содержимым
# (поды, квоты, LimitRange). Плюс ВРЕМЕННЫЙ namespace демо 3:
#   3) cookbook-hardening-privileged — enforce=privileged, создаётся manifests/
#      31-seccomp-unconfined.yaml только на время снятия Seccomp: 0 и обычно сносится самой демо
#      (kubectl delete -f). Здесь удаляется страховочно, чтобы privileged-ns не остался висеть.
# namespace общих стендов серии (cookbook-k8s, cookbook-net, cookbook-argocd) стенд не создаёт
# и не трогает.
#
# Чего скрипт НЕ делает:
#   - в kube-system не заходит, чужих объектов не трогает;
#   - на узлах ничего не меняет: ни меток, ни тейнтов, ни cordon/drain (control plane
#     стенд только читает);
#   - идемпотентен: повторный запуск на уже убранном стенде проходит без ошибок
#     (--ignore-not-found).
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx (ожидался admin@k8s-volga)"; exit 1; }

# --- оба постоянных namespace + временный privileged-ns целиком ---
kubectl delete namespace cookbook-hardening cookbook-hardening-baseline cookbook-hardening-privileged --ignore-not-found
echo "namespace cookbook-hardening, cookbook-hardening-baseline и (если был) cookbook-hardening-privileged удалены со всем содержимым"

# --- что осталось нетронутым ---
# Общие namespace серии, kube-system, метки/тейнты узлов и control plane стенд не затрагивает:
# он их только читает.
echo "cookbook-k8s, cookbook-net, cookbook-argocd, kube-system и узлы не затронуты"
