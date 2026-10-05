#!/usr/bin/env bash
# teardown.sh — уборка стенда scheduling.
#
# Стенд оставляет следы в ТРЁХ местах, а не в одном (как предыдущие стенды серии),
# поэтому шаги перечислены явно:
#   1) объекты стенда в namespace cookbook-k8s по метке stand=cookbook-scheduling;
#   2) метка cookbook.khorost.tech/scheduling с УЗЛОВ (объект узла — кластерный,
#      меткой демо помечается один worker; снимаем со всех узлов, где найдётся);
#   3) объекты PriorityClass cookbook-low и cookbook-high — они тоже кластерные
#      и в namespace не лежат, обычным `delete -n` их не убрать.
# Чего скрипт НЕ делает:
#   4) namespace cookbook-k8s не удаляет (общий для всех стендов серии), в kube-system
#      не заходит, чужих объектов и системных меток/тейнтов узлов не трогает.
#   5) идемпотентен: повторный запуск на уже убранном стенде проходит без ошибок
#      (--ignore-not-found, снятие несуществующей метки не считается ошибкой).
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx (ожидался admin@k8s-volga)"; exit 1; }

# --- шаг 1: объекты стенда в namespace ---
kubectl -n cookbook-k8s delete all,cm,secret,sa,role,rolebinding,pdb \
  -l stand=cookbook-scheduling --ignore-not-found
echo "1/3 объекты стенда scheduling (label stand=cookbook-scheduling) в namespace cookbook-k8s удалены"

# --- шаг 2: метка демо с узлов ---
# Узлы ищем по самой метке: имя узла нигде не захардкожено, а если метку успели
# поставить на несколько узлов — снимется со всех.
labeled="$(kubectl get nodes -l cookbook.khorost.tech/scheduling -o name 2>/dev/null || true)"
if [ -n "$labeled" ]; then
  # shellcheck disable=SC2086
  kubectl label $labeled cookbook.khorost.tech/scheduling-
  echo "2/3 метка cookbook.khorost.tech/scheduling снята с узлов: $(echo "$labeled" | tr '\n' ' ')"
else
  echo "2/3 метка cookbook.khorost.tech/scheduling ни на одном узле не найдена — снимать нечего"
fi

# --- шаг 3: кластерные PriorityClass ---
kubectl delete priorityclass cookbook-low cookbook-high --ignore-not-found
echo "3/3 PriorityClass cookbook-low и cookbook-high удалены"

# --- шаги 4-5: что осталось нетронутым ---
# namespace cookbook-k8s НЕ удаляется: общий для стендов серии (resources, autoscaling,
# stateful, secrets, networking, operators, architecture). Системные метки и тейнты узлов,
# kube-system и control plane скрипт не трогает вовсе: стенд их только читает.
echo "namespace cookbook-k8s, системные метки/тейнты узлов и kube-system не затронуты"
