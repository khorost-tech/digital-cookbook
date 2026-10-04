#!/usr/bin/env bash
# capture.sh <fixture-name> -- <kubectl args…> — снять живой вывод в fixtures/<name>.txt
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG to k8s-volga kubeconfig}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx (ожидался admin@k8s-volga)"; exit 1; }
name="$1"; shift
[ "$1" = "--" ] && shift
dir="$(dirname "$0")/fixtures"; mkdir -p "$dir"
out="$dir/$name.txt"
{
  echo "# captured on k8s-volga (k8s 1.36.2), fixture: $name"
  echo "# cmd: kubectl $*"
  echo "# ---"
  kubectl "$@"
} | tee "$out"
echo "saved: fixtures/$name.txt"
