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
| [`ansible/compose-deploy`](ansible/compose-deploy) | Деплой Docker Compose стека через Ansible: `community.docker.docker_compose_v2`, group_vars, Jinja2-шаблоны, ansible-vault | [Деплой Docker Compose через Ansible](https://khorost.tech/infrastructure/ansible-docker-compose-deploy/) |
| [`terraform/docker-host-handoff`](terraform/docker-host-handoff) | Стык Terraform → Ansible: Terraform поднимает docker-хост + firewall и генерирует Ansible inventory, Ansible готовит хост | [Terraform для Docker-хостов и сетей](https://khorost.tech/infrastructure/terraform-docker-hosts-and-networks/) |
| [`redis/client-resilience`](redis/client-resilience) | Клиенты Go/Java/Rust под нагрузкой: Redis Cluster и Sentinel на живом стенде, reconnect и failover при падении узла | [Подключение к Redis из Go, Java и Rust](https://khorost.tech/databases/redis-clients-go-java-rust/) |
| [`postgres/client-resilience`](postgres/client-resilience) | Клиенты Go/Java/Rust к PostgreSQL: primary+replica+pgbouncer, пул, чтение с реплики, reconnect и failover | [Клиенты Go, Java и Rust к PostgreSQL: надёжность подключений](https://khorost.tech/databases/postgres-clients-reliability-go-java-rust/) |
| [`performance/highload-lowlatency`](performance/highload-lowlatency) | Highload под SLA < 300 мс: HAProxy L7 (HTTP/2 cleartext, h2c) + пул Go/Java-бэкендов + клиенты-нагрузчики, сравнение L4 vs L7 | [Бюджет латентности и выбор транспорта](https://khorost.tech/performance/latency-budget-and-transport/) |

## Лицензия

MIT — см. [LICENSE](LICENSE).
