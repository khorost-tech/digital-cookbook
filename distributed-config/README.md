# distributed-config — etcd, ZooKeeper, Consul, Vault на живом стенде

Стенд к статье [«Распределённые конфигурации»](https://khorost.tech/architecture/distributed-configuration/).
Одной командой поднимаются четыре инструмента и PostgreSQL, а demo-скрипты показывают,
**какую задачу решает каждый** — на их собственных сильных сторонах.

## Что демонстрирует

| Инструмент | Демо | Что видно |
|---|---|---|
| **etcd** | `etcd-demo.sh` | put/get и `watch` — реакция на изменение ключа в реальном времени |
| **ZooKeeper** | `zookeeper-demo.sh` | persistent znode (config) и ephemeral znode (liveness — исчезает со смертью сессии) |
| **Consul** | `consul-demo.sh` | KV-хранилище и service discovery (регистрация + поиск сервиса) |
| **Vault** | `vault-demo.sh` | **динамические** credentials: Vault сам создаёт временную учётку в PostgreSQL с TTL |

Идея стенда — показать, что инструменты не взаимозаменяемы: etcd и ZooKeeper про
координацию, Consul про discovery, Vault про секреты (а ZooKeeper здесь ещё и как
исторический предок этого ландшафта).

## Запуск

Требуется Docker и `docker compose`. Все сервисы поднимаются в dev-режиме —
**только для демонстрации, не для production** (Vault dev без печати, без TLS).

```bash
# полная демонстрация: поднять всё, прогнать четыре демо, погасить
bash scripts/smoke.sh
```

Отдельные демо (стенд должен быть поднят — `docker compose up -d`):

```bash
bash scripts/etcd-demo.sh
bash scripts/zookeeper-demo.sh
bash scripts/consul-demo.sh
bash scripts/vault-demo.sh
```

## Структура

```
distributed-config/
  docker-compose.yml      # etcd + zookeeper + consul + vault (dev) + postgres
  scripts/
    smoke.sh              # up → четыре демо → down (trap)
    etcd-demo.sh          # put/get/watch
    zookeeper-demo.sh     # persistent + ephemeral znodes
    consul-demo.sh        # KV + service discovery
    vault-demo.sh         # динамические креды к PostgreSQL
```

## Лицензия

MIT — см. [LICENSE](../LICENSE) в корне репозитория.
