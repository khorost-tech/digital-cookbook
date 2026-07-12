#!/usr/bin/env bash
# Наполнение стенда для статьи «Семантический поиск в OpenSearch».
# Стенд: docker compose up -d  (OpenSearch 3.5.0 knn + Python, см. docker-compose.yml)
#
# Запуск:  ./setup.sh                 — knn-индекс + демо-данные (то же, что ./setup.sh load)
#          ./setup.sh load            — knn-индекс + демо-данные
#          ./setup.sh mlcommons       — register+deploy модели, ingest-pipeline, индекс articles-neural
#          ./setup.sh hybrid_pipeline — search-pipeline hybrid-pipeline (нормализация + веса)
# Внешняя генерация эмбеддингов (внешний knn-путь) — отдельно, через embed.py.
set -euo pipefail

BASE="${BASE:-http://localhost:9223}"          # OpenSearch (security выключен, plain HTTP)
J=(-H 'Content-Type: application/json')

req() { curl -s "$@"; echo; }

# --- 1. knn-индекс + демо-данные (без векторов; векторы добавит внешняя генерация / ingest-pipeline) ---
load() {
  curl -s -X DELETE "$BASE/articles-knn" >/dev/null 2>&1 || true
  curl -s -X PUT "$BASE/articles-knn" "${J[@]}" -d '{
    "settings": { "index": { "knn": true, "number_of_shards": 1, "number_of_replicas": 0 } },
    "mappings": { "properties": {
      "title": { "type": "text" },
      "body":  { "type": "text" },
      "tags":  { "type": "keyword" },
      "embedding": { "type": "knn_vector", "dimension": 384,
        "method": { "name": "hnsw", "space_type": "cosinesimil", "engine": "lucene" } }
    } }
  }'; echo
  curl -s -X POST "$BASE/articles-knn/_bulk?refresh=true" \
    -H 'Content-Type: application/x-ndjson' --data-binary @data/articles.ndjson >/dev/null
  curl -s "$BASE/articles-knn/_count?pretty"
}

# match-промах (лексика не понимает смысл):
match_miss() {
  req -X POST "$BASE/articles-knn/_search" "${J[@]}" -d '{"query":{"match":{"body":"увеличить скорость вставки"}},"_source":["title"]}'
}

# --- 2. ml-commons: register+deploy модели, ingest-pipeline auto-embed, индекс articles-neural ---
# Печатает model_id в конце — подставьте его в neural-запросы (queries.sh).
# NB: модель качается движком OpenSearch; на demo-стенде — через JVM-прокси (OPENSEARCH_JAVA_OPTS
#     -Dhttp.proxyHost/-Dhttps.proxyHost, см. docker-compose.yml), если прямого доступа в интернет нет.
mlcommons() {
  # настройки (model_access_control_disabled в 3.5.0 не распознаётся — не добавлять)
  curl -s -X PUT "$BASE/_cluster/settings" "${J[@]}" -d '{"persistent":{
    "plugins.ml_commons.allow_registering_model_via_url": true,
    "plugins.ml_commons.only_run_on_ml_node": false,
    "plugins.ml_commons.native_memory_threshold": 100 }}' >/dev/null
  # register + ждать COMPLETED
  local task mid
  task=$(curl -s -X POST "$BASE/_plugins/_ml/models/_register" "${J[@]}" -d '{
    "name":"huggingface/sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2",
    "version":"1.0.1","model_format":"TORCH_SCRIPT"}' | grep -o '"task_id":"[^"]*"' | cut -d'"' -f4)
  echo "register task: $task (качается модель ~135МБ, ждём...)"
  for _ in $(seq 1 60); do
    local st; st=$(curl -s "$BASE/_plugins/_ml/tasks/$task" | grep -o '"state":"[^"]*"' | cut -d'"' -f4)
    [ "$st" = "COMPLETED" ] && break; [ "$st" = "FAILED" ] && { echo "register FAILED"; return 1; }; sleep 10
  done
  mid=$(curl -s "$BASE/_plugins/_ml/tasks/$task" | grep -o '"model_id":"[^"]*"' | cut -d'"' -f4)
  [ -n "$mid" ] || { echo "register: не получен model_id — модель не зарегистрировалась (проверьте загрузку модели/прокси)"; return 1; }
  # deploy + ждать DEPLOYED
  curl -s -X POST "$BASE/_plugins/_ml/models/$mid/_deploy" >/dev/null
  local ms=""
  for _ in $(seq 1 30); do
    ms=$(curl -s "$BASE/_plugins/_ml/models/$mid" | grep -o '"model_state":"[^"]*"' | cut -d'"' -f4)
    [ "$ms" = "DEPLOYED" ] && break; sleep 8
  done
  [ "$ms" = "DEPLOYED" ] || { echo "deploy: модель $mid не вышла в DEPLOYED (текущее состояние: ${ms:-неизвестно})"; return 1; }
  # ingest-pipeline + индекс articles-neural + заливка (эмбеддинги проставятся автоматически)
  curl -s -X PUT "$BASE/_ingest/pipeline/embed-pipeline" "${J[@]}" -d "{\"processors\":[{\"text_embedding\":{\"model_id\":\"$mid\",\"field_map\":{\"body\":\"embedding\"}}}]}" >/dev/null
  curl -s -X DELETE "$BASE/articles-neural" >/dev/null 2>&1 || true
  curl -s -X PUT "$BASE/articles-neural" "${J[@]}" -d '{
    "settings":{"index":{"knn":true,"number_of_shards":1,"number_of_replicas":0,"default_pipeline":"embed-pipeline"}},
    "mappings":{"properties":{"title":{"type":"text"},"body":{"type":"text"},"tags":{"type":"keyword"},
      "embedding":{"type":"knn_vector","dimension":384,"method":{"name":"hnsw","space_type":"cosinesimil","engine":"lucene"}}}}}' >/dev/null
  curl -s -X POST "$BASE/articles-neural/_bulk?refresh=true" -H 'Content-Type: application/x-ndjson' --data-binary @data/articles.ndjson >/dev/null
  echo "MODEL_ID=$mid   # подставьте в neural-запросы"
}

# --- 3. гибрид: search-pipeline (нормализация + объединение лексики и семантики) ---
hybrid_pipeline() {
  curl -s -X PUT "$BASE/_search/pipeline/hybrid-pipeline" "${J[@]}" -d '{
    "description":"hybrid lexical+semantic",
    "phase_results_processors":[{"normalization-processor":{
      "normalization":{"technique":"min_max"},
      "combination":{"technique":"arithmetic_mean","parameters":{"weights":[0.3,0.7]}}
    }}]
  }'; echo
}

if [[ $# -eq 0 ]]; then
  load
else
  "$@"
fi
