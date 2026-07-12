#!/usr/bin/env bash
# Запросы OpenSearch для статьи «Полнотекстовый поиск».
# Стенд: docker compose up -d opensearch  (см. ../docker-compose.yml)
# Запуск:  ./queries.sh          — всё подряд
#          ./queries.sh analyze  — только один блок (setup/analyze/queries/relevance/highlight/pagination/aggregations)
#
# Тела запросов передаются через stdin (--data-binary @-), а НЕ через -d '...':
# кириллица в аргументах командной строки на части платформ (Windows Git Bash)
# ломается в argv и приходит битым UTF-8. stdin — байтовый поток, кодировка сохраняется.
set -euo pipefail

BASE="${BASE:-https://localhost:9215}"
AUTH="${AUTH:-admin:FtDemo#2026}"   # DEMO-ONLY

# q METHOD PATH  — тело JSON читается со stdin
q() { curl -sk -u "$AUTH" -X "$1" "$BASE/$2" -H 'Content-Type: application/json' --data-binary @-; echo; }

# --- 0. индекс + данные (идемпотентно) ---
setup() {
  curl -sk -u "$AUTH" -X DELETE "$BASE/articles" >/dev/null 2>&1 || true
  curl -sk -u "$AUTH" -X PUT "$BASE/articles" -H 'Content-Type: application/json' --data-binary @mapping.json; echo
  # _bulk требует Content-Type: application/x-ndjson
  curl -sk -u "$AUTH" -X POST "$BASE/articles/_bulk?refresh=true" \
    -H 'Content-Type: application/x-ndjson' --data-binary @../data/articles.ndjson >/dev/null
  curl -sk -u "$AUTH" "$BASE/articles/_count?pretty"; echo
}

# --- 1. анализаторы ---
analyze() {
  q POST _analyze          <<'JSON'
{"analyzer":"standard","text":"поиск по большим объёмам логов"}
JSON
  q POST articles/_analyze <<'JSON'
{"analyzer":"ru_custom","text":"поиск по большим объёмам логов"}
JSON
  q POST _analyze          <<'JSON'
{"analyzer":"standard","text":"Indexing and searching documents"}
JSON
  q POST articles/_analyze <<'JSON'
{"analyzer":"en_custom","text":"Indexing and searching documents"}
JSON
}

# --- 2. типы запросов ---
queries() {
  q POST articles/_search <<'JSON'
{"query":{"match":{"body":"поиск"}}}
JSON
  q POST articles/_search <<'JSON'
{"query":{"match_phrase":{"body":"полнотекстовый поиск"}}}
JSON
  q POST articles/_search <<'JSON'
{"query":{"multi_match":{"query":"загрузка данных","fields":["title^2","body"]}}}
JSON
  q POST articles/_search <<'JSON'
{"query":{"bool":{"must":[{"match":{"body":"кластер"}}],"filter":[{"term":{"category":"operations"}}],"should":[{"match":{"body":"реплики"}}]}}}
JSON
  q POST articles/_search <<'JSON'
{"query":{"match":{"body":{"query":"кластор","fuzziness":"AUTO"}}}}
JSON
}

# --- 3. релевантность ---
relevance() {
  q POST articles/_search <<'JSON'
{"query":{"function_score":{"query":{"match":{"body":"кластер"}},"functions":[{"gauss":{"published_at":{"origin":"2026-07-03","scale":"90d","decay":0.5}}}],"boost_mode":"multiply"}}}
JSON
  q POST articles/_explain/2 <<'JSON'
{"query":{"match":{"body":"поиск"}}}
JSON
}

# --- 4. подсветка ---
highlight() {
  q POST articles/_search <<'JSON'
{"query":{"match":{"body":"поиск"}},"highlight":{"fields":{"body":{}}}}
JSON
}

# --- 5. пагинация ---
pagination() {
  q POST articles/_search <<'JSON'
{"from":0,"size":2,"sort":[{"published_at":"desc"}],"query":{"match_all":{}}}
JSON
  # search_after: sort последнего hit листа 1 подставить в search_after листа 2
  q POST articles/_search <<'JSON'
{"size":3,"sort":[{"published_at":"desc"},{"_id":"asc"}],"query":{"match_all":{}}}
JSON
  q POST articles/_search <<'JSON'
{"size":3,"sort":[{"published_at":"desc"},{"_id":"asc"}],"search_after":[1775347200000,"4"],"query":{"match_all":{}}}
JSON
}

# --- 6. агрегации ---
aggregations() {
  q POST articles/_search <<'JSON'
{"size":0,"aggs":{"by_tag":{"terms":{"field":"tags"}}}}
JSON
  q POST articles/_search <<'JSON'
{"size":0,"aggs":{"by_cat":{"terms":{"field":"category"},"aggs":{"newest":{"max":{"field":"published_at"}},"oldest":{"min":{"field":"published_at"}}}}}}
JSON
  q POST articles/_search <<'JSON'
{"size":0,"aggs":{"per_month":{"date_histogram":{"field":"published_at","calendar_interval":"month"}}}}
JSON
  q POST articles/_search <<'JSON'
{"size":0,"query":{"match":{"body":"кластер"}},"aggs":{"by_cat":{"terms":{"field":"category"}}}}
JSON
}

if [[ $# -eq 0 ]]; then
  setup; analyze; queries; relevance; highlight; pagination; aggregations
else
  "$@"
fi
