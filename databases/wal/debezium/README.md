# Debezium Embedded Engine (без Kafka) — читает PG WAL

Минимальный пример: Debezium Embedded Engine поднимается **внутри одного JVM-процесса**,
без Kafka и Kafka Connect. Читает logical replication из PostgreSQL (`wal_level=logical`,
плагин `pgoutput`) и печатает декодированные change-события (JSON) в stdout.

Это индустриальный аналог "сырого" чтения replication-слота на Go (см. `../logical/go`) —
та же WAL-механика, но поверх готового коннектора с собственным снапшотом,
схемой и форматом событий.

## Версии

- Debezium **3.6.0.Final** (latest стабильный 3.x на Maven Central на момент проверки, 2026-07).
  Бриф предлагал запинить `3.0.0.Final` — обновлено до фактического latest.
- JDK 21, Maven 3.9.9 (exec-maven-plugin 3.5.0).

## API и адаптации

Использован канонический паттерн Embedded Engine:

```java
DebeziumEngine<ChangeEvent<String, String>> engine = DebeziumEngine.create(Json.class)
    .using(props)
    .notifying(record -> System.out.println(record.value()))
    .build();
```

Этот паттерн (`Json.class` формат + `notifying(Consumer<ChangeEvent<String,String>>)` +
`record.value()`) не менялся между Debezium 2.x и 3.x — адаптаций классов/имён
относительно канонического образца не потребовалось. Изменена только
версия артефактов (3.6.0.Final вместо 3.0.0.Final).

Конфигурация (`Properties`, без Kafka):

| Свойство | Значение | Смысл |
|---|---|---|
| `connector.class` | `io.debezium.connector.postgresql.PostgresConnector` | коннектор PG |
| `offset.storage` | `FileOffsetBackingStore` | offset без Kafka, локальный файл |
| `schema.history.internal` | `FileSchemaHistory` | история схемы, локальный файл |
| `plugin.name` | `pgoutput` | logical decoding plugin (встроен в PG, без расширений) |
| `slot.name` | `debezium_slot` | отдельный слот, не конфликтует с `wal_slot` из стенда `../logical/` |
| `publication.autocreate.mode` | `filtered` | публикация создаётся автоматически под `table.include.list` |
| `table.include.list` | `public.orders` | та же таблица, что в `../logical/` |

## Сеть: контейнерный путь (host заблокирован)

Как и в `../logical/`, host-firewall блокирует TCP от host-процессов к опубликованному
`localhost:5433`, хотя контейнер жив и `psql` изнутри работает. Поэтому Maven/Java
запускается **в контейнере**, подключённом к сети стенда `postgres_default`,
с `database.hostname=postgres` (имя сервиса в compose) и `database.port=5432`
(внутренний порт контейнера, не опубликованный 5433).

Offset/schema-history файлы (`/tmp/dbz-offsets.dat`, `/tmp/dbz-schema-history.dat`)
находятся **внутри контейнера** — эфемерны между запусками.

## Запуск

```bash
cd digital-cookbook/wal/postgres
docker compose up -d
docker compose exec -T postgres psql -U postgres -d waldemo \
  -c "CREATE TABLE IF NOT EXISTS orders(id bigserial primary key, amount numeric, status text);"

cd ../debezium
docker run --rm --network postgres_default \
  -v "$PWD":/app -v "$HOME/.m2":/root/.m2 -w /app \
  maven:3.9-eclipse-temurin-21 \
  sh -c "mvn -q compile && timeout 40 mvn -q exec:java -Ddatabase.hostname=postgres -Ddatabase.port=5432"
```

Пока движок стримит — из другого терминала (или после старта) выполнить в БД:

```bash
docker compose exec -T postgres psql -U postgres -d waldemo \
  -c "INSERT INTO orders(amount,status) VALUES (100,'new'); UPDATE orders SET status='paid' WHERE id=1;"
```

## Пример реального вывода

Старт движка (после первичного снапшота):

```
Debezium embedded engine started, streaming changes from orders...
```

Change-событие после `INSERT` (`"op":"c"`, `schema` усечена, `payload` реальный из прогона):

```
CHANGE-EVENT: {"schema":{...},"payload":{"before":null,"after":{"id":1,"amount":{"scale":0,"value":"ZA=="},"status":"new"},"source":{"version":"3.6.0.Final","connector":"postgresql","name":"wal","ts_ms":1783373121441,"snapshot":"false","db":"waldemo","schema":"public","table":"orders","txId":768,"lsn":34116352},"transaction":null,"op":"c","ts_ms":1783373121885}}
```

Change-событие после `UPDATE` (`"op":"u"`, `schema` усечена, `payload` реальный из прогона):

```
CHANGE-EVENT: {"schema":{...},"payload":{"before":null,"after":{"id":1,"amount":{"scale":0,"value":"ZA=="},"status":"paid"},"source":{"version":"3.6.0.Final","connector":"postgresql","name":"wal","ts_ms":1783373121441,"snapshot":"false","db":"waldemo","schema":"public","table":"orders","txId":768,"lsn":34116584},"transaction":null,"op":"u","ts_ms":1783373121899}}
```

Примечание: `before:null` в UPDATE — ожидаемо при `REPLICA IDENTITY DEFAULT` (по умолчанию), PG не пишет старые значения не-ключевых полей в WAL. `amount` сериализован как `VariableScaleDecimal` ({"scale":0,"value":"ZA=="} — base64 от `100`), это штатное поведение Debezium для `numeric` без фиксированного scale.

Полный лог реального прогона снят при сборке стенда.

## Остановка

```bash
cd ../postgres
docker compose down
```
