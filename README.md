# digital-cookbook

Коллекция рабочих примеров к статьям [khorost.tech](https://khorost.tech).
Каждый каталог — самодостаточный стенд, который можно поднять одной командой.

## Примеры

| Пример | Описание | Статьи |
|--------|----------|--------|
| [`rabbitmq/ha-cluster`](rabbitmq/ha-cluster) | Отказоустойчивый кластер RabbitMQ 4.x: quorum-очереди, failover, DLQ, federation, мониторинг | [Серия «Высокодоступный RabbitMQ»](https://khorost.tech/messaging/rabbitmq-ha-cluster-quorum-failover/) |
| [`proxmox/terraform-vm-automation`](proxmox/terraform-vm-automation) | Terraform для Proxmox: создание VM (клон шаблона или импорт cloud image) и LXC, cloud-init, `for_each` | [Proxmox + Terraform](https://khorost.tech/infrastructure/proxmox-terraform-vm-automation/) |
| [`docker/rootless`](docker/rootless) | rootful vs rootless Docker на живом стенде: владелец файлов в volume, ограничение портов <1024 | [Rootless Docker](https://khorost.tech/docker/rootless-docker/) |
| [`distributed-config`](distributed-config) | etcd, ZooKeeper, Consul, Vault и PostgreSQL: watch, znodes, service discovery, динамические credentials | [Распределённые конфигурации](https://khorost.tech/architecture/distributed-configuration/) |

## Лицензия

MIT — см. [LICENSE](LICENSE).
