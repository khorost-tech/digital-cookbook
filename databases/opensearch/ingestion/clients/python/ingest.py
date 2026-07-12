# Демо opensearch-py: одиночный index + helpers.bulk (скрипты/миграции).
# Проверено на OpenSearch 3.5.0. demo-креды — только для локального стенда.
import os

from opensearchpy import OpenSearch, helpers

# По умолчанию — localhost (клиент запускается на хосте рядом со стендом). Для запуска
# ИЗ контейнера, которому нужен хостовый стенд, задайте OS_HOST=host.docker.internal.
OS_HOST = os.environ.get("OS_HOST", "localhost")

client = OpenSearch(
    hosts=[{"host": OS_HOST, "port": 9214}],
    http_auth=("admin", "IngDemo#2026"),
    use_ssl=True,
    verify_certs=False,   # demo only
    ssl_show_warn=False,
)

# одиночный документ
resp = client.index(index="app-logs-python", body={
    "ts": "2026-07-03T10:00:00Z", "level": "error",
    "service": "service-a", "message": "disk full", "status": 500,
})
print("single index ->", resp["result"])

# пакетная загрузка через helpers.bulk (сам режет на чанки)
docs = [
    {"ts": "2026-07-03T10:01:00Z", "level": "info", "service": "service-a", "message": "ok", "status": 200},
    {"ts": "2026-07-03T10:02:00Z", "level": "warn", "service": "service-b", "message": "retry", "status": 429},
]
actions = [{"_index": "app-logs-python", "_source": d} for d in docs]
# raise_on_error=False -> вернёт список ошибок вместо исключения; проверяем!
success, errors = helpers.bulk(client, actions, raise_on_error=False)
print("bulk -> success:", success, "errors:", errors)
