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

# устойчивые к сбою хелперы: под `set -euo pipefail` голый `curl|grep` роняет скрипт,
# если grep ничего не нашёл (транзиентно пустой ответ ml-commons при CREATED/DEPLOYING).
mlget() { curl -s "$@" 2>/dev/null || true; }
jget()  { grep -o "\"$1\":\"[^\"]*\"" | head -1 | cut -d'"' -f4 || true; }
jerr()  { grep -o '"error":"[^"]*"' | head -1 | head -c 400 || true; }

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
  # --- register: качает модель (~135МБ). Требует выхода в интернет/прокси (docker-compose.yml). ---
  local task st mid ms attempt
  task=$(mlget -X POST "$BASE/_plugins/_ml/models/_register" "${J[@]}" -d '{
    "name":"huggingface/sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2",
    "version":"1.0.1","model_format":"TORCH_SCRIPT"}' | jget task_id)
  [ -n "$task" ] || { echo "register: не получен task_id — проверьте доступность ml-commons"; return 1; }
  echo "register task: $task (качается модель ~135МБ, ждём COMPLETED)"
  st=""
  for _ in $(seq 1 60); do
    st=$(mlget "$BASE/_plugins/_ml/tasks/$task" | jget state)
    [ "$st" = "COMPLETED" ] && break
    if [ "$st" = "FAILED" ]; then
      echo "register FAILED: $(mlget "$BASE/_plugins/_ml/tasks/$task" | jerr)"
      echo "  обычно причина — недоступность источника модели; включите прокси в docker-compose.yml"
      return 1
    fi
    sleep 10
  done
  [ "$st" = "COMPLETED" ] || { echo "register: не дошёл до COMPLETED за отведённое время (последнее: ${st:-?})"; return 1; }
  mid=$(mlget "$BASE/_plugins/_ml/tasks/$task" | jget model_id)
  [ -n "$mid" ] || { echo "register COMPLETED, но model_id пуст"; return 1; }
  echo "model_id: $mid"

  # --- deploy: подгружает PyTorch native runtime (DJL). Именно здесь ловится Premature EOF при
  #     флаки-сети — поэтому deploy РЕТРАИМ и на каждом шаге печатаем реальный статус/ошибку. ---
  ms=""
  for attempt in 1 2 3; do
    echo "deploy попытка $attempt/3 (подгружается PyTorch native runtime, DJL)..."
    mlget -X POST "$BASE/_plugins/_ml/models/$mid/_deploy" >/dev/null
    for _ in $(seq 1 40); do
      ms=$(mlget "$BASE/_plugins/_ml/models/$mid" | jget model_state)
      [ "$ms" = "DEPLOYED" ] && break
      if [ "$ms" = "DEPLOY_FAILED" ]; then
        echo "  DEPLOY_FAILED: $(mlget "$BASE/_plugins/_ml/models/$mid" | jerr)"
        break
      fi
      sleep 8
    done
    [ "$ms" = "DEPLOYED" ] && { echo "  ✔ DEPLOYED"; break; }
    echo "  deploy не удался (state=${ms:-?}); undeploy и повтор..."
    mlget -X POST "$BASE/_plugins/_ml/models/$mid/_undeploy" >/dev/null; sleep 5
  done
  [ "$ms" = "DEPLOYED" ] || { echo "deploy: модель $mid не вышла в DEPLOYED после 3 попыток (последнее: ${ms:-?}).
  «Premature EOF» при загрузке PyTorch native — почти всегда сеть: включите HTTP(S)-прокси в
  docker-compose.yml (OPENSEARCH_JAVA_OPTS -Dhttp.proxyHost/-Dhttps.proxyHost) и повторите ./setup.sh mlcommons"; return 1; }
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
