# Полный список примеров

Все стенды digital-cookbook со ссылками на статьи. Навигация по категориям — в [README.md](README.md).

## Architecture

| Пример | Описание | Статья |
|---|---|---|
| [`architecture/distributed-config`](architecture/distributed-config) | etcd, ZooKeeper, Consul, Vault: watch, discovery, dynamic credentials | [статья](https://khorost.tech/architecture/distributed-configuration/) |

## Databases

| Пример | Описание | Статья |
|---|---|---|
| [`databases/opensearch`](databases/opensearch) | OpenSearch: кластер, индексы, ingest, полнотекст, ISM, семантика, Dashboards | [статья](https://khorost.tech/infrastructure/opensearch-cluster-ansible/) |

## Messaging

| Пример | Описание | Статья |
|---|---|---|
| [`messaging/nats`](messaging/nats) | NATS 2.12: Core, JetStream, кластер, гео, безопасность, клиенты | [статья](https://khorost.tech/messaging/nats-core-subjects-request-reply/) |
| [`messaging/rabbitmq`](messaging/rabbitmq) | RabbitMQ 4.x: HA-кластер (quorum, DLQ, federation) и Streams | [статья](https://khorost.tech/messaging/rabbitmq-ha-cluster-quorum-failover/) |

## Go

| Пример | Описание | Статья |
|---|---|---|
| [`go/orm-gorm-vs-jet`](go/orm-gorm-vs-jet) | ORM в Go: GORM vs go-jet | [статья](https://khorost.tech/go/go-orm-gorm-vs-go-jet/) |
| [`go/slog`](go/slog) | Структурированное логирование log/slog: хендлеры, группы, ContextHandler, бенчи | 🔜 скоро |

## Performance

| Пример | Описание | Статья |
|---|---|---|
| [`performance/highload-lowlatency`](performance/highload-lowlatency) | Highload под SLA < 300 мс: HAProxy L7 (h2c) + пул Go/Java-бэкендов, L4 vs L7 | [статья](https://khorost.tech/performance/latency-budget-and-transport/) |

## Infrastructure

| Пример | Описание | Статья |
|---|---|---|
| [`infrastructure/ansible`](infrastructure/ansible) | Деплой Docker Compose стека через Ansible: docker_compose_v2, vault | [статья](https://khorost.tech/infrastructure/ansible-docker-compose-deploy/) |
| [`infrastructure/proxmox`](infrastructure/proxmox) | Terraform для Proxmox: VM/LXC, cloud-init, for_each | [статья](https://khorost.tech/infrastructure/proxmox-terraform-vm-automation/) |
| [`infrastructure/terraform`](infrastructure/terraform) | Стык Terraform → Ansible: docker-хост + firewall + inventory | [статья](https://khorost.tech/infrastructure/terraform-docker-hosts-and-networks/) |

## Docker

| Пример | Описание | Статья |
|---|---|---|
| [`docker/rootless`](docker/rootless) | Rootful vs rootless Docker на живом стенде | [статья](https://khorost.tech/docker/rootless-docker/) |

