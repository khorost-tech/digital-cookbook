# Demo-стенд: Отказоустойчивый кластер RabbitMQ 4.x

Рабочий стенд к серии статей [«Высокодоступный RabbitMQ»](https://khorost.tech/messaging/rabbitmq-ha-cluster-quorum-failover/) на khorost.tech.

Стенд показывает в действии quorum-очереди, автоматический failover, Dead Letter Queue,
federation и — для контраста — legacy classic mirrored queues на RabbitMQ 3.13.

---

## Содержание

1. [Требования](#требования)
2. [Запуск основного кластера](#запуск-основного-кластера)
3. [Доступы](#доступы)
4. [Сборка и запуск Go-клиентов](#сборка-и-запуск-go-клиентов)
5. [Порядок демонстраций](#порядок-демонстраций)
6. [Быстрая проверка всего стенда (smoke.sh)](#быстрая-проверка-всего-стенда-smokesh)
7. [Мониторинг (Grafana)](#мониторинг-grafana)
8. [Troubleshooting](#troubleshooting)
9. [Очистка](#очистка)

---

## Проверено на

| Компонент | Версия |
|-----------|--------|
| RabbitMQ | 4.3 |
| Docker Engine | 29.x+ |
| Docker Compose | v2 (`docker compose`) |
| Go | 1.22+ |

## Требования

| Компонент | Версия |
|-----------|--------|
| Docker Engine | 29.x+ |
| Docker Compose | v2 (`docker compose`) |
| Go | 1.22+ |

**Свободные порты:**

| Нода | AMQP | Management UI | Prometheus |
|------|------|---------------|------------|
| rabbit1 | 5672 | 15672 | 15692 |
| rabbit2 | 5673 | 15673 | — |
| rabbit3 | 5674 | 15674 | — |
| rabbit-legacy (3.13) | 5680 | 15680 | — |

---

## Запуск основного кластера

```bash
# Из папки ha-cluster:
docker compose up -d
```

Healthcheck срабатывает примерно за **40 секунд**. Проверить, что все три ноды в кластере:

```bash
docker exec rabbit1 rabbitmqctl cluster_status
```

В выводе в секции `Running Nodes` должны быть `rabbit@rabbit1`, `rabbit@rabbit2`, `rabbit@rabbit3`.

---

## Доступы

- **Management UI:** http://localhost:15672
- **Логин:** `demo` / **Пароль:** `demo`

Дополнительно rabbit2 и rabbit3 доступны на портах 15673 и 15674 соответственно.

---

## Сборка и запуск Go-клиентов

```bash
cd go
go mod tidy
go build ./...
```

### Producer

```bash
go run ./cmd/producer -n 100
```

Флаги:

| Флаг | По умолчанию | Описание |
|------|-------------|----------|
| `-urls` | `amqp://demo:demo@localhost:5672/,amqp://demo:demo@localhost:5673/,amqp://demo:demo@localhost:5674/` | AMQP узлы кластера через запятую |
| `-n` | `100` | Число отправляемых сообщений |
| `-confirms` | `true` | Publisher confirms — ждёт broker ack перед переходом к следующему сообщению; **при `-confirms=false` потери возможны** |
| `-queue` | `demo.orders` | Имя очереди (она же routing key) |

### Consumer

```bash
go run ./cmd/consumer
```

Флаги:

| Флаг | По умолчанию | Описание |
|------|-------------|----------|
| `-urls` | `amqp://demo:demo@localhost:5672/,amqp://demo:demo@localhost:5673/,amqp://demo:demo@localhost:5674/` | AMQP узлы кластера через запятую |
| `-queue` | `demo.orders` | Имя очереди |
| `-prefetch` | `10` | QoS prefetch count |
| `-autoack` | `false` | **`false` — manual ack (at-least-once, безопасно)**; `true` — авто-ack сразу при доставке, при падении процесса сообщения теряются |
| `-nack` | `false` | Симулировать падение обработчика: получить сообщение и закрыть соединение без ack → quorum растит `x-delivery-count` → после `delivery-limit=5` redelivery сообщение уходит в `demo.dlx` → `demo.dlq` |

> **Контраст надёжности:** запустите consumer с `-autoack=false` (по умолчанию) и `-autoack=true` по очереди.
> При failover с manual ack сообщения не теряются; с auto-ack — теряются те, что были «в пути».

---

## Порядок демонстраций

### 1. Кластер и quorum-очереди

Убедитесь, что кластер запущен (`cluster_status` — три ноды). Откройте Management UI
(http://localhost:15672) → вкладка Queues. После первого запуска producer очередь `demo.orders`
появится автоматически как `quorum`.

**Важный нюанс RabbitMQ 4.x:** тип очереди по умолчанию задаётся на уровне vhost
(`default_queue_type=quorum` в `cluster/definitions.json`). Попытка управлять типом очереди
через policy-ключ `queue-type` в 4.x **невалидна** — используйте только аргумент
`x-queue-type` при объявлении очереди или vhost-умолчание.

### 2. Failover (интерактивный скрипт)

> **Внимание:** скрипт интерактивен — он ждёт ваших нажатий Enter и **принудительно убивает
> ноду-лидера** quorum-очереди командой `docker kill`. Перед запуском убедитесь, что producer
> и consumer работают в соседних терминалах.

**Шаг 1.** В терминале 1 запустите consumer:

```bash
cd go && go run ./cmd/consumer
```

**Шаг 2.** В терминале 2 запустите producer:

```bash
cd go && go run ./cmd/producer -n 200
```

**Шаг 3.** В терминале 3 запустите скрипт failover:

```bash
bash scripts/failover.sh
```

Скрипт:
1. Покажет текущего лидера quorum-очереди `demo.orders`.
2. Убьёт ноду-лидера (`docker kill <container>`).
3. Подождёт 5 секунд и покажет нового лидера.

Наблюдайте: consumer в терминале 1 автоматически переподключается и продолжает получать
сообщения без потерь.

> **Важно — клиент должен знать ВСЕ узлы кластера.** Флаг `-urls` по умолчанию содержит все
> три эндпоинта (5672/5673/5674). Подключение только к одному узлу (`localhost:5672`) делает
> его единственной точкой отказа (SPOF): при падении этого узла клиент зациклится на
> «connection refused» и не переключится на выживших. Решение — список узлов в клиенте
> (как в этом стенде) или балансировщик типа HAProxy перед кластером.

**Неинтерактивный режим:** передайте `AUTO=1` перед скриптом, чтобы пропустить паузы:

```bash
AUTO=1 bash scripts/failover.sh
```

### 3. Отложенная доставка через retry-queue

```bash
bash scripts/retry-demo.sh
```

Неинтерактивный режим:

```bash
AUTO=1 bash scripts/retry-demo.sh
```

**Схема:** `demo.orders` → *(reject без requeue)* → `demo.dlx` → `demo.retry` *(quorum, TTL 5 сек)* → `demo.orders`

Механизм:
1. Consumer делает **nack без requeue** — симулирует временный сбой обработчика.
2. Брокер пересылает сообщение в `demo.dlx` (dead-letter-exchange из политики `dlx-orders`).
3. `demo.dlx` — fanout; его единственный подписчик — `demo.retry` (quorum-очередь с `x-message-ttl=5000`).
4. Через 5 секунд TTL истекает, и `demo.retry` dead-letter'ит сообщение обратно в default exchange с routing-key `demo.orders`.
5. Сообщение снова появляется в `demo.orders` — готово к повторной попытке.

> **Отличие от DLQ-паттерна:** в DLQ сообщение после `delivery-limit` вынимается из основного потока навсегда.
> В retry-queue сообщение автоматически возвращается к повторной обработке с задержкой, что позволяет
> пережить временные сбои (недоступность базы данных, внешнего API и т.д.).

> **Нюанс петли (обучающий момент):** `demo.dlx` — fanout, и **единственный** его подписчик —
> `demo.retry`. Если `delivery-limit=5` исчерпан, а очередь `demo.dlq` к `demo.dlx` не привязана,
> сообщение после истечения TTL вернётся в `demo.orders`, будет отклонено снова и попадёт
> обратно в `demo.retry` — потенциальная бесконечная петля. В production к `demo.dlx` привязывают
> как `demo.retry`, так и `demo.dlq`, чтобы сообщение после превышения лимита уходило в DLQ.

> **Изоляция сценариев:** скрипты `dlq-demo.sh` и `retry-demo.sh` конкурируют за подписчиков
> `demo.dlx`. Запускайте их раздельно, выполняя между прогонами `docker compose down -v` для
> сброса привязок и состояния очередей.

Проверить циркуляцию сообщения:

```bash
docker exec rabbit1 rabbitmqctl list_queues name messages | grep demo
```

### 4. Native Delayed Retry (RabbitMQ 4.3)

```bash
bash scripts/delayed-retry-demo.sh
```

Неинтерактивный режим:

```bash
AUTO=1 bash scripts/delayed-retry-demo.sh
```

**Механизм:** RabbitMQ 4.3 добавил нативную поддержку отложенного redelivery прямо в quorum-очереди — через аргументы `x-delayed-retry-type`, `x-delayed-retry-min`, `x-delayed-retry-max` или одноимённые ключи политики.

**Схема задержки:** `delay = min(min_delay × delivery_count, max_delay)`

Скрипт применяет policy с `delayed-retry-type=all`, `delayed-retry-min=2000` (2 сек), `delayed-retry-max=10000` (10 сек) на очередь `demo.orders`, публикует сообщение и симулирует implicit nack (закрытие AMQP-соединения без ack). Quorum-очередь откладывает повторную доставку: ~2 сек после первого нака, ~4 сек после второго, ~6 сек после третьего.

> **Важно:** нативный delayed retry срабатывает при **implicit nack** (закрытие соединения или канала без ack). Явный `Basic.Nack(requeue=false)` — это dead-lettering, а **не** delayed retry.

**Отличие от TTL-подхода (`retry-demo.sh`):**

| | TTL-retry-queue | Native delayed retry (4.3) |
|---|---|---|
| Требует дополнительных очередей | Да (`demo.retry`, `demo.dlx`) | Нет — всё внутри одной очереди |
| Нарастающая задержка | Нет (фиксированный TTL) | Да (`delay = min × delivery_count`) |
| Версия RabbitMQ | Любая | 4.x (quorum queues) |
| Переносимость | Высокая | Только RabbitMQ 4.x+ |

`retry-demo.sh` остаётся как демонстрация переносимого паттерна; `delayed-retry-demo.sh` — нативный механизм 4.3.

### 5. Dead Letter Queue (DLQ)

```bash
bash scripts/dlq-demo.sh
```

Скрипт создаёт очередь `demo.dlq` (тип `quorum`) и привязывает её к exchange `demo.dlx`.
Политика `dlx-orders` уже задана в `cluster/definitions.json`:
`dead-letter-exchange=demo.dlx`, `delivery-limit=5`. После 5 неудачных доставок (nack без
requeue) сообщение автоматически уходит в `demo.dlq`.

Проверить количество сообщений в очередях:

```bash
docker exec rabbit1 rabbitmqctl list_queues name messages | grep demo
```

### 6. Federation

```bash
bash scripts/federation-demo.sh
```

Скрипт настраивает federation-upstream `demo-upstream` (ссылается на `rabbit1`) и политику
федерации на exchange `demo.orders`. В демо upstream и downstream — один и тот же кластер
(для наглядности механизма); в реальном сценарии второй кластер поднимается в отдельной
Docker-сети.

Статус federation-links — в Management UI http://localhost:15672 или:

```bash
docker exec rabbit1 rabbitmqctl list_parameters
docker exec rabbit1 rabbitmqctl eval 'rabbit_federation_status:status().'
```

### 7. Legacy: classic mirrored queues на RabbitMQ 3.13

**Шаг 1.** Поднять legacy-стенд (один контейнер `rabbit-legacy`):

```bash
docker compose -f docker-compose.mirrored.yml up -d
```

Management UI legacy: http://localhost:15680 (логин `demo` / `demo`).

**Шаг 2.** Применить политику ha-all и создать очередь:

```bash
bash scripts/mirrored-demo.sh
```

Скрипт применяет политику `ha-all` (зеркалирование на все ноды) и создаёт тестовую очередь
`legacy.q`. Используется `rabbitmqadmin` v1 (старый синтаксис с позиционными аргументами
`name=...`).

> **Отличие от 4.x:** в RabbitMQ 4.x `ha-mode` в политиках **игнорируется** —
> `rabbitmqadmin` v2 использует принципиально другой синтаксис (`--name`, `--type`,
> `--username/--password`). Скрипт `dlq-demo.sh` написан под v2; `mirrored-demo.sh` — под v1.

---

## Быстрая проверка всего стенда (smoke.sh)

Чтобы проверить весь стенд одной командой (неинтерактивно):

```bash
bash scripts/smoke.sh
```

Скрипт выполняет полный цикл: `docker compose up -d` → ожидание healthcheck (~45 сек) →
проверка 3 нод в кластере → producer (-n 20) + consumer → `retry-demo.sh` (AUTO) →
`delayed-retry-demo.sh` (AUTO) → `docker compose down -v`.

---

## Мониторинг (Grafana)

Стенд включает Prometheus + Grafana для наблюдения за кластером в реальном времени, особенно во время failover-демо.

### Запуск

Prometheus и Grafana поднимаются вместе с основным кластером:

```bash
docker compose up -d
```

Или отдельно, если кластер уже запущен:

```bash
docker compose up -d prometheus grafana
```

### Доступы

| Сервис | URL | Логин |
|--------|-----|-------|
| **Grafana** | http://localhost:3000 | без логина (anonymous Admin) |
| **Prometheus** | http://localhost:9090 | — |
| **RabbitMQ metrics** | http://localhost:15692/metrics | — |

### Дашборд «RabbitMQ — Failover demo»

Дашборд загружается автоматически при старте Grafana. Откройте http://localhost:3000 → Dashboards → «RabbitMQ — Failover demo».

**Что смотреть во время failover** (`bash scripts/failover.sh`):

| Панель | Что показывает | Ожидаемое поведение при failover |
|--------|---------------|----------------------------------|
| **Живых нод** (stat, зелёный/красный) | Число нод RabbitMQ, которые Prometheus видит UP | Падает **3 → 2**, затем возвращается к 3 после восстановления |
| **Глубина очереди demo.orders** (timeseries) | Ready + Unacked + Total | **Не обнуляется** — данные не теряются |
| **Publish rate** (timeseries) | Скорость публикации msg/s | Кратковременный спад на 1–3 сек при переизбрании лидера, затем восстановление |
| **Deliver/Ack rate** (timeseries) | Скорость доставки с подтверждением msg/s | Аналогично — без длительного провала |
| **Unacked messages** (timeseries) | Сообщения «в пути» у consumer'а | Всплеск при reconnect (consumer переподключается), затем спад до нуля |

### Конфигурация мониторинга

Файлы лежат в `monitoring/`:
- `prometheus.yml` — скрейп rabbit1, rabbit2, rabbit3 каждые 5 секунд
- `grafana/provisioning/` — автонастройка datasource и дашборда
- `grafana/dashboards/rabbitmq-failover.json` — JSON дашборда

Метрики берутся из плагина `rabbitmq_prometheus` (порт 15692). В `cluster/rabbitmq.conf` включён `prometheus.return_per_object_metrics = true` — метрики детализированы по каждой очереди.

---

## Troubleshooting

### Ноды не кластеризуются

Все ноды должны использовать один и тот же Erlang cookie. В этом стенде он задан через
переменную окружения `RABBITMQ_ERLANG_COOKIE: "DEMOCOOKIE0123456789"` в `docker-compose.yml`.
Файл `.erlang.cookie` для справки (не монтируется в контейнеры напрямую).

Проверить, что ноды видят друг друга:

```bash
docker exec rabbit1 rabbitmqctl cluster_status
```

### Кластер не принимает сообщения после потери ноды

Это ожидаемое поведение. Quorum-очереди и Khepri-метастор требуют доступного **большинства
нод (Raft majority)**. При потере большинства (например, 2 из 3) оставшаяся нода не
обслуживает запросы к quorum-очередям и метаданным кластера — это автоматически,
без дополнительной настройки.

> **RabbitMQ 4.3:** параметр `cluster_partition_handling` (в т.ч. `pause_minority`)
> в 4.x **не действует** — он принимается без ошибки, но игнорируется. Поведение
> при потере кворума полностью определяется Raft-консенсусом, конфигурировать его
> отдельно не нужно.

Верните в строй ≥ 2 ноды (из 3) — кластер автоматически возобновит работу.

### Скрипты .sh не запускаются / ошибка CRLF

Файл `.gitattributes` содержит `*.sh text eol=lf`, что обеспечивает корректные окончания
строк при clone/checkout. Если скрипты всё равно выдают ошибку `\r: command not found`,
конвертируйте вручную:

```bash
dos2unix scripts/failover.sh
```

Или запускайте из **Git Bash** (не из PowerShell / cmd.exe).

### Порты уже заняты

Измените маппинг портов в `docker-compose.yml` (или `docker-compose.mirrored.yml`).
Например, замените `"5672:5672"` на `"5682:5672"` — и обновите флаг `-urls` в Go-командах,
например, запускайте с `-urls amqp://demo:demo@localhost:5682/` (или перечислите все узлы через запятую).

---

## Очистка

```bash
# Основной кластер (удалить контейнеры и volumes):
docker compose down -v

# Legacy-стенд:
docker compose -f docker-compose.mirrored.yml down -v
```
