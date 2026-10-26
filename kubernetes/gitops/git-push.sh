#!/usr/bin/env bash
# git-push.sh — выполнить git-операцию в демо-репозитории ИЗ ПОДА ВНУТРИ cookbook-argocd.
#
# Зачем именно из пода, а не с ноутбука. К git-серверу стенда пускает NetworkPolicy
# git-server-allow-stand-only (05-git-server.yaml): 9418/TCP и только из namespace
# cookbook-argocd. Снаружи кластера туда хода нет вовсе, и ослаблять политику ради демо —
# ровно то, чего делать нельзя: она и есть один из двух замков, закрытых в M1.
#
# Под проходит pod-security baseline: без privileged, без hostPath, без host-network.
# Образ — тот же дайджест, что у git-сервера, второй образ в стенд не тянется.
#
# Даты коммита ПРИБИТЫ (как у seed-коммита в 05-git-server.yaml): содержимое, личность и
# сообщение и так фиксированы, недетерминизм давали только метки времени. С прибитыми
# датами хеш коммита воспроизводим, и на него можно ссылаться в фикстурах.
#
# Использование — тело скрипта на stdin, выполняется в корне склонированного репозитория:
#   bash git-push.sh <<'EOF'
#   git rm apps/demo/service.yaml
#   git commit -m "..."
#   git push origin main
#   EOF
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG to k8s-volga kubeconfig}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx (ожидался admin@k8s-volga)"; exit 1; }

NS=cookbook-argocd
POD=git-client
IMAGE='buildpack-deps:bookworm-scm@sha256:877e9e4d949edfbcbedabc3a2d7ab593955fee5d6d0777adf3a991eb30c750d8'
REPO='git://git-server.cookbook-argocd.svc.cluster.local/demo.git'

body="$(cat)"

kubectl -n "$NS" delete pod "$POD" --ignore-not-found --wait=true >/dev/null

kubectl -n "$NS" apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: $POD
  namespace: $NS
  labels:
    stand: cookbook-gitops
    role: git-client
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    runAsGroup: 1000
  containers:
    - name: git
      image: $IMAGE
      env:
        - name: HOME
          value: /tmp
        - name: GIT_AUTHOR_DATE
          value: "2026-07-24T13:00:00+00:00"
        - name: GIT_COMMITTER_DATE
          value: "2026-07-24T13:00:00+00:00"
      command: ["bash", "-euo", "pipefail", "-c"]
      args:
        - |
          git config --global user.email "stand@khorost.tech"
          git config --global user.name  "digital-cookbook stand"
          git config --global --add safe.directory '*'
          git clone --quiet "$REPO" /tmp/repo
          cd /tmp/repo
$(printf '%s\n' "$body" | sed 's/^/          /')
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
        seccompProfile:
          type: RuntimeDefault
      volumeMounts:
        - name: work
          mountPath: /tmp
      resources:
        requests:
          cpu: 20m
          memory: 32Mi
        limits:
          cpu: 200m
          memory: 256Mi
  volumes:
    - name: work
      emptyDir: {}
YAML

kubectl -n "$NS" wait --for=jsonpath='{.status.phase}'=Succeeded pod/"$POD" --timeout=120s >/dev/null 2>&1 || true
kubectl -n "$NS" logs "$POD"
phase="$(kubectl -n "$NS" get pod "$POD" -o jsonpath='{.status.phase}')"
kubectl -n "$NS" delete pod "$POD" --wait=true >/dev/null
echo "pod phase: $phase (под удалён)"
[ "$phase" = "Succeeded" ]
