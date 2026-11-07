#!/usr/bin/env bash
#
# run-all.sh — полный прогон стенда vector-databases/ann. Запускать из каталога
# стенда. Нужен собранный датасет (стенд vector-databases/dataset). Первая
# упавшая ступень обрывает прогон, и маркер не ставится.
# FRESH=1 — пересобрать bench, образ Faiss и jar jvector с нуля (вторая сессия).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Свободное место до старта: тома баз, образы и промежуточные выдачи стенда
# занимают до 5 ГБ, при нехватке сессия падает посередине с «no space left on
# device». Меряем и диск Docker (на Mac — диск его VM), и диск хоста: файл
# диска VM растёт на хосте, и предел VM может быть больше свободного места.
# MIN_FREE_GB — переопределить порог.
need_gb=${MIN_FREE_GB:-5}
vm_gb=$(docker run --rm --quiet busybox:1.37.0 df -Pk / | awk 'NR == 2 {print int($4 / 1048576)}')
host_gb=$(df -Pk . | awk 'NR == 2 {print int($4 / 1048576)}')
free_gb=$(( vm_gb < host_gb ? vm_gb : host_gb ))
if [ "$free_gb" -lt "$need_gb" ]; then
  echo "run-all: свободно $free_gb ГБ (Docker $vm_gb, хост $host_gb), стенду нужно $need_gb." >&2
  echo "Освободить: docker system df; тома завершённых стендов — docker compose -p <проект> down -v." >&2
  exit 1
fi

if [ "${FRESH:-0}" = 1 ]; then
  rm -rf out java/target
  docker image rm -f vdb-ann-faiss >/dev/null 2>&1 || true
  docker volume rm -f vdb-gocache >/dev/null 2>&1 || true
fi
for axis in brute hnsw ivf quant metric crosscheck; do
  # раннер и разбор — отдельными командами: под set -e ошибка в левой части &&
  # прогон не обрывает, и разбор шёл бы по фикстуре прошлой сессии
  python3 scripts/run-$axis.py
  python3 scripts/analyze.py $axis >/dev/null
done
python3 scripts/run-manifest.py
python3 scripts/run-marker.py      # последним: проверки и диверсии внутри
