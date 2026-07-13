#!/usr/bin/env bash
#
# demo.sh — индексы, маппинги, шаблоны и алиасы в OpenSearch на живом стенде.
# Проверено на OpenSearch 3.5.0. Companion к статье:
#   https://khorost.tech/databases/opensearch-indices-mappings/
#
# Поднимает одноузловой контейнер OpenSearch (demo-security, self-signed TLS),
# показывает: dynamic mapping -> component/index templates -> _simulate ->
# _analyze -> dynamic:strict -> bulk -> алиасы с ротацией write-индекса.
#
# ВНИМАНИЕ: demo-пароль ниже — только для локального стенда. Не использовать вне localhost.

set -euo pipefail

NAME="${NAME:-osim-demo}"
PORT="${PORT:-9210}"
PASS="${OPENSEARCH_INITIAL_ADMIN_PASSWORD:-ImDemo#2026}"   # demo-only
IMAGE="${IMAGE:-opensearchproject/opensearch:3.5.0}"
OS="curl -sk -u admin:${PASS} https://localhost:${PORT}"
DIR="$(cd "$(dirname "$0")" && pwd)"

echo "==> Поднимаю контейнер ${NAME} (${IMAGE}) на порту ${PORT}"
docker rm -f "${NAME}" >/dev/null 2>&1 || true
docker run -d --name "${NAME}" -p "${PORT}:9200" \
  -e discovery.type=single-node \
  -e OPENSEARCH_INITIAL_ADMIN_PASSWORD="${PASS}" \
  -e OPENSEARCH_JAVA_OPTS='-Xms1g -Xmx1g' \
  "${IMAGE}" >/dev/null

echo "==> Жду готовности..."
for i in $(seq 1 60); do
  if ${OS}/_cluster/health >/dev/null 2>&1; then echo "    готов (попытка $i)"; break; fi
  sleep 3
done

echo; echo "==> 1. Dynamic mapping: индексирую документ без схемы"
${OS}/app-logs-demo/_doc/1 -XPOST -H 'Content-Type: application/json' \
  -d '{"level":"error","message":"disk full on node","status":500,"ts":"2026-07-03T10:00:00Z","latency_ms":12.7}' >/dev/null
echo "    OpenSearch вывел маппинг сам:"
${OS}/app-logs-demo/_mapping?pretty

echo; echo "==> 2. Явная схема через component + index templates"
${OS}/_component_template/logs-common -XPUT -H 'Content-Type: application/json' -d "@${DIR}/component-templates.json" >/dev/null
${OS}/_index_template/app-logs      -XPUT -H 'Content-Type: application/json' -d "@${DIR}/index-template.json"      >/dev/null
echo "    Резолвинг шаблона для нового индекса app-logs-2026.08:"
${OS}/_index_template/_simulate_index/app-logs-2026.08?pretty -XPOST

echo; echo "==> 3. _analyze: как разбивается text"
${OS}/_analyze -XPOST -H 'Content-Type: application/json' -d '{"analyzer":"standard","text":"Disk Full on service-a"}'

echo; echo "==> 4. dynamic:strict отклоняет неизвестное поле"
${OS}/app-logs-2026.08/_doc -XPOST -H 'Content-Type: application/json' \
  -d '{"level":"error","message":"ok","region":"eu"}' || true

echo; echo "==> 5. Bulk-загрузка демо-документов"
${OS}/_bulk?refresh=true -XPOST -H 'Content-Type: application/x-ndjson' --data-binary "@${DIR}/sample-docs.ndjson" >/dev/null
echo "    документов в app-logs-2026.08: $(${OS}/app-logs-2026.08/_count | sed -E 's/.*"count":([0-9]+).*/\1/')"

echo; echo "==> 6. Алиас app-logs с ротацией write-индекса"
${OS}/app-logs-2026.09 -XPUT >/dev/null 2>&1 || true
${OS}/_aliases -XPOST -H 'Content-Type: application/json' -d '{"actions":[
  {"add":{"index":"app-logs-2026.08","alias":"app-logs","is_write_index":true}}
]}' >/dev/null
echo "    пишу через алиас — уходит в write-индекс:"
${OS}/app-logs/_doc -XPOST -H 'Content-Type: application/json' \
  -d '{"level":"info","message":"via alias","status":200,"ts":"2026-07-03T11:00:00Z","service":"service-b"}' \
  | sed -E 's/.*"_index":"([^"]+)".*/    -> \1/'
echo
echo "    атомарный свап write-индекса на app-logs-2026.09:"
${OS}/_aliases -XPOST -H 'Content-Type: application/json' -d '{"actions":[
  {"add":{"index":"app-logs-2026.08","alias":"app-logs","is_write_index":false}},
  {"add":{"index":"app-logs-2026.09","alias":"app-logs","is_write_index":true}}
]}' >/dev/null
${OS}/_cat/aliases/app-logs?v'&'h=alias,index,is_write_index

echo; echo "==> Готово. Убрать контейнер: docker rm -f ${NAME}"
