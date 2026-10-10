#!/usr/bin/env bash
# teardown.sh — снести объекты стенда autoscaling в ns cookbook-k8s.
# Namespace НЕ удаляется (общий со стендом resources), операторы (VPA,
# Prometheus Adapter, KEDA) этот скрипт не трогает — их жизненный цикл отдельный.
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx"; exit 1; }

kubectl -n cookbook-k8s delete deployment,service,configmap -l app=metricgen --ignore-not-found
kubectl -n cookbook-k8s delete hpa -l app=metricgen --ignore-not-found
kubectl -n cookbook-k8s delete scaledobject -l app=metricgen --ignore-not-found 2>/dev/null || true

# B2: собственный Prometheus стенда + consumer (metricgen выше уже покрыт app=metricgen)
kubectl -n cookbook-k8s delete deployment,service,configmap,serviceaccount,role,rolebinding -l app=prometheus --ignore-not-found
kubectl -n cookbook-k8s delete deployment -l app=consumer --ignore-not-found
kubectl -n cookbook-k8s delete hpa -l app=consumer --ignore-not-found
kubectl -n cookbook-k8s delete scaledobject -l app=consumer --ignore-not-found 2>/dev/null || true

# B1: демо HPA по CPU — php-apache/load-gen (manifests/10-hpa-cpu.yaml,
# 11-load-gen.yaml) без меток app=*, снимаются по имени, если демо не
# убрано вручную сразу после снятия ряда (см. README, «Порядок запуска»)
kubectl -n cookbook-k8s delete deployment,service,hpa php-apache --ignore-not-found
kubectl -n cookbook-k8s delete pod load-gen --ignore-not-found

echo "объекты стенда autoscaling (app=metricgen/prometheus/consumer, php-apache, load-gen, hpa, scaledobject) удалены из cookbook-k8s"
echo "namespace cookbook-k8s НЕ удаляется — общий со стендом resources"
echo "Prometheus Adapter (ns custom-metrics, helm-релиз prometheus-adapter) этот скрипт НЕ трогает —"
echo "снимается отдельно: helm uninstall prometheus-adapter -n custom-metrics && kubectl delete namespace custom-metrics"
