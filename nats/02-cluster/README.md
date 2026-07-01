# 02-cluster — кластер из 3 нод, JetStream RAFT, failover

Демо к статье [«Классический кластер NATS: routes, RAFT и надёжный failover»](https://khorost.tech/messaging/nats-cluster-raft-failover/).

Три ноды `nats-server` (образ `nats:2.12`), объединённые в один Core-кластер через `cluster{routes}` (full-mesh) и один JetStream-кластер (meta-group + per-stream RAFT). Каждая нода — отдельный сервис `n1`/`n2`/`n3` в общей compose-сети, где имена сервисов резолвятся как hostname друг для друга — это и есть адреса в `routes`.

**Этот стенд переиспользуется дальнейшими статьями серии** (клиенты на Go и Java подключаются именно к нему) — не переименовывайте ноды и порты без необходимости.

## Топология

| Нода | Клиентский порт | Мониторинг | Route-порт (внутри сети) |
|------|------------------|------------|---------------------------|
| `n1` | `localhost:4222` | `localhost:8222` | `n1:6222` |
| `n2` | `localhost:4223` | `localhost:8223` | `n2:6222` |
| `n3` | `localhost:4224` | `localhost:8224` | `n3:6222` |

Все три ноды смотрят в общий JetStream store (`/data`, отдельный bind-mount `./data-N` на каждую) и объявляют друг друга соседями в `cluster.routes` — конфиги отличаются только `server_name` и списком `routes` (себя нода не указывает).

## Запуск

```bash
docker compose up -d
```

Подождите несколько секунд, пока ноды обнаружат друг друга через gossip и сформируют full-mesh routes, а JetStream — meta-group.

`nats` CLI ставится отдельно от сервера ([nats-io/natscli](https://github.com/nats-io/natscli)); контекст на первую ноду:

```bash
nats context save local --server localhost:4222 --select
```

## Проверка кластера

Список серверов (полей больше при наличии `$SYS`-аккаунта; в этом демо аутентификация не настроена, поэтому `nats server ls`/`nats server report jetstream` недоступны без системных привилегий — для проверки состояния кластера используйте monitoring endpoint):

```bash
curl -s localhost:8222/routez | jq '.num_routes, [.routes[].remote_name]'
curl -s localhost:8222/jsz | jq '.meta_cluster'
```

`meta_cluster.cluster_size` должен быть `3`, `meta_cluster.leader` — имя одной из нод (`n1`/`n2`/`n3`).

## R3-стрим

Создать stream `EVENTS` с тремя репликами (RAFF-группа поверх meta-group):

```bash
nats stream add EVENTS --subjects "events.>" --replicas 3 --storage file --defaults
nats stream info EVENTS
```

В блоке `Cluster Information` — `Leader` (одна из трёх нод) и две строки `Replica: ..., current` — все три реплики синхронизированы.

Опубликовать сообщение:

```bash
nats pub events.test "hello"
```

## Failover: падение лидера

Определите текущего лидера стрима (`nats stream info EVENTS | grep Leader`), затем остановите **любую** ноду — пример для `n1`:

```bash
docker compose stop n1
```

Подключитесь к живой ноде (например, `n2` на порту `4223`) и проверьте стрим:

```bash
nats --server localhost:4223 stream info EVENTS
```

Если остановленная нода была лидером — в `Cluster Information` будет новый `Leader` среди оставшихся двух, а погашенная нода отмечена `OFFLINE`. Сообщения, опубликованные до остановки, никуда не делись — `Messages`/`First Sequence`/`Last Sequence` не изменились. Кворум для R3 — 2 из 3, поэтому кластер продолжает принимать публикации и во время простоя одной ноды:

```bash
nats --server localhost:4223 pub events.test2 "during outage"
nats --server localhost:4223 stream info EVENTS   # Messages: 2
```

Верните ноду — она подключится обратно и досинхронизирует отставшие записи RAFT-лога:

```bash
docker compose start n1
sleep 5
nats stream info EVENTS   # все три реплики снова current
```

## Ручной step-down

Не дожидаясь падения ноды, можно вручную попросить лидера стрима уступить место — например, перед плановым обслуживанием ноды-лидера:

```bash
nats stream cluster step-down EVENTS
```

Через секунду-две в кластере будет новый лидер (кто именно — решает RAFT-голосование среди оставшихся реплик), а данные стрима не меняются — это чисто управленческая операция, не связанная с потерей данных.

## Остановка

```bash
docker compose down -v
```

`./data-1`, `./data-2`, `./data-3` — bind-mount каталоги (не именованные volume), `-v` их не удаляет; при необходимости почистите вручную (`rm -rf data-1 data-2 data-3`) перед следующим чистым запуском.
