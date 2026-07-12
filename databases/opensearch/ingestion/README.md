# OpenSearch: сбор данных (API, клиенты, bulk)

Companion-демо к статье [«Сбор данных в OpenSearch: API, клиенты и bulk»](https://khorost.tech/databases/opensearch-data-ingestion/)
(серия «OpenSearch: глубокое погружение»). Всё проверено на **OpenSearch 3.5.0**.

REST/bulk и официальные клиенты на четырёх языках, плюс бенчмарк single vs bulk.

## Запуск

```bash
docker compose up -d                    # OpenSearch 3.5.0 на :9214 (demo security)
```

Клиенты (каждый пишет несколько документов через single index + bulk и разбирает ответ):

| Каталог | Клиент | Запуск |
|---------|--------|--------|
| `clients/go`     | opensearch-go v4.6.0   | `cd clients/go && go run .` |
| `clients/java`   | opensearch-java 3.9.0  | `cd clients/java && mvn compile exec:java` (JDK 21) |
| `clients/rust`   | opensearch-rs 2.4.0    | `cd clients/rust && cargo run` |
| `clients/python` | opensearch-py 3.2.0    | `cd clients/python && pip install -r requirements.txt && python ingest.py` |
| `bench`          | Go, single vs bulk     | `cd bench && go run .` |

Все четыре клиента по умолчанию подключаются к `https://localhost:9214` (стенд слушает
`9214`). Хост берётся из переменной окружения `OS_HOST` (дефолт `localhost`) — если клиент
запускается **из контейнера**, которому нужен хостовый стенд, задайте
`OS_HOST=host.docker.internal` (например, `OS_HOST=host.docker.internal go run .`).
Проверить стенд: `curl -sk -u admin:'<пароль>' https://localhost:9214/_cat/indices?v`.

## Demo credentials

Пароль `IngDemo#2026` и self-signed TLS (demo security дистрибутива) — **только для локального
стенда**. За пределами localhost сгенерируйте свой пароль и выпустите настоящие сертификаты
(см. статью серии про [установку кластера](https://khorost.tech/infrastructure/opensearch-cluster-ansible/)).
Имена индексов (`app-logs-*`, `service-a`/`service-b`) — обобщённые, не из реальной инфраструктуры.
