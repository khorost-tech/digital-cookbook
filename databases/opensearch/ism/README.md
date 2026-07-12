# OpenSearch: ISM и жизненный цикл индексов (retention)

Companion-демо к статье [«Retention в OpenSearch: ISM, rollover и жизненный цикл индексов»](https://khorost.tech/databases/opensearch-ism-retention/)
(серия «OpenSearch: глубокое погружение»).

**Мульти-нодовый** стенд: 2 ноды OpenSearch **3.5.0** (hot + warm) + **MinIO** (S3-репозиторий
снапшотов). Демонстрирует полный живой цикл лог-индекса под управлением ISM:
**rollover под write-алиасом → hot → warm (allocation) → snapshot в MinIO → delete**.

## Запуск

```bash
docker compose up -d                 # 2 ноды (os-hot :9218 / os-warm :9219) + MinIO (:9320, консоль :9321)
```

Образ OpenSearch собирается из `Dockerfile` — в стандартный образ 3.5.0 доустанавливается плагин
`repository-s3` (без него снапшоты в S3 недоступны). Дождитесь `number_of_nodes: 2`, `status: green`:

```bash
curl -s localhost:9218/_cluster/health?pretty
```

Создайте бакет и прогоните весь сценарий:

```bash
# бакет для снапшотов в MinIO
docker run --rm --network ism_default --entrypoint /bin/sh minio/mc \
  -c "mc alias set m http://ism-minio:9000 minioadmin 'IsmDemo#2026' && mc mb -p m/snapshots"

cd opensearch && ./ism.sh                 # весь цикл: репо + политика + bootstrap + rollover + переходы
#   (если ./ism.sh ругается на права — запустите: bash ism.sh)
./ism.sh explain                          # текущее состояние индексов в любой момент
```

`ism.sh` регистрирует S3-репозиторий, создаёт ISM-политику (`policy.json`), заводит index-шаблон
(`rollover_alias` + hot-размещение + 0 реплик, чтобы rolled-over `app-logs-000002` не сел на warm-ноду)
и bootstrap-индекс `app-logs-000001` под write-алиасом `app-logs`, заливает документы и ждёт каждого
перехода **по факту** — опросом `_plugins/_ism/explain` с таймаутом, а не фиксированными паузами
(ISM двигает индексы на прогонах фоновой джобы `job_interval`, а не ровно по возрасту).

## Ускоренные пороги (demo)

В `policy.json` пороги переходов **специально занижены** до минут (`min_index_age: "1m"/"2m"/"3m"`,
`rollover min_doc_count: 5`), чтобы прогнать весь жизненный цикл живьём за несколько минут. **В проде**
это дни и десятки гигабайт (например, `min_index_age: "14d"`) — перед выкатом политики обязательно
верните пороги к реальным значениям.

## Demo credentials

- OpenSearch: security **отключён** (`DISABLE_SECURITY_PLUGIN=true`, plain HTTP) — стенд посвящён ISM,
  а не TLS (TLS/security — тема статьи про [установку кластера](https://khorost.tech/infrastructure/opensearch-cluster-ansible/)).
- MinIO: `minioadmin` / `IsmDemo#2026`, регион `us-east-1` (repository-s3 на AWS SDK v2 требует регион
  даже для MinIO). S3-креды запечены в образ через `opensearch-keystore` — **только для локального стенда**.

Всё это — DEMO-ONLY. За пределами localhost включите security, настоящие сертификаты и собственные
креды. Имена индексов (`app-logs-*`) — обобщённые, не из реальной инфраструктуры.

## Teardown

```bash
docker compose down -v
```
