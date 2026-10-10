#!/usr/bin/env bash
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx"; exit 1; }

# ВНИМАНИЕ: этот teardown удаляет НАМЕСПЕЙС cookbook-k8s ЦЕЛИКОМ. Он общий для
# нескольких стендов серии — в нём живут объекты autoscaling, stateful, secrets,
# operators и architecture. Удаление ns снесёт ИХ объекты тоже, а не только
# ресурсы стенда resources. Запускай этот скрипт ТОЛЬКО если снимаешь всю серию;
# для точечной очистки одного стенда удаляй его объекты по имени/лейблу, не ns.
echo "ВНИМАНИЕ: удаляю ns cookbook-k8s ЦЕЛИКОМ — вместе с объектами соседних стендов"
echo "         (autoscaling/stateful/secrets/operators/architecture живут в этом же ns)."
echo "         Запускай только при снятии всей серии, а не одного стенда."

# Реальный guard: удаление общего namespace требует явного подтверждения через
# переменную окружения (работает неинтерактивно, в т.ч. в CI). Без подтверждения —
# ничего не удаляем и выходим с ненулевым кодом.
: "${CONFIRM_DELETE_NAMESPACE:=}"
if [ "$CONFIRM_DELETE_NAMESPACE" != "cookbook-k8s" ]; then
  echo "ОТКАЗ: teardown удалит общий namespace cookbook-k8s ЦЕЛИКОМ вместе с объектами соседних стендов серии" >&2
  echo "       (autoscaling/stateful/secrets/operators/architecture живут в этом же ns)." >&2
  echo "Если вы точно сносите всю серию, повторите с подтверждением:" >&2
  echo "  CONFIRM_DELETE_NAMESPACE=cookbook-k8s bash kubernetes/resources/teardown.sh" >&2
  exit 1
fi

kubectl delete namespace cookbook-k8s --ignore-not-found
kubectl delete namespace cookbook-k8s-noqos --ignore-not-found
echo "namespace cookbook-k8s и cookbook-k8s-noqos удалены"
# ns vpa НЕ трогаем: там живёт VPA-оператор (vpa-recommender/vpa-updater), общий для
# всех демо кластера k8s-volga, а не часть стенда resources — его жизненный цикл отдельный.
echo "namespace vpa НЕ удаляется — VPA-оператор общий, вне жизненного цикла этого стенда"
