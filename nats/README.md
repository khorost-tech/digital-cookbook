# NATS — примеры к серии «Погружение в NATS»

Рабочие стенды к серии статей [«Погружение в NATS»](https://khorost.tech/messaging/nats-core-subjects-request-reply/) на khorost.tech.
Каждый подкаталог поднимается одной командой `docker compose up -d`. Все секреты — демонстрационные, не для продакшна.

Версия: nats-server 2.12.x.

| Стенд | Тема | Статья |
|-------|------|--------|
| [`00-core`](00-core) | Core NATS: pub/sub, request/reply, queue groups | Ст.1 |
| [`01-jetstream`](01-jetstream) | JetStream: stream, consumer, KV | Ст.2 |
| [`02-cluster`](02-cluster) | Кластер 3 ноды, JetStream RAFT | Ст.3 |
| [`03-geo`](03-geo) | Supercluster (gateways), leaf node, mirror | Ст.4 |
| [`04-bench`](04-bench) | Сценарии `nats bench` | Ст.5 |
| [`clients/go`](clients/go) | Клиент на nats.go | Ст.6 |
| [`clients/java`](clients/java) | Клиент на jnats | Ст.7 |
| [`05-security`](05-security) | accounts, JWT (nsc), mTLS | Ст.8 |
| [`06-observability`](06-observability) | Prometheus + Grafana + nats-exporter, backup | Ст.9 |
