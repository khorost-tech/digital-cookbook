# Observability — примеры

Наблюдаемость: конвейеры логов, сбор и доставка телеметрии.

| Стенд | Описание | Статья |
|---|---|---|
| [`vector-pipeline/`](vector-pipeline/) | Сквозной контур логов на Vector 0.57: file source с checkpoints, VRL-парсинг и фильтрация, транспорт NATS/Kafka, доставка в OpenSearch 3.5.0, дисковые буферы и подтверждения; семь замеров со скриптами | [статья](https://khorost.tech/observability/vector-data-pipeline/) |
| [`vector-vs-collector/`](vector-vs-collector/) | Vector, OpenTelemetry Collector и Fluent Bit на одном потоке логов: конфигурация, поведение под нагрузкой, сравнение | [статья](https://khorost.tech/observability/vector-vs-otel-collector/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
