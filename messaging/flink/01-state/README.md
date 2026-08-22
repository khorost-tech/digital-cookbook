# 01-state — состояние, checkpoints и exactly-once

Демо к статье [«Состояние и exactly-once в Flink»](https://khorost.tech/messaging/flink-state-exactly-once/).

Стенд: Flink (state backend **RocksDB**, чекпоинты каждые 10 c) + Kafka (KRaft).
Показывает, что состояние переживает падение TaskManager, а сток в Kafka — exactly-once.

## Запуск

```bash
docker compose up -d --build
```

`--build` нужен: образ Flink дособирается с Kafka SQL-коннектором (см. [`Dockerfile`](Dockerfile)).
Dashboard: <http://localhost:8081> — вкладка **Checkpoints** покажет успешные снимки.

## Запустить джобу

```bash
docker compose exec jobmanager ./bin/sql-client.sh
```

Выполнить блоки из [`sql/exactly-once.sql`](sql/exactly-once.sql): источник `orders`,
Kafka-сток `user_totals` с `sink.delivery-guarantee = exactly-once`, оконная агрегация
(состояние в RocksDB). Джоба появится в Dashboard, чекпоинты начнут проходить.

## Проверить вывод

Топик `user-totals` читается **только закоммиченными** сообщениями (`read_committed`):

```bash
docker compose exec kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server kafka:9092 --topic user-totals --from-beginning \
  --isolation-level read_committed
```

## Проверить восстановление состояния

Убить TaskManager на середине и посмотреть, что окна не задвоятся и не потеряются:

```bash
docker compose kill taskmanager
docker compose up -d taskmanager        # Flink перезапустит джобу с последнего чекпоинта
```

В Dashboard джоба уйдёт в `RESTARTING` → `RUNNING` с восстановленного чекпоинта.
В `user-totals` не появится дублей уже закоммиченных окон (exactly-once) — состояние окна
восстановлено из RocksDB-чекпоинта, а не пересчитано с нуля.

## Что попробовать

- **RocksDB vs heap.** В `docker-compose.yml` смените `state.backend.type: rocksdb` на `hashmap` —
  состояние уедет в heap TaskManager (быстрее, но ограничено RAM; для больших окон/ключей — OOM).
- **Инкрементальные чекпоинты.** `execution.checkpointing.incremental: true` (уже включено) — RocksDB
  пишет только дельту (см. размеры чекпоинтов в Dashboard); с `hashmap` инкрементальность недоступна.
- **at-least-once.** Смените `sink.delivery-guarantee` на `at-least-once` и повторите kill —
  в `user-totals` при чтении `read_uncommitted` увидите повторы.

## Остановка

```bash
docker compose down -v
```
