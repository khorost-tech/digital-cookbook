#!/usr/bin/env bash
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx"; exit 1; }

kubectl -n cookbook-k8s delete issuer,clusterissuer,certificate,certificaterequest,secret -l stand=cookbook-operators --ignore-not-found

# H1: свой оператор greeter-operator (CRD Greeting) — удаляем Greeting раньше
# Deployment/RBAC, иначе оператор может успеть заново создать ConfigMap на
# резюнке до собственной остановки; порядок здесь не критичен для
# корректности (все команды — --ignore-not-found), но снижает шум в логах.
#
# --timeout=60s: kubectl delete по умолчанию ждёт фактического исчезновения
# объекта, а не только приёма запроса. Если на Greeting остался finalizer
# (см. README, демо H2 "риски": finalizer превращает delete в отложенное, а
# не отменённое удаление) — без --timeout эта команда зависла бы навсегда,
# ожидая снятия finalizer, которого никто не снимет. С --timeout teardown.sh
# останавливается с понятной причиной вместо молчаливого зависания.
if ! kubectl -n cookbook-k8s delete greeting -l stand=cookbook-operators --ignore-not-found --timeout=60s; then
  echo "ВНИМАНИЕ: удаление greeting не завершилось за 60с — похоже, у объекта остался finalizer." >&2
  echo "Диагностика: kubectl -n cookbook-k8s get greeting -l stand=cookbook-operators -o jsonpath='{.items[*].metadata.finalizers}'" >&2
  echo "Снятие (крайняя мера, см. README, раздел \"Демо\" -> finalizer): kubectl -n cookbook-k8s patch greeting <name> --type merge -p '{\"metadata\":{\"finalizers\":[]}}'" >&2
  exit 1
fi

# Deployment/SA/Role/RoleBinding оператора + оба ConfigMap стенда
# (greeter-operator-src — исходник оператора, greeting-demo — порождённый
# reconcile'ом). У обоих ConfigMap нет ownerReferences (см. README, раздел
# 10 и 20 про cert-manager Secret и свой ConfigMap) — сборка мусора
# Kubernetes их не заденет, поэтому удаляем явно, тем же лейблом.
kubectl -n cookbook-k8s delete configmap,deployment,serviceaccount,role,rolebinding -l stand=cookbook-operators --ignore-not-found

# CRD greetings.cookbook.khorost.tech — расширяет API кластера, а не
# namespace cookbook-k8s, поэтому у неё нет лейбл-области видимости
# namespace; удаляем последней, когда все Greeting в cookbook-k8s уже сняты
# (см. проверку выше) — так apiserver не пытается сносить CR сам, минуя нашу
# диагностику по finalizer.
kubectl delete crd greetings.cookbook.khorost.tech --ignore-not-found

echo "объекты стенда operators (label stand=cookbook-operators) в namespace cookbook-k8s удалены, CRD greetings.cookbook.khorost.tech снята"

# namespace cookbook-k8s НЕ удаляется: общий для всех стендов серии (resources, stateful,
# secrets, operators и т.д.).
# cert-manager (его Deployment, CRD, ClusterRole) и другие операторы кластера не трогаем —
# они вне жизненного цикла этого стенда, общие для кластера.
# Чужие объекты без label stand=cookbook-operators (боевой Certificate
# wildcard-volga-khorost-tech в ns argocd, selectel-webhook-* в ns cert-manager и другое) не
# затрагиваются: список ресурсов ограничен namespace cookbook-k8s (кроме финального delete crd,
# у которого другая, кластерная область видимости, но свой конкретный ресурс по имени), а
# delete внутри namespace — ещё и label-селектором.
