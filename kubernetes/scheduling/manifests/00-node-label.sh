#!/usr/bin/env bash
# 00-node-label.sh set|unset — метка демо на ОДНОМ worker-узле.
#
# Зачем отдельный скрипт, а не YAML: узел — объект кластерный и общий, «применить» его
# манифестом нельзя, не перетерев чужие поля. Ставится ровно одна метка с префиксом стенда —
# cookbook.khorost.tech/scheduling=demo, и снимается в teardown.sh.
#
# Имя узла НЕ захардкожено: берётся первый узел БЕЗ тейнта
# node-role.kubernetes.io/control-plane и печатается на экран.
# Никаких других меток и никаких тейнтов скрипт не трогает.
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG to k8s-volga kubeconfig}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx (ожидался admin@k8s-volga)"; exit 1; }

label_key="cookbook.khorost.tech/scheduling"
label_val="demo"

mode="${1:-}"
case "$mode" in
  set|unset) ;;
  *) echo "usage: $0 set|unset"; exit 2 ;;
esac

if [ "$mode" = "unset" ]; then
  # Снимаем по самой метке, а не по имени узла: так unset идемпотентен и убирает метку
  # отовсюду, куда бы её ни поставили.
  labeled="$(kubectl get nodes -l "$label_key" -o name)"
  if [ -z "$labeled" ]; then
    echo "метки $label_key нет ни на одном узле — снимать нечего"
    exit 0
  fi
  # shellcheck disable=SC2086
  kubectl label $labeled "${label_key}-"
  echo "снято: $label_key с $(echo "$labeled" | tr '\n' ' ')"
  exit 0
fi

# set: первый узел БЕЗ тейнта control-plane.
# Строка на узел: "<имя><TAB><ключи тейнтов через пробел>"; строки с ключом control-plane
# отбрасываем. jsonpath отрицания не умеет (`!` — синтаксическая ошибка), jq в окружении
# может не быть — поэтому фильтруем средствами shell.
nodes="$(kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.taints[*].key}{"\n"}{end}')"
node="$(printf '%s\n' "$nodes" \
  | grep -v 'node-role\.kubernetes\.io/control-plane' \
  | grep -v '^[[:space:]]*$' \
  | head -n1 | cut -f1 || true)"

[ -n "$node" ] || { echo "worker-узел (без тейнта control-plane) не найден"; exit 1; }

echo "выбран worker-узел: $node"
current="$(kubectl get node "$node" -o jsonpath="{.metadata.labels['cookbook\.khorost\.tech/scheduling']}")"
if [ "$current" = "$label_val" ]; then
  echo "метка ${label_key}=${label_val} на $node уже стоит — ничего не меняем"
  exit 0
fi
kubectl label node "$node" "${label_key}=${label_val}" --overwrite
echo "поставлено: ${label_key}=${label_val} на $node"
