#!/usr/bin/env bash
# Демонстрация жизненного цикла лог-индекса под управлением ISM:
# rollover под write-алиасом -> hot -> warm (allocation) -> snapshot (MinIO) -> delete.
# Стенд: docker compose up -d  (2 ноды hot/warm + MinIO, см. docker-compose.yml)
#
# Запуск:  ./ism.sh            — весь сценарий; каждый шаг ждётся по _ism/explain, а не по таймеру
#          (ускоренные пороги 1m/2m/3m, но фактическое время плавает: переходы идут на прогонах
#           ISM-джобы job_interval, обычно несколько минут суммарно)
#          bash ism.sh         — то же, если у файла не выставлен executable-бит
#          ./ism.sh setup      — только репозиторий + политика + bootstrap-индекс
#          ./ism.sh explain    — текущее состояние индексов
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
  # index-шаблон для ВСЕХ app-logs-*: не только rollover_alias (иначе rolled-over индексы застревают
  # на "Missing rollover_alias"), но и hot-размещение + 0 реплик — иначе новый app-logs-000002 наследует
  # дефолтную аллокацию и садится на warm-ноду с репликой, хотя bootstrap-индекс был hot/0-реплик.
  curl -s -X PUT "$HOT/_index_template/app-logs-template" "${J[@]}" -d '{
    "index_patterns": ["app-logs-*"],
    "template": { "settings": {
      "plugins.index_state_management.rollover_alias": "app-logs",
      "index.number_of_replicas": 0,
      "index.routing.allocation.require.box_type": "hot"
    } }
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

# --- ожидание реального состояния (polling _ism/explain), а не фиксированные sleep ---
# ISM двигает индексы на прогонах фоновой джобы (job_interval), а не ровно по возрасту,
# поэтому шаги ждём по факту наступления, а не по часам.
http_code()    { curl -s -o /dev/null -w '%{http_code}' "$HOT/$1"; }
index_exists() { [[ "$(http_code "$1")" == 200 ]]; }
index_absent() { [[ "$(http_code "$1")" == 404 ]]; }
in_state()     { curl -s "$HOT/_plugins/_ism/explain/$1" | grep -q "\"state\":{\"name\":\"$2\""; }
snapshot_taken() { curl -s "$HOT/_snapshot/$REPO/_all" | grep -q '"snapshot"'; }

# wait_for <описание> <таймаут_сек> <команда-предикат...>
wait_for() {
  local desc="$1" timeout="$2"; shift 2
  local waited=0
  echo "== ждём: $desc (таймаут ${timeout}s, poll 5s) =="
  until "$@"; do
    if (( waited >= timeout )); then
      echo "!! ТАЙМАУТ ${timeout}s: '$desc' не наступило. Текущее состояние ISM:" >&2
      explain >&2
      return 1
    fi
    sleep 5; waited=$((waited+5))
  done
  echo "-> $desc: наступило через ~${waited}s"
}

# --- полный сценарий: каждый шаг ждём по _ism/explain, а не по таймеру ---
run() {
  setup; load
  wait_for "rollover → создан app-logs-000002"        180 index_exists app-logs-000002
  indices; aliases; explain
  wait_for "app-logs-000001 → warm"                   180 in_state    app-logs-000001 warm
  shards; explain
  wait_for "snapshot app-logs-000001 снят в MinIO"    180 snapshot_taken
  snaps; explain
  wait_for "app-logs-000001 удалён (delete отработал)" 240 index_absent app-logs-000001
  indices; explain
  echo "== цикл завершён: app-logs-000001 прошёл rollover → warm → snapshot → delete =="
}

if [[ $# -eq 0 ]]; then run; else "$@"; fi
