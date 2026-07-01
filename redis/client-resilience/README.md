# redis/client-resilience

Живой стенд к статье [Подключение к Redis из Go, Java и Rust](https://khorost.tech/databases/redis-clients-go-java-rust/).
Показывает то, что нельзя увидеть на сниппетах: как клиенты Go (`go-redis`), Java (`Lettuce`) и Rust
(`redis-rs`) ведут себя при **падении узла** в топологиях Redis Cluster и Sentinel — окно недоступности,
ошибки и последующий reconnect.

## Что внутри

```
client-resilience/
  cluster/docker-compose.yml    # Redis Cluster: 3 primary + 3 replica (шардирование по слотам)
  sentinel/docker-compose.yml   # 1 primary + 2 replica + 3 sentinel (HA одного primary)
  clients/
    go/     go-redis:  cluster + sentinel в одном бинарнике (REDIS_MODE)
    java/   Lettuce:   cluster (+ topology refresh) + sentinel
    rust/   redis-rs:  cluster-async + sentinel
```

Каждый клиент — простой read-through цикл: раз в секунду `GET user:42`, при промахе `SET ... EX 30`.
Клиент **не падает** на ошибке: он логирует её и продолжает — поэтому при failover видно и окно
недоступности, и восстановление.

## Требования

- Docker + Docker Compose v2.
- Свободные подсети `172.28.0.0/16` (cluster) и `172.29.0.0/16` (sentinel).

Версии зафиксированы и сверены на актуальность (июль 2026): Redis `8.8`, go-redis `v9.21`,
Lettuce `7.6`, redis-rs `1.x`. Rust-клиент использует трейт `AsyncTypedCommands` (redis-rs 1.x).

## Запуск

### Cluster

```bash
cd cluster
docker compose --profile go   up --build     # или --profile java / --profile rust
```

Дождитесь, пока `cluster-init` соберёт кластер, и клиент начнёт печатать `HIT/MISS`.
В другом терминале уроните один из primary-узлов и смотрите логи клиента:

```bash
docker kill client-resilience-cluster-redis-1-1
```

Ожидаемо: несколько секунд `ERROR` (окно, пока кластер промотирует реплику упавшего primary
и клиент обновляет карту слотов), затем снова `HIT/MISS`. Имя контейнера — из `docker compose ps`.

### Sentinel

```bash
cd sentinel
docker compose --profile go   up --build     # или --profile java / --profile rust
```

Уроните primary:

```bash
docker kill client-resilience-sentinel-redis-primary-1
```

Ожидаемо: `ERROR` в течение `down-after-milliseconds` (5с) + время на кворум и промоушен,
затем Sentinel назначает нового primary, клиент повторно резолвит его и продолжает.

> Записи, принятые упавшим primary, но не успевшие реплицироваться, при промоушене теряются —
> репликация асинхронная. Это не баг стенда, а свойство топологии (см. статью).

## Остановка

```bash
docker compose --profile go down -v          # тот же профиль, что запускали
```

## Лицензия

MIT — см. [LICENSE](../../LICENSE) в корне репозитория.
