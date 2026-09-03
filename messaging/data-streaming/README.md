# Стриминг и обработка данных: сквозной пайплайн

Companion к серии «Стриминг и обработка данных» на khorost.tech.

Одна цепочка целиком: `PG → Kafka Connect (Debezium) → Kafka → Kafka Streams → Go-консьюмер → ClickHouse`.
Серия показывает **сборку** — швы между звеньями и композицию гарантий; механики отдельных
систем разобраны в соседних сериях (Kafka, Flink, WAL, ClickHouse) и здесь не пересказываются.

## Запуск

Порядок ниже — обязательный, не косметический: `orders.events` должен получить 3
партиции ДО того, как коннектор впервые в него запишет, а `customer.totals` —
ровно 1 партицию ДО того, как в него начнёт писать Kafka Streams (иначе брокер
создаст топик автосозданием с дефолтным числом партиций — см. комментарий в
`scripts/create-topics.sh`: для `orders.events` это ломает демонстрацию
co-partitioning, для `customer.totals` — молча ломает дедуп-доказательство в
ClickHouse, если дефолт когда-нибудь окажется не 1).

Команды ниже соединены через `&&` намеренно, а не построчно: если
`create-topics.sh` упадёт, `curl` НЕ должен зарегистрировать коннектор — иначе
Debezium создаст `orders.events` автосозданием (1 партиция) прежде, чем топик
будет пересоздан явно, и дефект партиционирования вернётся молча.

```bash
(cd compose && docker compose up -d) \
  && bash scripts/create-topics.sh \
  && curl -s -X POST -H "Content-Type: application/json" \
       --data @connect/debezium-outbox.json localhost:8084/connectors
```

Порты: PostgreSQL `5455`, Kafka `9096`, Kafka Connect `8084`, ClickHouse `8125`/`9001`.

Дальше — обязательные шаги, без которых цепочка не полная (см. `FIXTURES.md` —
все числа ниже сняты именно в таком порядке, живьём):

### 1. Наполнить магазин демо-данными

```bash
bash scripts/seed.sh 50   # N заказов (по умолчанию 50); заказ + событие outbox — одной транзакцией
```

Проверить, что событие дошло до Kafka: `docker exec ds-kafka /opt/kafka/bin/kafka-get-offsets.sh --bootstrap-server localhost:9092 --topic orders.events`.

### 2. Запустить Kafka Streams

Kafka Streams — библиотека внутри процесса, не отдельный сервис в `compose.yml`
(это часть демонстрации: обработка стрима без выделенного кластера). Поднимается
вручную, отдельным процессом, требуется JDK 21+:

```bash
cd streams && KAFKA=localhost:9096 mvn -q compile exec:java
```

**Важно:** именно `KAFKA=host:port mvn ...` (переменная окружения ПЕРЕД командой),
**НЕ** `mvn ... -DKAFKA=host:port`. `StreamsApp.env()` читает
`System.getenv("KAFKA")`, а `exec-maven-plugin` 3.5.0 не пробрасывает Java
system-свойства (`-D...`) в окружение запускаемого `main` — `-DKAFKA=...` будет
молча проигнорирован (совпадёт с дефолтом `localhost:9096` только случайно).

Приложение пишет состояние в локальный RocksDB (`STATE_DIR`, по умолчанию
`/tmp/ds-streams`) и дублирует его в changelog-топик
`ds-streams-customer-totals-store-changelog` — при падении/рестарте состояние
восстанавливается из этого топика (см. `FIXTURES.md` §2, живой прогон с
`rm -rf` state dir и рестартом).

Процесс не демонизирован — это разовый долгоживущий процесс для демонстрации,
останавливается `Ctrl+C` или `kill`.

### 3. Прогнать Go-консьюмер (customer.totals -> ClickHouse)

```bash
cd consumer
go run . -brokers localhost:9096 -ch localhost:9001          # обычный прогон
go run . -from-start -for 20s                                 # backfill: перечитать топик с начала
go run . -dup -from-start -for 20s                             # имитация at-least-once (повторная доставка)
```

Консьюмер коммитит оффсеты вручную, ПОСЛЕ того как пачка реально долетела до
ClickHouse — не по таймеру, и читает `customer.totals` с
`kgo.FetchIsolationLevel(kgo.ReadCommitted())` — источник транзакционный
(Kafka Streams пишет с `EXACTLY_ONCE_V2`), `read_committed` защищает от
видимости данных прерванной (aborted) транзакции. Дедуп на стоке — через
`ReplacingMergeTree(version)` + модификатор `FINAL` при чтении (см.
`sql/02-clickhouse.sql` и `FIXTURES.md` §3): `customer_totals` и `raw_totals`
физически получают ОДНИ И ТЕ ЖЕ строки на вставке, разницу даёт `FINAL`, не
движок сам по себе (`SELECT ... FROM raw_totals FINAL` — `Code: 181,
ILLEGAL_FINAL`, "тот же запрос, другой движок" технически невозможен). Это
верно на вставке, но не постоянно: `ReplacingMergeTree` гасит дубли
АСИНХРОННО фоновым merge, `MergeTree` (`raw_totals`) — никогда; счётчик строк
без `FINAL` может разойтись между таблицами уже через несколько минут, как
только фоновый merge отработает (живой пример — `FIXTURES.md` §3). Читать
`customer_totals` нужно всегда через `FINAL`/`argMax`, а не полагаться на
состояние таблицы в моменте.

**Важно про доказательную силу §3:** `customer_totals`/`raw_totals` — это
changelog НАКОПИТЕЛЬНЫХ снимков состояния клиента, а не независимых событий.
Наивная `sum(total)` по ним завышена уже в baseline, без единого дубля —
это измеряет семантику ЧТЕНИЯ changelog (нужен `argMax`/`FINAL`), а не
эффект дублей. Прямое доказательство эффекта дубля (`2N` против `N` на одном
и том же входе) — изолированный эксперимент на `orders.events`
(`ds.order_counts_raw`/`ds.order_dedup`, см. `FIXTURES.md` §3.1):

```bash
go run . -mode dedup-demo -from-start -for 15s   # baseline: N событий
go run . -mode dedup-demo -from-start -for 15s   # replay: order_counts_raw → 2N, order_dedup FINAL → остаётся N
```

### 4. Демонстрации

Порядок ниже — рекомендованный (в этом порядке артефакты сняты в `FIXTURES.md`).
Единственная жёсткая зависимость: **`scripts/inject-bad.sh` + прогон консьюмера
после него должны доехать до конца ДО `scripts/schema-evolve.sh`** — иначе
собственная проверка `schema-evolve.sh` (ожидает `битых=0`) упадёт на
непрочитанном битом сообщении из предыдущей демонстрации, уже ПОСЛЕ того как
`schema-evolve.sh` необратимо вставит новый заказ в PG.

```bash
bash scripts/lag.sh              # лаг по звеньям (ds-streams, ds-sink)
bash scripts/inject-bad.sh       # битое сообщение -> DLQ (после — прогнать консьюмер)
bash scripts/schema-evolve.sh    # новое поле (currency) проходит сквозь цепочку без искажений
```

**Все три скрипта ниже — НЕОБРАТИМЫ на живом стенде:**

- `scripts/inject-bad.sh` кладёт заведомо непарсимую запись в `customer.totals`
  напрямую (Kafka не умеет удалять отдельные сообщения) — после первого запуска
  "чистый" baseline (битых=0) без полного `docker compose down -v` не
  воспроизвести; каждый повторный `go run . -from-start` находит эту же запись
  заново.
- `scripts/schema-evolve.sh` каждый запуск добавляет один заказ в PG
  (`customer_id=3`) и одну фантомную строку `customer_id=999` в
  `customer_totals` (синтетический пробник устойчивости Go-консьюмера к
  незнакомым JSON-полям) — тоже без отката без `down -v`.
- Полная пересборка стенда для чистого повтора всех демонстраций:
  `docker compose down -v && docker compose up -d`, затем заново весь путь
  выше (create-topics → регистрация коннектора → seed → Streams → консьюмер).

### Особенности, о которых важно знать заранее

- **`LAG=1` у группы `ds-sink` на `customer.totals` — это НЕ отставание.**
  Kafka Streams пишет в `customer.totals` с `PROCESSING_GUARANTEE=EXACTLY_ONCE_V2`:
  транзакционный продюсер кладёт в топик служебные control-записи (маркеры
  commit/abort), которые занимают оффсет, но не являются сообщениями — обычный
  консьюмер (в т.ч. `ds-sink`) их не читает и никогда не "дочитает" этот
  последний оффсет. `LAG=1` в состоянии покоя — ожидаемый артефакт
  EXACTLY_ONCE_V2, не путать с реальным растущим отставанием (см. `FIXTURES.md` §5).
- **Нечистая остановка Kafka Streams (`kill -9`) во время активной
  EXACTLY_ONCE_V2-транзакции может привести к `TaskCorruptedException` при
  следующем запуске.** Приложение обнаруживает это само и самовосстанавливается
  (переинициализирует таск, заново восстанавливает состояние из changelog) без
  вмешательства — живое наблюдение, см. `FIXTURES.md` («Особенности окружения»).
- `orders.events` — **обязательно 3 партиции**, `customer.totals` — **обязательно
  1 партиция**, ОБЕ должны существовать ДО первого обращения (см.
  `scripts/create-topics.sh` и комментарий выше про порядок запуска) — иначе
  брокер создаст их автосозданием с дефолтным числом партиций, что молча ломает
  либо демонстрацию co-partitioning в Kafka Streams, либо корректность
  `version=offset` в `ReplacingMergeTree` (см. `sql/02-clickhouse.sql`).
