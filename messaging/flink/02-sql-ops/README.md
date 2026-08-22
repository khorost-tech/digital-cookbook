# 02-sql-ops — Flink SQL, джойн и savepoint/rescale

Демо к статье [«Flink SQL, коннекторы и эксплуатация»](https://khorost.tech/messaging/flink-sql-connectors-operations/).

Стенд: Flink + Kafka. Показывает стриминговый **interval join** двух Kafka-топиков на Flink SQL
и эксплуатационную операцию — **savepoint + rescale** (смена параллелизма) без потери состояния.

## Запуск

```bash
docker compose up -d --build
```

Dashboard: <http://localhost:8081>.

## Наполнить топики и запустить джойн

```bash
docker compose exec jobmanager ./bin/sql-client.sh
```

В одной сессии SQL client:

1. [`sql/feeders.sql`](sql/feeders.sql) — создаёт таблицы `orders`/`payments` и два фидер-джоба
   (`datagen` → Kafka-топики `orders` и `payments`).
2. [`sql/join.sql`](sql/join.sql) — interval join по `order_id` в окне ±1 минута → топик `orders-enriched`
   (exactly-once).

Проверить результат:

```bash
docker compose exec kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server kafka:9092 --topic orders-enriched --from-beginning \
  --isolation-level read_committed
```

## Savepoint и rescale

Джоба джойна держит состояние обеих сторон в окне. Изменим параллелизм, не потеряв его.

Узнать JobID (джоба джойна) и снять savepoint с остановкой:

```bash
docker compose exec jobmanager ./bin/flink list
docker compose exec jobmanager ./bin/flink stop --savepoint file:///tmp/flink-savepoints <JOB_ID>
```

Команда выведет путь сохранённого savepoint, например `file:///tmp/flink-savepoints/savepoint-xxxx`.

Перезапустить джойн с бОльшим параллелизмом из savepoint — в SQL client:

```sql
SET 'execution.savepoint.path' = 'file:///tmp/flink-savepoints/savepoint-xxxx';
SET 'parallelism.default' = '4';
-- заново выполнить ТОЛЬКО INSERT INTO enriched из join.sql
```

Повторять `CREATE TABLE` не нужно и нельзя: в той же сессии таблица уже есть, и команда
упадёт с `TableAlreadyExistException`. Если же сессия SQL client была закрыта, каталог
пропал вместе с ней (он in-memory) — тогда сначала пересоздайте `orders`, `payments` и
`enriched`, но **не** запускайте фидер-джобы второй раз.

Джоба поднимется с восстановленным состоянием окна, но уже на 4 слотах.
В `orders-enriched` не будет ни потерь, ни дублей на стыке (exactly-once + savepoint):
на живом прогоне после rescale в топике оказалось 755 сообщений — ровно 755 уникальных
`order_id` подряд, с 1 по 755, без пропусков и повторов.

> Фидер-джобы можно оставить работающими — они независимы от джобы джойна.

## Что попробовать

- **Backpressure.** Поднимите `rows-per-second` фидеров до сотен и уменьшите слоты TaskManager —
  в Dashboard (вкладка джобы) операторы окрасятся в backpressure; посмотрите, где именно копится.
- **Regular join vs interval join.** Уберите условие по `ts` — получится regular join, который держит
  **всё** состояние обеих сторон вечно (растущее состояние); interval join ограничивает окном.
- **CDC вместо datagen.** В проде источником одной из сторон часто выступает CDC (Debezium):
  таблица в Flink SQL объявляется так же, но данные приходят как changelog реальной БД.
  Как устроен захват изменений — статья [CDC из Postgres в Kafka](https://khorost.tech/messaging/cdc-debezium-postgres-kafka/).

## Остановка

```bash
docker compose down -v
```
