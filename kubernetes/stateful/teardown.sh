#!/usr/bin/env bash
# teardown.sh — снести объекты стенда stateful в ns cookbook-k8s.
# Namespace НЕ удаляется (общий со стендами resources/autoscaling), операторы и
# чужие объекты этот скрипт не трогает — их жизненный цикл отдельный.
#
# Стенд — это не только основной StatefulSet demo (задача D0, остаётся развёрнутым
# 3/3 до самого teardown), но и временные объекты отдельных демо серии, которые
# полагается прибирать вручную сразу после снятия соответствующей фикстуры:
# StatefulSet/Service demo-rp (задача D2), StatefulSet/Service pg (задача D4),
# поды rwx-writer/rwx-reader и PVC shared (задача D3), временные под(ы)/PVC
# nfs-ownership-check проверки uid-сквоша (задача D5). Если ручная уборка после
# какой-то из задач не была доведена до конца, этот скрипт подчищает и их —
# все команды на --ignore-not-found, повторный запуск безопасен (идемпотентно).
#
# StorageClass nfs-csi — reclaimPolicy: Retain: после удаления PVC том (PV) остаётся
# в Released, данные на NFS не стираются. Поэтому скрипт явно подчищает PV, которые
# остались от PVC этого стенда, — иначе они копятся в кластере навсегда.
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx"; exit 1; }

kubectl -n cookbook-k8s delete statefulset demo --ignore-not-found
kubectl -n cookbook-k8s delete service demo --ignore-not-found
kubectl -n cookbook-k8s delete pvc -l app=demo --ignore-not-found

# Временные StatefulSet/Service отдельных задач серии — на случай, если ручная
# уборка после соответствующей демонстрации не была доведена до конца.
kubectl -n cookbook-k8s delete statefulset demo-rp pg --ignore-not-found
kubectl -n cookbook-k8s delete service demo-rp pg --ignore-not-found

# StatefulSet pg (задача D4) не имеет persistentVolumeClaimRetentionPolicy (в отличие от
# demo-rp, где whenDeleted: Delete задан явно и каскадно сносит PVC вместе со StatefulSet) —
# его PVC data-pg-0 переживает удаление StatefulSet и остаётся Bound без владельца.
# Подчищаем явно, до секции освободившихся PV ниже (иначе её awk-фильтр на Released его
# не поймает — PVC будет всё ещё Bound).
kubectl -n cookbook-k8s delete pvc data-pg-0 --ignore-not-found

# RWX-демо (задача D3): временные поды-не-StatefulSet и общий PVC.
kubectl -n cookbook-k8s delete pod rwx-writer rwx-reader --ignore-not-found
kubectl -n cookbook-k8s delete pvc shared --ignore-not-found

# Проверка uid-сквоша NFS (задача D5): временный под(ы) и PVC.
kubectl -n cookbook-k8s delete pod nfs-ownership-check nfs-ownership-pg-fail --ignore-not-found
kubectl -n cookbook-k8s delete pvc nfs-ownership-check --ignore-not-found

# PV с Retain не удаляются автоматически вместе с PVC — подчищаем те, что
# claimRef'ились на PVC этого стенда (data-demo-*, data-pg-*, shared,
# nfs-ownership-check) в cookbook-k8s. Другие PVC этого namespace (стендов
# resources/autoscaling) под этот шаблон не подпадают — не трогаем.
released_pvs="$(kubectl get pv -o jsonpath='{range .items[?(@.spec.claimRef.namespace=="cookbook-k8s")]}{.metadata.name}{" "}{.spec.claimRef.name}{"\n"}{end}' \
  | awk '$2 ~ /^data-demo-|^data-pg-|^shared$|^nfs-ownership-check$/ {print $1}')"
if [ -n "$released_pvs" ]; then
  echo "$released_pvs" | xargs -r kubectl delete pv --ignore-not-found
else
  echo "нет освободившихся PV стенда stateful для удаления"
fi

echo "объекты стенда stateful (statefulset demo/demo-rp/pg, их service, pvc data-demo-*/data-pg-*/shared/nfs-ownership-check, поды rwx-writer/rwx-reader/nfs-ownership-*, их PV) удалены из cookbook-k8s"
echo "namespace cookbook-k8s НЕ удаляется — общий с другими стендами серии"
