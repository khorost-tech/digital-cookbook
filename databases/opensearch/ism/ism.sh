#!/usr/bin/env bash
# Демонстрация жизненного цикла лог-индекса под управлением ISM:
# rollover под write-алиасом -> hot -> warm (allocation) -> snapshot (MinIO) -> delete.
# Стенд: docker compose up -d  (2 ноды hot/warm + MinIO, см. docker-compose.yml)
#
# Запуск:  ./ism.sh            — весь сценарий (займёт ~4-5 минут: ускоренные пороги 1m/2m/3m)
#          ./ism.sh setup      — только репозиторий + политика + bootstrap-индекс
#          ./ism.sh explain     — текущее состояние индексов
set -euo pipefail

HOT="${HOT:-http://localhost:9218}"
WARM="${WARM:-http://localhost:9219}"
REPO="${REPO:-ism-snapshots}"
J=(-H 'Content-Type: application/json')

req() { curl -s "$@"; echo; }

# --- 0. S3-репозиторий (креды запечены в образ; region/endpoint — в compose) ---
repo() {
  req -X PUT "$HOT/_snapshot/$REPO" "${J[@]}" \
    -d '{"type":"s3","settings":{"bucket":"snapshots","client":"default","base_path":"ism"}}'
  req -X POST "$HOT/_snapshot/$REPO/_verify"
}

# --- 1. ISM-политика + bootstrap write-алиаса ---
setup() {
  repo
  # index-шаблон: rollover_alias для ВСЕХ app-logs-* (иначе rolled-over индексы застревают на "Missing rollover_alias")
  curl -s -X PUT "$HOT/_index_template/app-logs-template" "${J[@]}" -d '{
    "index_patterns": ["app-logs-*"],
    "template": { "settings": { "plugins.index_state_management.rollover_alias": "app-logs" } }
  }'; echo
  curl -s -X PUT "$HOT/_plugins/_ism/policies/app-logs-policy" "${J[@]}" --data-binary @policy.json; echo
  # bootstrap: первый индекс с write-алиасом app-logs и rollover_alias
  curl -s -X PUT "$HOT/app-logs-000001" "${J[@]}" -d '{
    "settings": {
      "index.number_of_shards": 1,
      "index.number_of_replicas": 0,
      "index.routing.allocation.require.box_type": "hot",
      "plugins.index_state_management.rollover_alias": "app-logs"
    },
    "aliases": { "app-logs": { "is_write_index": true } }
  }'; echo
}

# --- 2. записать документы (превысить порог rollover min_doc_count=5) ---
load() {
  for i in $(seq 1 12); do
    curl -s -X POST "$HOT/app-logs/_doc" "${J[@]}" \
      -d "{\"@timestamp\":\"2026-07-24T10:0$((i%10)):00\",\"level\":\"INFO\",\"msg\":\"event $i\"}" >/dev/null
  done
  curl -s -X POST "$HOT/app-logs/_refresh" >/dev/null; echo "loaded 12 docs via write-alias"
}

# --- наблюдение ---
indices() { req "$HOT/_cat/indices/app-logs-*?v&s=index"; }
aliases() { req "$HOT/_cat/aliases/app-logs?v"; }
shards()  { req "$HOT/_cat/shards/app-logs-*?v&h=index,shard,prirep,state,node"; }
explain() { req "$HOT/_plugins/_ism/explain/app-logs-*?pretty"; }
snaps()   { req "$HOT/_snapshot/$REPO/_all?pretty"; }

# --- полный сценарий с ожиданием переходов ---
run() {
  setup; load
  echo "== ждём rollover (порог 5 доков, job_interval 1m) =="; sleep 90; indices; aliases; explain
  echo "== ждём hot->warm (min_index_age 1m) =="; sleep 60; shards; explain
  echo "== ждём warm->snapshot (2m) =="; sleep 90; snaps; explain
  echo "== ждём snapshot->delete (3m) =="; sleep 90; indices; explain
}

if [[ $# -eq 0 ]]; then run; else "$@"; fi
