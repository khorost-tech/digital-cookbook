# OpenSearch Dashboards: Discover, визуализации и дашборды

Companion-демо к статье [«OpenSearch Dashboards: Discover, визуализации и дашборды»](https://khorost.tech/observability/opensearch-dashboards/)
(серия «OpenSearch: глубокое погружение»).

Стенд: **OpenSearch 3.5.0** (demo security) + **OpenSearch Dashboards 3.5.0**. Security ВКЛючён — нужен
для логина в Dashboards и живого RBAC (reader-роль, ограниченная index-паттерном, tenants).

## Запуск

```bash
docker compose up -d          # OpenSearch (:9221) + Dashboards (:5602)
```

Dashboards стартует ~1–2 минуты. Дождитесь готовности (нужна авторизация — security ВКЛ):

```bash
curl -s -u admin:'DashDemo#2026' http://localhost:5602/api/status | jq .status.overall.state   # green
```

Наполните стенд (`setup.sh` запускается из этого каталога — рядом с `data/` и `saved-objects.ndjson`):

```bash
./setup.sh                    # данные app-logs + saved objects (import) + RBAC (reader-роль/юзер)
# если ./setup.sh ругается на права — запустите: bash setup.sh
./setup.sh load               # только демо-данные
./setup.sh objects            # только импорт saved-objects.ndjson (index-pattern/visualization/dashboard)
./setup.sh rbac               # только reader-роль/role_mapping/юзер
```

Откройте `http://localhost:5602` в браузере, войдите как `admin` / `DashDemo#2026`. В **Discover**
выберите index pattern `app-logs-*`; на дашборде **App logs overview** — визуализация «Events by level».

### Проверить RBAC-изоляцию

Войдите (или сходите REST-ом) под `reader` / `DashRead#2026`:

```bash
curl -sk -u reader:'DashRead#2026' -X POST https://localhost:9221/app-logs/_search -H 'Content-Type: application/json' -d '{"query":{"match_all":{}}}'   # 300 hits
curl -sk -u reader:'DashRead#2026' -X POST https://localhost:9221/other-000001/_search                                                                    # HTTP 403 security_exception
```

Reader видит только `app-logs-*` (роль `app_logs_reader` ограничена этим index-паттерном) и не может даже
перечислить чужие индексы.

## Demo credentials

- OpenSearch: `admin` / `DashDemo#2026`; reader-юзер `reader` / `DashRead#2026`.
- Dashboards: сервисный пользователь `kibanaserver` (дефолт demo security), TLS-проверка отключена
  (`OPENSEARCH_SSL_VERIFICATIONMODE=none`) — self-signed demo.

Всё DEMO-ONLY. За пределами localhost выпустите настоящие сертификаты, включите
`opensearch.ssl.verificationMode: full`, `opensearch_security.cookie.secure: true` и свои креды. Имена
индексов (`app-logs-*`) — обобщённые, не из реальной инфраструктуры.

## Teardown

```bash
docker compose down -v
```
