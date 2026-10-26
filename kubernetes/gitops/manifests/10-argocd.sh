#!/usr/bin/env bash
# 10-argocd.sh — установка СВОЕГО, namespace-scoped ArgoCD в ns cookbook-argocd.
#
# Почему не `kubectl apply -f .../install.yaml`, как во всех руководствах:
# кластер k8s-volga — ОБЩИЙ, и в нём уже живёт боевой ArgoCD в ns argocd. CRD группы
# argoproj.io (applications, applicationsets, appprojects) — объекты КЛАСТЕРНОГО уровня,
# они одни на весь кластер и принадлежат Helm-релизу боевого ArgoCD
# (labels app.kubernetes.io/managed-by=Helm, annotations meta.helm.sh/release-name=argocd).
# Штатный install.yaml несёт эти CRD внутри себя — применить его значит перехватить
# чужие объекты у Helm и, возможно, подменить схему под боевыми Application.
#
# Отсюда три решения, каждое со своей причиной:
#   1) берём namespace-install.yaml, а не install.yaml;
#   2) версия пиновата в v3.4.1 — ровно та, что у боевого (quay.io/argoproj/argocd:v3.4.1),
#      чтобы наш контроллер читал CRD той же схемы, под которую он собран;
#   3) манифест ВСЁ РАВНО прогоняется через фильтр kind: CustomResourceDefinition —
#      см. блок «фильтр» ниже. На v3.4.1 фильтр вырезает ноль документов, и это не повод
#      его убрать: он превращает «я посмотрел файл глазами» в проверяемое утверждение и
#      сломает установку громко, если в другой версии CRD в этот манифест вернутся.
#
# Идемпотентность: kubectl apply. Повторный запуск не создаёт ничего нового.
# Переопределения: ARGOCD_VERSION, ARGOCD_NS, ARGOCD_MANIFEST (путь к локальной копии).
set -euo pipefail

VERSION="${ARGOCD_VERSION:-v3.4.1}"
NS="${ARGOCD_NS:-cookbook-argocd}"
URL="https://raw.githubusercontent.com/argoproj/argo-cd/${VERSION}/manifests/namespace-install.yaml"
PROXY="${STAND_PROXY:-s9.khorost.tech:3128}"

: "${KUBECONFIG:?set KUBECONFIG to k8s-volga kubeconfig}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx (ожидался admin@k8s-volga)"; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
raw="$work/raw.yaml"

echo "== 1. манифест ArgoCD $VERSION =="
if [ -n "${ARGOCD_MANIFEST:-}" ]; then
  cp "$ARGOCD_MANIFEST" "$raw"
  echo "источник: локальная копия $ARGOCD_MANIFEST"
else
  if ! curl -fsSL --max-time 60 -o "$raw" "$URL"; then
    echo "прямая загрузка не прошла, пробуем через прокси $PROXY"
    curl -fsSL --max-time 60 --proxy "http://$PROXY" -o "$raw" "$URL"
  fi
  echo "источник: $URL"
fi
echo "размер: $(wc -c < "$raw") байт, sha256: $(sha256sum < "$raw" | cut -d' ' -f1)"

echo
echo "== 2. фильтр: вырезаем ВСЕ документы kind: CustomResourceDefinition =="
# Разбиение по строкам '---' в нулевой колонке. Это генерённый kustomize файл, внутри
# документов таких строк нет — проверяется тем, что сумма kind'ов совпадает с числом
# непустых документов (см. вывод ниже).
docs="$work/docs"; mkdir -p "$docs"
awk -v d="$docs" '
  BEGIN { n = 0; f = sprintf("%s/doc-%04d.yaml", d, n) }
  /^---[[:space:]]*$/ { n++; f = sprintf("%s/doc-%04d.yaml", d, n); next }
  { print > f }
' "$raw"

filtered="$work/filtered.yaml"; : > "$filtered"
total=0; kept=0; dropped=0; dropped_names=""
for f in "$docs"/doc-*.yaml; do
  # пустой документ (только комментарии/пробелы) — не считаем
  grep -qE '^[a-zA-Z]' "$f" || continue
  total=$((total + 1))
  kind="$(awk '/^kind:[[:space:]]/{print $2; exit}' "$f")"
  name="$(awk '/^  name:[[:space:]]/{print $2; exit}' "$f")"
  if [ "$kind" = "CustomResourceDefinition" ]; then
    dropped=$((dropped + 1)); dropped_names="$dropped_names $name"
    continue
  fi
  { echo "---"; cat "$f"; } >> "$filtered"
  kept=$((kept + 1))
done

echo "документов в исходном манифесте: $total"
echo "вырезано kind: CustomResourceDefinition: $dropped ($([ -n "$dropped_names" ] && echo "$dropped_names" || echo "имён нет"))"
echo "осталось к применению: $kept"
echo "состав того, что применяем:"
grep -E '^kind: ' "$filtered" | sort | uniq -c

echo
echo "== 2a. подмена реестра для redis =="
# Единственная правка содержимого манифеста, и она вынужденная: namespace-install тянет
# redis из public.ecr.aws, а из этого кластера реестр недоступен. Живьём:
#   Failed to pull image "public.ecr.aws/docker/library/redis:8.2.3-alpine": rpc error:
#   code = Canceled desc = failed to pull and unpack image ...: context canceled
#   → ImagePullBackOff
# Меняется ТОЛЬКО реестр, версия остаётся ровно та, что в манифесте v3.4.1 (8.2.3-alpine):
# подменять заодно и версию значило бы уехать от протестированной апстримом связки.
# docker.io в этом кластере рабочий — оттуда же тянет redis боевой ArgoCD
# (imageID docker.io/library/redis@sha256:d146f83b…).
REDIS_FROM="public.ecr.aws/docker/library/redis:8.2.3-alpine"
REDIS_TO="${ARGOCD_REDIS_IMAGE:-docker.io/library/redis:8.2.3-alpine}"
before="$(grep -c "$REDIS_FROM" "$filtered" || true)"
sed -i "s#$REDIS_FROM#$REDIS_TO#g" "$filtered"
echo "вхождений $REDIS_FROM: $before -> заменено на $REDIS_TO"
echo "образы, которые применяем:"
grep -E '^ +(- )?image: ' "$filtered" | sed 's/^ *//' | sort -u

echo
# Доказательства, а не обещания: два жёстких утверждения на отфильтрованном файле.
crd_left="$(grep -c '^kind: CustomResourceDefinition' "$filtered" || true)"
[ "$crd_left" -eq 0 ] || { echo "СТОП: в применяемом манифесте осталось CRD: $crd_left"; exit 1; }
echo "ПРОВЕРКА: CRD в применяемом манифесте = $crd_left"

# namespace-scoped — не на словах: кластерных ролей в применяемом наборе быть не должно.
cluster_left="$(grep -cE '^kind: (ClusterRole|ClusterRoleBinding)' "$filtered" || true)"
[ "$cluster_left" -eq 0 ] || { echo "СТОП: в манифесте есть ClusterRole/ClusterRoleBinding: $cluster_left"; exit 1; }
echo "ПРОВЕРКА: ClusterRole/ClusterRoleBinding в применяемом манифесте = $cluster_left"

echo
echo "== 3. применяем в ns $NS =="
kubectl apply -n "$NS" -f "$filtered"

echo
echo "== 4. контроль границ сразу после применения =="
echo "-- кластерные роли с argocd в имени (ожидаются ТОЛЬКО боевые, из ns argocd):"
kubectl get clusterrole,clusterrolebinding -o custom-columns='KIND:.kind,NAME:.metadata.name,CREATED:.metadata.creationTimestamp' \
  | grep -iE 'KIND|argocd' || echo "(нет ни одной)"
echo
echo "-- CRD argoproj.io: creationTimestamp обязан остаться прежним (2026-07-19):"
kubectl get crd -o custom-columns='NAME:.metadata.name,CREATED:.metadata.creationTimestamp,MANAGED-BY:.metadata.labels.app\.kubernetes\.io/managed-by' \
  | grep -E 'NAME|argoproj'
echo
echo "готово: ArgoCD $VERSION применён в $NS без CRD и без кластерных ролей"
