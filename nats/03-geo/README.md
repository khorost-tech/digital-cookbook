# 03-geo — supercluster (gateways), leaf node, mirror-стрим

Демо к статье [«Геораспределённый NATS: superclusters, gateways, leaf nodes и mirror-стримы»](https://khorost.tech/messaging/nats-geo-superclusters-leafnodes/).

Топология:

- **`cluster-a`** — один узел `a1` (образ `nats:2.12`), в нём открыт `leafnodes{listen}` для внешних edge-подключений.
- **`cluster-b`** — два узла `b1`/`b2`, соединённых обычным Core-кластером (`cluster{routes}`, как в [`02-cluster`](../02-cluster)) — реалистичнее одиночной ноды для целевого кластера mirror-стрима.
- **`gateway`** — `cluster-a` и `cluster-b` объединены в один supercluster через `gateway{gateways:[...]}`.
- **`leaf1`** — leaf-нода, солиситует соединение к `a1` через `leafnodes.remotes`.

**Почему в `cluster-b` две ноды, а не одна.** JetStream meta-group у gateway-связанного supercluster с совсем маленькими кластерами не всегда режется строго по `cluster.name` — при малом числе JetStream-нод она может расшириться на весь supercluster целиком (это подтверждено на этом же стенде: при 1+1 ноде `meta.cluster_size` показывал `2` для обоих кластеров одновременно). Чётное число JetStream-нод в supercluster — практический анти-паттерн: кворум meta-group может не набраться при рассогласовании. `1 (cluster-a) + 2 (cluster-b) = 3` — нечётно, meta-group стабильна (`cluster_size: 3`, единый лидер виден на всех трёх нодах). В проде эта проблема решается либо третьим полноценным кластером, либо облегчённым **arbiter** — нодой без JetStream, которая просто участвует в gateway-топологии ради нечётности meta-group. Подробности — в статье.

## Топология портов

| Нода | Кластер | Клиентский порт | Мониторинг | Route (внутри сети) | Gateway (внутри сети) |
|------|---------|------------------|------------|----------------------|-------------------------|
| `a1` | cluster-a | `localhost:4222` | `localhost:8222` | `a1:6222` | `a1:7222`, `leafnodes.listen a1:7422` |
| `b1` | cluster-b | `localhost:4223` | `localhost:8223` | `b1:6222` | `b1:7222` |
| `b2` | cluster-b | `localhost:4225` | `localhost:8225` | `b2:6222` | `b2:7222` |
| `leaf1` | — (leaf) | `localhost:4224` | `localhost:8224` | — | remote → `a1:7422` |

`$SYS`-аккаунт (`accounts{SYS:{users:[{user:sys,password:sysdemo}]}}`) настроен на всех трёх JetStream-нодах — это давнее обязательное правило nats-server: когда на ноде одновременно определены `gateway{}` и `leafnodes{}`, system-аккаунт должен быть задан явно, иначе сервер не стартует (правило не связано с конкретной версией). Пользовательский трафик при этом идёт через дефолтный аккаунт `$G`, который в этом стенде остаётся без аутентификации — осознанное упрощение: демо одноразовое и без чувствительных данных, в проде так делать нельзя. Обычные `nats pub`/`sub`/`stream` команды логина не требуют.

## Запуск

```bash
docker compose up -d
```

Подождите 10–15 секунд, пока ноды поднимут Core-кластер в `cluster-b`, установят gateway-соединения между `cluster-a`/`cluster-b` и сформируют meta-group JetStream.

`nats` CLI ставится отдельно ([nats-io/natscli](https://github.com/nats-io/natscli)).

## Проверка gateway / supercluster

Аутентификации для этого не требуется (см. выше) — но `nats server report`-команды всё равно опираются на `$SYS`, поэтому проще и надёжнее смотреть напрямую в monitoring endpoints, как и в [`02-cluster`](../02-cluster):

```bash
curl -s localhost:8222/varz   | grep -A8 '"gateway"'   # cluster-a видит cluster-b
curl -s localhost:8222/gatewayz                        # outbound/inbound gateway-соединения a1
curl -s localhost:8222/varz   | grep -A6 '"meta"'       # meta.cluster_size должен быть 3
```

`meta.cluster_size: 3` на любой из трёх JetStream-нод — meta-group видит весь supercluster, `leader` — один из `a1`/`b1`/`b2`.

## Interest-propagation: publish в A виден в B

Подписка на `cluster-b` (`b1`), публикация в `cluster-a` (`a1`) — сообщение проходит через gateway именно потому, что на B есть подписчик:

```bash
nats sub --server nats://localhost:4223 "geo.>" &
nats pub --server nats://localhost:4222 geo.test "hello-from-a"
```

Подписчик на `b1` получает сообщение. Если подписчика на стороне B нет вообще — gateway не пересылает `geo.test` через границу кластеров: interest-propagation отправляет трафик только туда, где есть реальный интерес, а не транслирует все subjects во все кластеры supercluster.

## Leaf node: публикация уходит в hub

`leaf1` не входит ни в один Core-кластер — это отдельный процесс с локальной автономностью, который солиситует одно leaf-соединение к `a1`:

```bash
nats sub --server nats://localhost:4222 "edge.>" &
nats pub --server nats://localhost:4224 edge.ping "hello-from-leaf"
```

Подписчик на `a1` (hub) получает сообщение, опубликованное через `leaf1`. Проверить само leaf-соединение:

```bash
curl -s localhost:8222/leafz   # leafnodes: 1, id/name leaf1, account $G
```

## Mirror-стрим: асинхронная репликация между регионами

Stream `SOURCE` создаётся в `cluster-a`, `MIRROR` — в `cluster-b`, с `--mirror SOURCE`. Никакого `external{}`/`--js-domain` не нужно: gateway между кластерами делает origin-stream видимым по имени.

```bash
nats stream add SOURCE --server nats://localhost:4222 \
  --subjects "orders.>" --storage file --replicas 1 --defaults

nats pub --server nats://localhost:4222 orders.eu.1 "order-1"
nats pub --server nats://localhost:4222 orders.eu.2 "order-2"

nats stream add MIRROR --server nats://localhost:4223 \
  --mirror SOURCE --storage file --replicas 1 --defaults

nats stream info MIRROR --server nats://localhost:4223
```

В `Mirror Information` — `Stream Name: SOURCE`, `Lag: 0` после нескольких секунд синхронизации, `State.Messages` совпадает с числом сообщений в `SOURCE`. Публикация нового сообщения в `SOURCE` асинхронно долетает и в `MIRROR`:

```bash
nats pub --server nats://localhost:4222 orders.eu.3 "order-3"
sleep 3
nats stream info MIRROR --server nats://localhost:4223   # Messages: 3
```

`MIRROR` — только для чтения: публиковать в него напрямую нельзя, только читать через consumer.

## Остановка

```bash
docker compose down -v
```

`./data-a1`, `./data-b1`, `./data-b2` — bind-mount каталоги, `-v` их не удаляет; почистите вручную (`rm -rf data-a1 data-b1 data-b2`) перед следующим чистым запуском.
