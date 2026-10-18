# Observability — примеры

Наблюдаемость: конвейеры логов, сбор и доставка телеметрии.

| Стенд | Описание | Статья |
|---|---|---|
| [`log-backends/`](log-backends/) | Три бэкенда логов на одном потоке: Loki, OpenSearch и VictoriaLogs получают одно и то же событие через один Vector с тремя синками. Замеры объёма на диске, памяти в покое и под нагрузкой, поведения при высококардинальном поле (Loki упирается в лимит потоков и отвечает 429) и разницы между значениями по умолчанию и настроенной конфигурацией | 🔜 скоро |
| [`practice-stack/`](practice-stack/) | Четыре сигнала на одном стенде: OTel-инструментирование Go и Java, распределённый трейсинг, корреляция логов, метрики RED/USE с правилами записи, SLO с multi-window burn-rate алертами и Alertmanager, непрерывное профилирование в Pyroscope — девять самопроверок с точными критериями | [статья](https://khorost.tech/observability/otel-instrumentation-go-java/) |
| [`vector-pipeline/`](vector-pipeline/) | Сквозной контур логов на Vector 0.57: file source с checkpoints, VRL-парсинг и фильтрация, транспорт NATS/Kafka, доставка в OpenSearch 3.5.0, дисковые буферы и подтверждения; семь замеров со скриптами | [статья](https://khorost.tech/observability/vector-data-pipeline/) |
| [`vector-vs-collector/`](vector-vs-collector/) | Vector, OpenTelemetry Collector и Fluent Bit на одном потоке логов: конфигурация, поведение под нагрузкой, сравнение | [статья](https://khorost.tech/observability/vector-vs-otel-collector/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
