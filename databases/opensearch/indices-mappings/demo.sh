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

# имя контейнера — через OSIM_NAME (специфичная переменная), чтобы не подхватить чужой $NAME из окружения
OSIM_NAME="${OSIM_NAME:-osim-demo}"
PORT="${PORT:-9210}"
PASS="${OPENSEARCH_INITIAL_ADMIN_PASSWORD:-ImDemo#2026}"   # demo-only
IMAGE="${IMAGE:-opensearchproject/opensearch:3.5.0}"
BASE="https://localhost:${PORT}"
# --retry/-m: узел первые секунды после старта отвечает медленно; под `set -e` один транзиентный
# таймаут оборвал бы всё демо, поэтому демо-запросы переживают короткие заминки прогрева.
OS="curl -sk --connect-timeout 5 -m 60 --retry 3 --retry-delay 2 -u admin:${PASS} ${BASE}"
DIR="$(cd "$(dirname "$0")" && pwd)"

# HTTP-код аутентифицированного запроса — для readiness и явных ассертов.
# `|| true`: пока контейнер стартует, curl не может подключиться (exit 28) и под `set -e` уронил бы
# скрипт; так он печатает "000" и не прерывает readiness-цикл.
code() { curl -sk -o /dev/null -w '%{http_code}' -u "admin:${PASS}" "$@" 2>/dev/null || true; }
# провалить скрипт с сообщением (иначе демо «зеленело» даже при не отработавших проверках)
fail() { echo "!! АССЕРТ ПРОВАЛЕН: $*" >&2; exit 1; }

echo "==> Поднимаю контейнер ${OSIM_NAME} (${IMAGE}) на порту ${PORT}"
docker rm -f "${OSIM_NAME}" >/dev/null 2>&1 || true
docker run -d --name "${OSIM_NAME}" -p "${PORT}:9200" \
  -e discovery.type=single-node \
  -e OPENSEARCH_INITIAL_ADMIN_PASSWORD="${PASS}" \
  -e OPENSEARCH_JAVA_OPTS='-Xms1g -Xmx1g' \
  "${IMAGE}" >/dev/null

echo "==> Жду готовности кластера И инициализации Security..."
# _cluster/health отвечает и ДО инициализации Security (503 «OpenSearch Security not initialized»),
# а curl при этом завершается успехом — поэтому ждём именно аутентифицированный 200 с реальным
# статусом в теле, иначе первые запросы демо летят в неинициализированный Security.
ready=false
for i in $(seq 1 90); do
  hc="$(code "${BASE}/_cluster/health")"
  body="$(${OS}/_cluster/health 2>/dev/null || true)"
  if [[ "$hc" == "200" ]] && echo "$body" | grep -q '"status"' && ! echo "$body" | grep -qi 'not initialized'; then
    # health зелёный и Security инициализирован — но узел ещё может не принимать запись.
    # Пробуем реальный PUT: только когда он проходит, шлём первый документ демо.
    if [[ "$(code -XPUT "${BASE}/.osim-readiness-probe")" == "200" ]]; then
      ${OS}/.osim-readiness-probe -XDELETE >/dev/null 2>&1 || true
      echo "    готов: status=$(echo "$body" | sed -E 's/.*"status":"([a-z]+)".*/\1/'), Security инициализирован, запись принимается (попытка $i)"
      ready=true; break
    fi
  fi
  sleep 3
done
[[ "$ready" == true ]] || { echo "!! Кластер/Security не готовы за отведённое время; последние логи контейнера:" >&2; docker logs --tail 30 "${OSIM_NAME}" >&2; exit 1; }

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

echo; echo "==> 4. dynamic:strict отклоняет неизвестное поле (ожидаем HTTP 400)"
strict_code="$(code -XPOST -H 'Content-Type: application/json' \
  -d '{"level":"error","message":"ok","region":"eu"}' "${BASE}/app-logs-2026.08/_doc")"
echo "    документ с неизвестным полем region → HTTP ${strict_code}"
[[ "$strict_code" == "400" ]] || fail "dynamic:strict должен был отклонить поле region (ожидался HTTP 400 strict_dynamic_mapping_exception), получено HTTP ${strict_code} — возможно, Security ещё не был готов или шаблон не применился"
echo "    OK: неизвестное поле отклонено, как и требует dynamic:strict"

echo; echo "==> 5. Bulk-загрузка демо-документов"
expected=$(grep -cE '^[[:space:]]*\{[[:space:]]*"(index|create)"' "${DIR}/sample-docs.ndjson")
bulk_resp="$(${OS}/_bulk?refresh=true -XPOST -H 'Content-Type: application/x-ndjson' --data-binary "@${DIR}/sample-docs.ndjson")"
echo "$bulk_resp" | grep -q '"errors":false' || fail "bulk вернул ошибки индексации: $(echo "$bulk_resp" | head -c 400)"
actual="$(${OS}/app-logs-2026.08/_count | sed -E 's/.*"count":([0-9]+).*/\1/')"
echo "    документов в app-logs-2026.08: ${actual} (ожидалось ${expected})"
[[ "$actual" == "$expected" ]] || fail "ожидалось ${expected} документов после bulk, в индексе ${actual}"
echo "    OK: все ${expected} документа проиндексированы без ошибок"

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

echo; echo "==> Готово. Убрать контейнер: docker rm -f ${OSIM_NAME}"
