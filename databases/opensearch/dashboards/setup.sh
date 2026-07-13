#!/usr/bin/env bash
# Наполнение стенда для статьи «OpenSearch Dashboards».
# Стенд: docker compose up -d  (OpenSearch 3.5.0 demo security + Dashboards 3.5.0, см. docker-compose.yml)
#
# Запуск:  ./setup.sh          — данные + saved objects + RBAC
#          ./setup.sh load     — только демо-данные
#          ./setup.sh objects  — только index-pattern/visualization/dashboard
#          ./setup.sh rbac     — только reader-роль/role_mapping/юзер
set -euo pipefail

OS_URL="${OS_URL:-https://localhost:9221}"       # OpenSearch REST
DASH_URL="${DASH_URL:-http://localhost:5602}"    # OpenSearch Dashboards
OS_AUTH="${OS_AUTH:-admin:DashDemo#2026}"        # DEMO-ONLY
OSc=(curl -sk -u "$OS_AUTH")                                   # OpenSearch (self-signed TLS)
DASHc=(curl -s -u "$OS_AUTH" -H 'osd-xsrf: true' -H 'Content-Type: application/json')  # Dashboards saved-objects API

# --- 1. демо-данные app-logs ---
load() {
  "${OSc[@]}" -X PUT "$OS_URL/app-logs-000001" -H 'Content-Type: application/json' -d '{
    "settings":{"number_of_shards":1,"number_of_replicas":0},
    "mappings":{"properties":{
      "@timestamp":{"type":"date"},
      "level":{"type":"keyword"},
      "service":{"type":"keyword"},
      "message":{"type":"text","fields":{"keyword":{"type":"keyword"}}}
    }},
    "aliases":{"app-logs":{}}
  }'; echo
  "${OSc[@]}" -X POST "$OS_URL/app-logs-000001/_bulk?refresh=true" \
    -H 'Content-Type: application/x-ndjson' --data-binary @data/app-logs.ndjson >/dev/null
  "${OSc[@]}" "$OS_URL/app-logs-000001/_count?pretty"
}

# --- 2. saved objects (index-pattern + visualization + dashboard) ---
# Импорт заранее выгруженного набора (saved-objects.ndjson) — тянет всю цепочку зависимостей.
objects() {
  curl -s -u "$OS_AUTH" -H 'osd-xsrf: true' \
    -F file=@saved-objects.ndjson "$DASH_URL/api/saved_objects/_import?overwrite=true"
  echo
}

# --- 3. RBAC: reader-роль, ограниченная index-паттерном, + юзер + role_mapping ---
rbac() {
  "${OSc[@]}" -X PUT "$OS_URL/_plugins/_security/api/roles/app_logs_reader" -H 'Content-Type: application/json' -d '''{
    "cluster_permissions": ["cluster_composite_ops_ro"],
    "index_permissions": [
      { "index_patterns": ["app-logs-*"], "allowed_actions": ["read","search","indices:data/read/*","indices_monitor"] }
    ]
  }'''; echo
  "${OSc[@]}" -X PUT "$OS_URL/_plugins/_security/api/internalusers/reader" -H 'Content-Type: application/json' -d '''{"password":"DashRead#2026","backend_roles":[]}'''; echo   # DEMO-ONLY
  "${OSc[@]}" -X PUT "$OS_URL/_plugins/_security/api/rolesmapping/app_logs_reader" -H 'Content-Type: application/json' -d '''{"users":["reader"]}'''; echo
  "${OSc[@]}" -X PUT "$OS_URL/_plugins/_security/api/rolesmapping/kibana_user" -H 'Content-Type: application/json' -d '''{"users":["reader"]}'''; echo
}

if [[ $# -eq 0 ]]; then
  load; objects; rbac
else
  "$@"
fi
