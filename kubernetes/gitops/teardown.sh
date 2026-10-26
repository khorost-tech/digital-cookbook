#!/usr/bin/env bash
# teardown.sh — уборка стенда gitops.
#
# Стенд, в отличие от стендов фаз 1-3, живёт в СВОЁМ namespace и ставит кластерные CRD,
# поэтому шаги перечислены явно:
#   1) namespace cookbook-argocd удаляется ЦЕЛИКОМ — в нём и git-сервер, и ArgoCD стенда
#      (M1), и демо-приложение, которое ArgoCD выкатывает. Общего с другими стендами
#      в этом namespace нет ничего, поэтому точечная уборка по меткам не нужна.
#   2) CRD Argo Rollouts (ставятся в M4) — объекты КЛАСТЕРНЫЕ, удалением namespace не
#      убираются. Перечислены ЯВНЫМ списком имён, а не маской *.argoproj.io: под маску
#      попали бы чужие CRD боевого ArgoCD.
#
# ⛔ Чего скрипт НЕ делает и делать не должен:
#   3) НЕ трогает namespace argocd — там живёт БОЕВОЙ ArgoCD, управляющий реальными
#      сервисами. Стенд его только читает.
#   4) НЕ трогает чужие CRD: applications.argoproj.io, applicationsets.argoproj.io,
#      appprojects.argoproj.io, imageupdaters.argocd-image-updater.argoproj.io —
#      они принадлежат Helm-релизу боевого ArgoCD. ArgoCD стенда namespace-scoped и
#      переиспользует эти же CRD, своих не ставит; удаление любого из них снесло бы
#      боевые Application вместе с определением типа.
#   5) НЕ трогает узлы, control plane, kube-system и стенды фаз 1-4 (cookbook-k8s,
#      cookbook-net).
#   6) Идемпотентен: повторный запуск на убранном стенде проходит без ошибок
#      (--ignore-not-found везде).
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx (ожидался admin@k8s-volga)"; exit 1; }

# CRD, которые ставит Argo Rollouts (M4) и которые уносит этот скрипт.
ROLLOUTS_CRDS="
rollouts.argoproj.io
analysisruns.argoproj.io
analysistemplates.argoproj.io
clusteranalysistemplates.argoproj.io
experiments.argoproj.io
"
# Чужие CRD боевого ArgoCD. Список нужен не для удаления, а для проверки: если он
# пересечётся со списком выше — скрипт останавливается, не удалив ничего.
FOREIGN_CRDS="
applications.argoproj.io
applicationsets.argoproj.io
appprojects.argoproj.io
imageupdaters.argocd-image-updater.argoproj.io
"

# --- предохранитель: списки не должны пересекаться ---
for f in $FOREIGN_CRDS; do
  for r in $ROLLOUTS_CRDS; do
    [ "$f" = "$r" ] && { echo "STOP: $f — чужой CRD, он попал в список на удаление"; exit 1; }
  done
done

# --- шаг 1: свой namespace целиком ---
kubectl delete namespace cookbook-argocd --ignore-not-found --wait=true
echo "1/2 namespace cookbook-argocd удалён целиком (git-сервер, ArgoCD стенда, демо-приложение)"

# --- шаг 2: кластерные CRD Argo Rollouts ---
# shellcheck disable=SC2086
kubectl delete crd $ROLLOUTS_CRDS --ignore-not-found
echo "2/2 CRD Argo Rollouts удалены: $(echo $ROLLOUTS_CRDS | tr '\n' ' ')"

# --- шаги 3-6: что осталось нетронутым ---
echo "не затронуты: namespace argocd (боевой ArgoCD), чужие CRD ($(echo $FOREIGN_CRDS | tr '\n' ' ')),"
echo "              узлы, control plane, kube-system, стенды cookbook-k8s и cookbook-net"
