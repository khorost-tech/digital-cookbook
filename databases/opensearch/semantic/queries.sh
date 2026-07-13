#!/usr/bin/env bash
# Запросы к семантическому стенду: match-промах, neural (семантика), hybrid (контраст).
# Стенд: docker compose up -d && ./setup.sh load && ./setup.sh mlcommons  (см. README)
# Внешний knn-путь (Python-эмбеддинги) — в embed.py.
#
# MODEL_ID — id развёрнутой модели (печатает ./setup.sh mlcommons). Запуск:
#   MODEL_ID=<id> ./queries.sh
set -euo pipefail

BASE="${BASE:-http://localhost:9223}"
MID="${MODEL_ID:?установите MODEL_ID=<id> из ./setup.sh mlcommons}"
J=(-H 'Content-Type: application/json')
req() { curl -s "$@"; echo; }

Q="как быстрее добавлять документы пачками"     # парафраз про bulk-запись
T="BM25"                                          # точный термин (акроним)

# лексика цепляется за поверхностное слово «документы» → НЕ та статья:
match_wrong() { req -X POST "$BASE/articles-neural/_search" "${J[@]}" -d "{\"_source\":[\"title\"],\"query\":{\"match\":{\"body\":\"$Q\"}}}"; }

# семантика (neural) понимает смысл → bulk #1:
neural() { req -X POST "$BASE/articles-neural/_search" "${J[@]}" -d "{\"size\":3,\"_source\":[\"title\"],\"query\":{\"neural\":{\"embedding\":{\"query_text\":\"$Q\",\"model_id\":\"$MID\",\"k\":3}}}}"; }

# точный термин: чистая семантика «размазывает», гибрид ставит верно:
pure_semantic() { req -X POST "$BASE/articles-neural/_search" "${J[@]}" -d "{\"size\":3,\"_source\":[\"title\"],\"query\":{\"neural\":{\"embedding\":{\"query_text\":\"$T\",\"model_id\":\"$MID\",\"k\":3}}}}"; }
hybrid() { req -X POST "$BASE/articles-neural/_search?search_pipeline=hybrid-pipeline" "${J[@]}" -d "{\"size\":3,\"_source\":[\"title\"],\"query\":{\"hybrid\":{\"queries\":[{\"match\":{\"body\":\"$T\"}},{\"neural\":{\"embedding\":{\"query_text\":\"$T\",\"model_id\":\"$MID\",\"k\":3}}}]}}}"; }

if [[ $# -eq 0 ]]; then
  echo "== match (не та статья) =="; match_wrong
  echo "== neural (bulk по смыслу) =="; neural
  echo "== pure semantic на «$T» (размазан) =="; pure_semantic
  echo "== hybrid на «$T» (резко верно) =="; hybrid
else
  "$@"
fi
