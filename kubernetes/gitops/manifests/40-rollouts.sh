#!/usr/bin/env bash
# 40-rollouts.sh — установка Argo Rollouts (progressive delivery) в ns cookbook-argocd.
#
# ЭТО ЕДИНСТВЕННАЯ кластерная установка стенда: Argo Rollouts определяет СВОИ CRD
# (rollouts, analysisruns, analysistemplates, clusteranalysistemplates, experiments —
# группа argoproj.io, но ДРУГИЕ имена, чем у боевого ArgoCD). CRD в Kubernetes — объект
# кластерный, потому их не спрятать в namespace. Всё остальное (контроллер, RBAC) — строго
# namespace-scoped.
#
# Границы (проверяются ниже по коду, не на словах):
#   1) ПЕРЕД установкой убеждаемся, что CRD Rollouts в кластере НЕТ. Если есть — СТОП:
#      кто-то поставил их параллельно, и перезапись схемы под чужими объектами недопустима.
#   2) Ставим РОВНО пять CRD Rollouts. Имена сверяются с белым списком и со списком чужих
#      CRD боевого ArgoCD (applications/applicationsets/appprojects/imageupdaters): пересечение
#      — СТОП. Чужие CRD не трогаются вовсе.
#   3) Контроллер — из namespace-install.yaml (флаг --namespaced, Role+RoleBinding в своём
#      namespace). Три aggregate-ClusterRole из этого манифеста ВЫРЕЗАЮТСЯ: они лишь
#      подмешивают глаголы rollout в встроенные admin/edit/view и стенду не нужны — так у
#      стенда НОЛЬ собственных кластерных ролей, как и в M1. После вырезания в применяемом к
#      контроллеру наборе не должно остаться ни одной ClusterRole/ClusterRoleBinding.
#
# Версия пинуется в v1.9.1 (последний стабильный релиз на момент съёмки). Берутся РЕЛИЗНЫЕ
# ассеты, а не сырые manifests/ из тега: сырой namespace-install.yaml тянет образ :latest и
# не содержит CRD, релизный — пинует образ v1.9.1.
#
# Переопределения: ROLLOUTS_VERSION, ROLLOUTS_NS, STAND_PROXY.
set -euo pipefail

VERSION="${ROLLOUTS_VERSION:-v1.9.1}"
NS="${ROLLOUTS_NS:-cookbook-argocd}"
BASE="https://github.com/argoproj/argo-rollouts/releases/download/${VERSION}"
PROXY="${STAND_PROXY:-s9.khorost.tech:3128}"

: "${KUBECONFIG:?set KUBECONFIG to k8s-volga kubeconfig}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx (ожидался admin@k8s-volga)"; exit 1; }

# Белый список CRD Rollouts и чёрный список чужих CRD боевого ArgoCD.
ALLOWED_CRDS="rollouts.argoproj.io analysisruns.argoproj.io analysistemplates.argoproj.io clusteranalysistemplates.argoproj.io experiments.argoproj.io"
FOREIGN_CRDS="applications.argoproj.io applicationsets.argoproj.io appprojects.argoproj.io imageupdaters.argocd-image-updater.argoproj.io"

echo "== 0. предполётная проверка: CRD Rollouts в кластере быть НЕ должно =="
present="$(kubectl get crd -o name 2>/dev/null | sed 's#customresourcedefinition.apiextensions.k8s.io/##' | grep -E '^(rollouts|analysisruns|analysistemplates|clusteranalysistemplates|experiments)\.argoproj\.io$' || true)"
if [ -n "$present" ]; then
  echo "СТОП: CRD Rollouts УЖЕ есть в кластере — кто-то поставил их параллельно:"
  echo "$present"
  echo "установка прервана, чтобы не переписать чужую схему (NEEDS_CONTEXT)"
  exit 1
fi
echo "CRD Rollouts не найдено — можно ставить. Для контраста, чужие CRD argoproj.io на месте:"
kubectl get crd -o custom-columns='NAME:.metadata.name,CREATED:.metadata.creationTimestamp' | grep -E 'NAME|argoproj' || true

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT

dl() { # dl <file> <dest>
  local url="$BASE/$1" dest="$2"
  if ! curl -fsSL --max-time 90 -o "$dest" "$url"; then
    echo "прямая загрузка $1 не прошла, пробуем через прокси $PROXY" >&2
    curl -fsSL --max-time 90 --proxy "http://$PROXY" -o "$dest" "$url"
  fi
}

echo
echo "== 1. загрузка релизных ассетов $VERSION =="
dl install.yaml "$work/install.yaml"
dl namespace-install.yaml "$work/nsinstall.yaml"
echo "install.yaml:           $(wc -c < "$work/install.yaml") байт, sha256 $(sha256sum < "$work/install.yaml" | cut -d' ' -f1)"
echo "namespace-install.yaml: $(wc -c < "$work/nsinstall.yaml") байт, sha256 $(sha256sum < "$work/nsinstall.yaml" | cut -d' ' -f1)"

# --- разбивка на документы ---
split_docs() { # split_docs <file> <dir>
  local src="$1" d="$2"; mkdir -p "$d"
  awk -v d="$d" 'BEGIN{n=0;f=sprintf("%s/doc-%04d.yaml",d,n)}
    /^---[[:space:]]*$/ {n++; f=sprintf("%s/doc-%04d.yaml",d,n); next}
    {print > f}' "$src"
}

echo
echo "== 2. CRD: берём ТОЛЬКО kind: CustomResourceDefinition из install.yaml =="
split_docs "$work/install.yaml" "$work/idocs"
crds="$work/crds.yaml"; : > "$crds"; crd_names=""
for f in "$work"/idocs/doc-*.yaml; do
  grep -qE '^[a-zA-Z]' "$f" || continue
  kind="$(awk '/^kind:[[:space:]]/{print $2; exit}' "$f")"
  [ "$kind" = "CustomResourceDefinition" ] || continue
  name="$(awk '/^  name:[[:space:]]/{print $2; exit}' "$f")"
  crd_names="$crd_names $name"
  { echo "---"; cat "$f"; } >> "$crds"
done
echo "CRD в install.yaml:$crd_names"

# сверка с белым и чёрным списками
for n in $crd_names; do
  echo "$ALLOWED_CRDS" | tr ' ' '\n' | grep -qx "$n" || { echo "СТОП: CRD $n не в белом списке Rollouts"; exit 1; }
  echo "$FOREIGN_CRDS" | tr ' ' '\n' | grep -qx "$n" && { echo "СТОП: CRD $n — чужой (боевой ArgoCD)"; exit 1; }
done
cnt=$(echo $crd_names | wc -w)
[ "$cnt" -eq 5 ] || { echo "СТОП: ожидалось 5 CRD Rollouts, получено $cnt"; exit 1; }
echo "ПРОВЕРКА: 5 CRD Rollouts, все в белом списке, пересечения с чужими нет"

echo
echo "== 3. контроллер: namespace-install БЕЗ трёх aggregate-ClusterRole =="
split_docs "$work/nsinstall.yaml" "$work/ndocs"
ctrl="$work/controller.yaml"; : > "$ctrl"; dropped=""
for f in "$work"/ndocs/doc-*.yaml; do
  grep -qE '^[a-zA-Z]' "$f" || continue
  kind="$(awk '/^kind:[[:space:]]/{print $2; exit}' "$f")"
  name="$(awk '/^  name:[[:space:]]/{print $2; exit}' "$f")"
  if [ "$kind" = "ClusterRole" ] || [ "$kind" = "ClusterRoleBinding" ]; then
    dropped="$dropped $name"; continue
  fi
  { echo "---"; cat "$f"; } >> "$ctrl"
done
echo "вырезано ClusterRole/ClusterRoleBinding:$dropped"
left="$(grep -cE '^kind: (ClusterRole|ClusterRoleBinding)' "$ctrl" || true)"
[ "$left" -eq 0 ] || { echo "СТОП: в наборе контроллера осталась кластерная роль: $left"; exit 1; }
echo "ПРОВЕРКА: ClusterRole/ClusterRoleBinding в наборе контроллера = 0"
echo "состав набора контроллера:"; grep -E '^kind: ' "$ctrl" | sort | uniq -c

echo
echo "== 4. применяем: сперва CRD (кластер), затем контроллер (ns $NS) =="
kubectl apply -f "$crds"
kubectl apply -n "$NS" -f "$ctrl"

echo
echo "== 5. контроль границ сразу после установки =="
echo "-- CRD argoproj.io: пять наших НОВЫЕ, четыре чужих со СТАРЫМ creationTimestamp 2026-07-19:"
kubectl get crd -o custom-columns='NAME:.metadata.name,CREATED:.metadata.creationTimestamp' | grep -E 'NAME|argoproj'
echo
echo "-- кластерные роли с 'rollout' в имени (ожидается: НИ ОДНОЙ — мы их вырезали):"
kubectl get clusterrole,clusterrolebinding -o name | grep -i rollout || echo "(нет ни одной — как и задумано)"
echo
echo "-- боевые Application (ожидается: только наш demo, чужих ноль):"
kubectl get applications.argoproj.io -A
echo
echo "готово: Argo Rollouts $VERSION — CRD в кластере, контроллер namespace-scoped в $NS"
