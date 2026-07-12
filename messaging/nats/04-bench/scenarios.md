# Сценарии `nats bench`

Три сценария: Core pub/sub, JetStream publish R1 vs R3, pull-consumer. Для каждого — команда, что она измеряет, и фактические числа с прогона автора (demo-железо, см. оговорку в [README](README.md)).

Проверка синтаксиса: `nats bench --help`, `nats bench js pub sync --help`, `nats bench js fetch --help` — держите под рукой, если версия CLI отличается от `v0.3.0`.

---

## Сценарий 1: Core pub/sub throughput

Стенд: [`../01-jetstream`](../01-jetstream) (JetStream включён на ноде, но в этом сценарии не используется — публикуем в обычный Core subject).

```bash
cd ../01-jetstream
docker compose up -d && sleep 3
```

Публикация без подписчика (верхний предел throughput публикации — сервер не ждёт ничьего ack, кроме TCP-уровня):

```bash
nats --server localhost:4222 bench pub foo --clients 1 --msgs 100000 --size 128 --no-progress
```

Публикация с одновременным подписчиком (полный путь publish → deliver → receive; `bench sub` нужно запустить первым, чтобы подписка успела встать):

```bash
nats --server localhost:4222 bench sub foo --clients 1 --msgs 100000 --no-progress &
sleep 1
nats --server localhost:4222 bench pub foo --clients 1 --msgs 100000 --size 128 --no-progress
```

Несколько параллельных publisher'ов (`--clients`) — проверка масштабирования по клиентам на одном subject:

```bash
nats --server localhost:4222 bench pub foo --clients 2 --msgs 100000 --size 128 --no-progress
```

### Результаты прогона автора

| Вариант | msgs/sec | Латентность publish |
|---|---|---|
| pub, 1 клиент, без подписчика | 450,555 | 2.22 µs |
| pub, 1 клиент, с подписчиком (`bench sub`, деливери подтверждён) | 352,533 | 2.84 µs |
| sub, тот же прогон (получение) | 355,306 | 2.81 µs |
| pub, 2 клиента параллельно (aggregated) | 585,322 | 3.40–3.42 µs на клиента |

Порядок величины — сотни тысяч сообщений в секунду на один процесс `nats-server` без persistence, задержка публикации — единицы микросекунд. Это Core NATS: at-most-once, ничего не хранится, оба числа выше — не гарантия доставки, а верхняя граница механической скорости.

---

## Сценарий 2: JetStream publish R1 vs R3

Показывает главный tradeoff статьи: репликация стоит latency записи.

### R1 (один узел, без RAFT) — стенд `../01-jetstream`

```bash
cd ../01-jetstream
docker compose up -d && sleep 3
```

Синхронная публикация (каждый `Publish()` ждёт ack сервера перед следующим — честная latency на сообщение, не throughput-оптимальный режим):

```bash
nats --server localhost:4222 bench js pub sync jsfoo --create --replicas 1 --storage file --msgs 5000 --size 128 --purge --no-progress
```

Асинхронная публикация (`PublishAsync`, ack собираются батчами — throughput-режим):

```bash
nats --server localhost:4222 bench js pub async jsfoo --create --replicas 1 --storage file --msgs 50000 --size 128 --purge --no-progress
```

Storage `memory` вместо `file` для сравнения (данные пропадают при падении процесса — см. статью, раздел Storage и репликация):

```bash
nats --server localhost:4222 bench js pub sync jsmem --stream benchmem --create --replicas 1 --storage memory --msgs 5000 --size 128 --purge --no-progress
```

```bash
docker compose down -v
```

### R3 (кластер, RAFT-кворум) — стенд `../02-cluster`

```bash
cd ../02-cluster
docker compose up -d && sleep 8
curl -s localhost:8222/jsz | grep cluster_size   # дождаться "cluster_size":3
```

Те же команды, `--replicas 3`, тот же subject-размер батча, чтобы сравнение было честным:

```bash
nats --server localhost:4222 bench js pub sync jsr3 --stream benchr3 --create --replicas 3 --storage file --msgs 5000 --size 128 --purge --no-progress
nats --server localhost:4222 bench js pub async jsr3 --stream benchr3 --replicas 3 --storage file --msgs 50000 --size 128 --purge --no-progress
```

Для контроля — R1 на том же кластерном железе (изолирует переменную: меняется только число реплик, не машина):

```bash
nats --server localhost:4222 bench js pub sync jsr1 --stream benchr1 --create --replicas 1 --storage file --msgs 5000 --size 128 --purge --no-progress
```

```bash
docker compose down -v
rm -rf data-1 data-2 data-3
```

### Результаты прогона автора

| Вариант | msgs/sec | Латентность publish |
|---|---|---|
| R1, file, sync (`01-jetstream`) | 1,307 | 764.69 µs |
| R1, memory, sync (`01-jetstream`) | 1,810 | 552.32 µs |
| R1, file, async (`01-jetstream`) | 4,399 | 227.31 µs (батч) |
| R1, file, sync, то же железо что R3 (`02-cluster`, контроль) | 1,259 | 793.67 µs |
| **R3, file, sync (`02-cluster`)** | **491** | **2,035.79 µs** |
| R3, file, async (`02-cluster`) | 24,864 | 40.22 µs (батч, см. оговорку ниже) |

R1-контроль на кластерном железе (1,259 msgs/sec) практически совпадает с R1 на одиночном узле (1,307) — значит разница между R1 и R3 ниже объясняется именно репликацией, а не разным железом под контейнерами. **Синхронная публикация: R3 медленнее R1 примерно в 2.5–2.7 раза по latency** — каждая запись ждёт подтверждения от кворума (2 из 3), а не только записи на диск локальной ноды. Это и есть цена RAFT-репликации, показанная в статье диаграммой replica factor → latency.

Async-числа с R3 (24,864 msgs/sec, 40.22 µs) не читайте как «async отменяет цену репликации» — `PublishAsync` просто перекрывает время ожидания ack следующими публикациями (конвейеризация), поэтому *средняя видимая клиентом* задержка на сообщение падает, а не сама работа кворума. Реальная задержка commit на стороне сервера для R3 никуда не делась — просто клиент не блокируется на ней построчно. Для приложений, которым важна одна конкретная гарантированная запись (а не средний throughput), это разница существенная.

---

## Сценарий 3: Pull-consumer (fetch)

Показывает скорость вычитывания уже подтверждённого backlog — отдельная переменная от publish.

```bash
cd ../02-cluster   # или ../01-jetstream для R1
docker compose up -d && sleep 8
```

Наполнить stream (переиспользуем R3-стрим из сценария 2 либо создать заново):

```bash
nats --server localhost:4222 bench js pub sync jsr3 --stream benchr3 --create --replicas 3 --storage file --msgs 20000 --size 128 --purge --no-progress
```

Создать durable pull-consumer с explicit ack (см. статью 2 серии — та же модель ack/redelivery):

```bash
nats --server localhost:4222 consumer add benchr3 bench-pull --pull --ack explicit --filter jsr3 --defaults
```

Бенчмарк вычитывания (`fetch` — синхронный pull-запрос батчами, `--batch` по умолчанию 500):

```bash
nats --server localhost:4222 bench js fetch --stream benchr3 --consumer bench-pull --clients 1 --msgs 20000 --acks explicit --no-progress
```

### Результаты прогона автора

| Вариант | msgs/sec | Латентность fetch |
|---|---|---|
| Pull-consumer, R1-стрим (`01-jetstream`) | 83,088 | 12.04 µs |
| Pull-consumer, R3-стрим (`02-cluster`) | 54,289 | 18.42 µs |

Чтение backlog заметно быстрее publish в обоих случаях (десятки тысяч msgs/sec против единиц тысяч) — оно не проходит через RAFT-commit, только читает уже реплицированные данные с локальной ноды. R3 всё равно медленнее R1 и здесь — вероятно, накладные расходы на поддержание кворума/heartbeat на фоне чтения плюс разница в железе под конкретным прогоном; для точной атрибуции нужен изолированный бенчмарк без фоновой нагрузки, чего этот демо-стенд не гарантирует. Не делайте далеко идущих выводов из разницы 83K/54K — воспроизводите на своём стенде.

---

## Как мерить под свою нагрузку

1. Меняйте `--size` под реальный размер сообщений вашего сервиса — throughput в msgs/sec и в МБ/сек ведут себя по-разному на маленьких (128 B) и больших (16 KB+) сообщениях.
2. Меняйте `--clients` — одиночный synchronous publisher почти никогда не отражает реальную нагрузку многопоточного или многоинстансового сервиса.
3. Прогоняйте `sync` и `async` отдельно — `sync` даёт честную latency на операцию, `async` — предел throughput при конвейеризации; путать их — источник нереалистичных ожиданий.
4. Для JetStream — прогоняйте на **той топологии, которая пойдёт в прод** (R1 для некритичных данных, R3 для durable), а не экстраполируйте с R1 на R3 вручную.
5. Держите бенчмарк-клиент (`nats bench`) на отдельной машине/контейнере от сервера, если меряете что-то ближе к продакшн-числам — на одном ноутбуке CPU и диск делят между собой сервер, бенчмарк-клиент и Docker Desktop, и это тоже часть оговорки «иллюстрация, не обещание».
