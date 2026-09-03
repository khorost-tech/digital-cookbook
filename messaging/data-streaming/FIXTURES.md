# FIXTURES — живые выводы стенда messaging/data-streaming

Все числа и логи ниже — из живого прогона на этой машине (WSL2 Ubuntu + Docker),
единым проходом ПОСЛЕ полной пересборки стенда (`docker compose down -v` → `up -d`)
и ПОСЛЕ ремедиации авторского ревью (read_committed, семантическая валидация,
честный комментарий про state store, изолированный дедуп-эксперимент — см.
раздел «Ремедиация» ниже). Источник фактов для серии «Стриминг и обработка
данных». Дата прогона: 2026-07-17.

Предыдущая редакция этого файла (тот же день, до ремедиации) считается
МЁРТВОЙ — переснята заново целиком после правок кода, ни одно число оттуда
не перенесено без повторного живого измерения.

## Ремедиация (что изменилось в коде перед этим прогоном)

1. **read_committed** (`consumer/main.go`, основной consumer-group клиент,
   читающий `customer.totals`) — было: дефолт franz-go `read_uncommitted`.
   `customer.totals` — транзакционный ВЫВОД Kafka Streams
   (`PROCESSING_GUARANTEE_CONFIG=EXACTLY_ONCE_V2`). `read_uncommitted` отдаёт
   записи прерванных (aborted) транзакций как обычные сообщения; при
   `kill -9` посреди активной EOS-транзакции (см. §5 ниже) это означало бы
   риск протащить в витрину данные, которых с точки зрения Kafka никогда не
   было. Добавлено `kgo.FetchIsolationLevel(kgo.ReadCommitted())`.
2. **Семантическая валидация** (`consumer/main.go`, после `json.Unmarshal`) —
   `{}` синтаксически валиден и анмаршалится в `customer_id=0, orders=0,
   total=0` без единой ошибки. Добавлена проверка `customer_id == 0` →
   трактуется как битая запись (DLQ), тем же путём, что и синтаксически
   невалидный JSON. Живая проверка — см. §6.
3. **Честный комментарий про повреждённый стор** (`streams/.../StreamsApp.java`,
   `TotalsProcessor`) — предыдущая редакция утверждала, что поведение
   treat-as-absent «самовосстанавливается». Это неверно: восстанавливается
   ПОТОК (перестаёт падать), а не ДАННЫЕ — накопленная история клиента
   теряется навсегда, заниженное значение уходит в changelog/витрину без
   дальнейшей коррекции. Комментарий переписан честно, назван правильный
   прод-ответ (fail-fast + пересоздание стора + replay из changelog, либо
   явная миграция формата). Это правка комментария, не поведения —
   собственного FIXTURES-раздела не требует.
4. **Изолированный дедуп-эксперимент** (`sql/02-clickhouse.sql`:
   `ds.order_counts_raw` + `ds.order_dedup`; `consumer/main.go`: режим
   `-mode dedup-demo`) — старый §3 (`customer_totals`/`raw_totals`)
   недостаточен как доказательство дедупа: это changelog НАКОПИТЕЛЬНЫХ
   снимков состояния клиента, наивная `sum(total)` завышена уже в baseline
   БЕЗ единого дубля. Новый эксперимент читает `orders.events` (события, не
   снимки) — см. §3.1.

## Ремедиация круга 2 (по второму авторскому ревью, без полного ре-рана)

Точечная правка кода + точечная пересъёмка §6 (новый кейс) + автотесты. §1–§5
и §7 НЕ переснимались — эти правки на их числа не влияют (см. обоснование
в каждом пункте). Живой стенд не пересоздавался (`down -v` не выполнялся) —
те же контейнеры, что и после круга 1.

1. **Fail-fast вместо тихого сброса состояния** (`streams/.../StreamsApp.java`,
   `TotalsProcessor.process()`) — предыдущая правка (круг 1, п.3 выше) только
   переписала комментарий, оставив поведение прежним: повреждённая/неполная
   запись стора трактовалась как отсутствующее состояние и накопление
   начиналось заново (`orders=1`), заниженный агрегат тихо уходил в
   store.put() → changelog → `customer.totals` → ClickHouse. Теперь вместо
   сброса бросается новое `StreamsApp.CorruptedStateStoreException`
   (`RuntimeException`) с точной причиной (какой customer, что не так).
   `streams.setUncaughtExceptionHandler` (см. `main()`) реагирует на неё
   `SHUTDOWN_CLIENT` (чистая однократная остановка), на любую другую
   необработанную ошибку stream-треда — по-прежнему `REPLACE_THREAD`
   (транзиентные инфраструктурные ошибки, не связанные со стором). Путь
   НЕДОСТИЖИМ в этом стенде (один и тот же код пишет и читает формат
   стора) — правка не меняет ни одного числа §1–§7, только делает код
   корректным по умолчанию; проверено `TopologyTestDriver`-тестом (см.
   «Тесты» ниже), не живым стендом.
2. **Усиленная семантическая валидация** (`consumer/main.go`, вынесена в
   `consumer/validate.go`: `parseValidTotal`/`parseValidOrderEvent`) —
   валидация круга 1 (см. п.2 выше) отвергала только `customer_id==0`;
   `{"customer_id":1}` (orders/total отсутствуют → `orders=0`) и
   `customer_id` вне 1..5 (например, синтетический `999` из
   `scripts/schema-evolve.sh`, ранее нарочно пропускавшийся, см. §7)
   проходили в витрину. Теперь totals-режим отвергает `customer_id<1 ||
   customer_id>5` ИЛИ `orders==0`; dedup-demo режим — `order_id==0 ||
   customer_id<1 || customer_id>5`. Сообщения об ошибке называют ТОЧНУЮ
   причину. Живая проверка нового кейса (`{"customer_id":1}`) — новый
   подраздел в §6 ниже. Числа §1–§5/§7 не переснимались: изменение
   поведения для `customer_id=999` (§7) в этом файле остаётся
   задокументированным ИСТОРИЧЕСКИ, как поведение ДО этого ужесточения.
3. **Точечная пересъёмка §6** — новый кейс `{"customer_id":1}` (см. §6
   ниже), команда+вывод сняты живьём на том же поднятом стенде.
4. **Автотесты** — `go test ./...` и `mvn test` раньше только компилировали.
   См. отдельный раздел «Тесты» в конце файла.

## Ремедиация круга 3 (по четвёртому авторскому ревью серии, без полного ре-рана)

Точечная правка кода (2 блокера + 2 улучшения) + точечная пересъёмка §6 (новый
кейс) + новые автотесты. §1–§5 и §7 НЕ переснимались — эти правки на их числа
не влияют (см. обоснование в каждом пункте). Живой стенд не пересоздавался
(`down -v` не выполнялся) — те же контейнеры, что и после круга 2.

1. **Fail-fast по ВСЕЙ цепочке причин, а не по верхнему throwable**
   (`streams/.../StreamsApp.java`) — предыдущая правка (круг 2, п.1 выше)
   проверяла `throwable instanceof CorruptedStateStoreException` на ВЕРХНЕМ
   объекте, переданном в `setUncaughtExceptionHandler`. Но Kafka Streams
   4.3.1 ОБОРАЧИВАЕТ исключение, брошенное из `Processor.process()`, в
   `StreamsException` ПЕРЕД тем, как передать его в обработчик — `instanceof`
   на верхнем throwable была ЛОЖЬЮ, обработчик реально уходил в
   `REPLACE_THREAD` (дефолтную ветку до этой правки), то есть в тот самый
   краш-луп на повреждённом сторе, который вся правка круга 2 обещала
   предотвратить. Классификация вынесена в static
   `StreamsApp.classifyResponse(Throwable)` — идёт по цепочке `getCause()`,
   находит `CorruptedStateStoreException` на любой глубине. Путь по-прежнему
   НЕДОСТИЖИМ в этом стенде (один и тот же код пишет и читает формат стора) —
   правка не меняет ни одного числа §1–§7, только делает код ДЕЙСТВИТЕЛЬНО
   fail-fast (было — код, ошибочно СЧИТАВШИЙСЯ fail-fast). Проверено новым
   юнит-тестом на СКОНСТРУИРОВАННОМ обёрнутом исключении
   (`classifyResponseFindsCorruptedStoreExceptionInWrappedCause`) и живым
   `TopologyTestDriver`-тестом на РЕАЛЬНОЙ цепочке причин
   (`corruptedStoreRecordTriggersFailFast`, теперь проверяет
   `classifyResponse` на реально брошенном throwable, а не вручную
   разматывает цепочку) — см. «Тесты (актуальные, кругов 3–6)» ниже.
   ⚠️ **ОТМЕНЕНО кругом 6:** после п.3 ниже (SHUTDOWN_CLIENT для всего) обе
   ветки `classifyResponse` стали возвращать одно и то же, а тесты на неё —
   пустыми (метод целиком удалили бы, и они бы всё равно прошли). Метод удалён,
   обработчик стал безусловным. См. «Ремедиация круга 6».
2. **`total` теперь обязательное поле, как и `orders`** (`consumer/validate.go`)
   — `parseValidTotal` проверяла `customer_id` и `orders`, но не `total`:
   `{"customer_id":1,"orders":1}` (без `total`) проходил бы как валидная
   запись с `total=0`, хотя `total` нигде не присылался — статья
   (`event-pipelines-design.md`) называет ОБА поля обязательными. Разбор
   перенесён на промежуточную wire-структуру `totalWire` с `Total *float64`
   — различает ОТСУТСТВУЮЩИЙ `total` (nil) от явного `total:0` (валидное
   значение, присутствие поля важнее его значения, проверки `total>0` нет).
   Отсутствующий `total` теперь отвергается (DLQ) с точным сообщением;
   явный `total:0` по-прежнему проходит. Живая проверка нового кейса — новый
   подраздел в §6 ниже.
3. **Дефолт SHUTDOWN_CLIENT, а не REPLACE_THREAD, для НЕКЛАССИФИЦИРОВАННЫХ
   причин** (`StreamsApp.classifyResponse`) — до этой правки любая ошибка
   ИНАЧЕ CorruptedStateStoreException давала REPLACE_THREAD, в предположении
   "любая другая ошибка транзиентная". Это предположение необоснованно:
   неизвестная причина (например, `NullPointerException` — баг
   пользовательского кода) с той же вероятностью детерминирована, и
   REPLACE_THREAD дал бы тот же краш-луп, что и у CorruptedStateStoreException.
   `classifyResponse` теперь возвращает SHUTDOWN_CLIENT ДЛЯ ВСЕГО — код этого
   стенда не порождает классифицированных транзиентных причин, поэтому
   REPLACE_THREAD в нём больше нигде не используется. Проверено новым
   юнит-тестом (`classifyResponseDefaultsToShutdownForUnknownCause`, см.
   «Тесты (актуальные, кругов 3–6)» ниже) — живого стенда не касается (путь
   по-прежнему недостижим).
   ⚠️ **Решение в силе, реализация упрощена кругом 6:** политика «SHUTDOWN_CLIENT
   для всего» сохранена, но выражена безусловным обработчиком без
   `classifyResponse` — см. «Ремедиация круга 6».
4. **Смягчён комментарий про repartition** (`StreamsAppTest.java`,
   `aggregatesOrdersPerCustomer`) — было "косвенно проверяет repartition",
   что переоценивает `TopologyTestDriver`: он проверяет КОРРЕКТНОСТЬ
   АГРЕГАЦИИ (что репартиционированные записи одного клиента сходятся в одно
   накопление), но не воспроизводит физическое распределение по нескольким
   stream-task'ам на нескольких партициях. Полноценное подтверждение
   co-partitioning — интеграционный прогон на живом стенде с несколькими
   партициями (см. §1/§2 — стенд поднят с 3 партициями `orders.events`).
   Правка комментария, не поведения.

## Ремедиация круга 4 (по пятому авторскому ревью, без ре-рана)

Один блокер — ТИХОЕ искажение агрегата на non-finite `amount`. Живого стенда
не касается (числа §1–§7 не менялись): в стенде `seed.sh` генерирует только
конечные суммы, путь недостижим — но код учебный, и запускаемый пример не
должен молча портить данные. Проверено новыми тестами, не прогоном стенда.

1. **`amount` = `"NaN"`/`"Infinity"`/`"-Infinity"` проходил валидацию**
   (`StreamsApp.parseEvent`). `Double.parseDouble` принимает эти строки как
   ВАЛИДНЫЕ литералы Java — `NumberFormatException` не бросается, общий
   `catch` их не ловит. Дальше значение попадало в агрегат и искажалось на
   округлении `Math.round(total*100.0)/100.0`: `NaN` → **`0.0`** (обнуление
   суммы клиента), `Infinity` → **`9.223372036854776E16`**
   (`Math.round(Inf)==Long.MAX_VALUE`). То есть не отказ, который кто-то
   заметит, а тихое повреждение. Добавлена явная проверка
   `Double.isFinite(amount)` после разбора — событие отбрасывается тем же
   путём, что и прочие невалидные (см. `flatMap`).
2. **Переполнение накопленной суммы** (`TotalsProcessor.process`) — вторая
   линия обороны: слагаемые по отдельности конечны, но накопленный `total`
   может выйти за пределы `double` до `±Infinity` и дать то же
   `9.223372036854776E16`. Добавлена проверка `Double.isFinite(total)` после
   суммирования; при переполнении бросается исключение, обработчик отвечает
   `SHUTDOWN_CLIENT` (громкая остановка вместо тихо искажённого числа).
3. **Тестируемость** — `parseEvent`/`ParsedEvent` сделаны package-private
   (были `private`): разбор и валидация одного события — чистая функция без
   Kafka, теперь покрыта прямыми юнит-тестами. Java-тестов стало **7**
   (было 4), см. «Тесты (актуальные, кругов 3–6)» в конце файла.

## Ремедиация круга 5 (по шестому авторскому ревью, без ре-рана)

Один блокер — ЦЕЛОЧИСЛЕННЫЕ переполнения, тихо портящие агрегат. Штатного
потока не касается (в PostgreSQL `customer_id`/`order_id` — `BIGINT`, значения
заведомо влезают в `long`), числа §1–§7 не менялись. Но пример отдельно обещает
устойчивость к повреждённым данным, поэтому проверено новыми тестами.

1. **Алиасинг `customer_id` — тихое СМЕШИВАНИЕ клиентов** (`StreamsApp.parseEvent`).
   `custNode.isIntegralNumber()` истинен и для `BigInteger`, не влезающего в
   `long`, а `asLong()` в этом случае молча УСЕКАЕТ. Проверка на Jackson 2.18.2:
   `customer_id=18446744073709551617` (2^64+1) → `canConvertToLong()==false`,
   но `asLong()==1`. То есть событие с битым/чужим идентификатором приписалось
   бы **реальному клиенту 1** и исказило его сумму — правдоподобное неверное
   число, а не отказ. Добавлена проверка `canConvertToLong()` до `asLong()`;
   не прошедшее событие отбрасывается как битое. (Строковый путь был безопасен
   и раньше: `Long.parseLong` на таком значении бросает `NumberFormatException`.)
2. **Переполнение счётчика заказов** (`TotalsProcessor.process`). Было
   `orders = ordersNode.asLong() + 1` без проверок: при `orders=Long.MAX_VALUE`
   обычное сложение молча даёт `Long.MIN_VALUE`, и в store → changelog →
   `customer.totals` уехало бы ОТРИЦАТЕЛЬНОЕ число заказов. Добавлены:
   `ordersNode.canConvertToLong()`, проверка `orders >= 1` (реальный агрегат
   создаётся с `orders=1`, меньшее — признак повреждения), инкремент через
   `Math.addExact` с переводом `ArithmeticException` в
   `CorruptedStateStoreException` → `SHUTDOWN_CLIENT`.
3. **Тесты** — Java 7 → **10** (см. раздел о тестах ниже): `customer_id` вне
   диапазона `long` (числом и строкой), `orders=Long.MAX_VALUE`, отрицательный
   `orders` в сторе.

## Ремедиация круга 6 (по седьмому авторскому ревью, без ре-рана)

Не дефект поведения, а ФОРМАЛЬНОСТЬ, которая приписывала тестам доказательство,
которого те не давали. Числа §1–§7 не менялись.

**Что было не так.** После круга 3 (п.3 выше — SHUTDOWN_CLIENT для всего) обе
ветки `classifyResponse` возвращали ОДИН И ТОТ ЖЕ ответ:
`CorruptedStateStoreException → SHUTDOWN_CLIENT` и `любая другая причина →
SHUTDOWN_CLIENT`. Значит юнит-тест «находит исключение в обёрнутой цепочке»
ничего не доказывал: удалить весь цикл `getCause()` — и тест всё равно прошёл бы
через дефолтную ветку. Комментарий «без обхода цепочки этот тест провалился бы»
стал неверным. То же с `corruptedStoreRecordTriggersFailFast`:
`assertThrows(Throwable.class)` принимает ЛЮБОЕ исключение, а последующая
проверка ответа обработчика всегда истинна.

**Что сделано:**
1. `classifyResponse` **удалён**; обработчик стал безусловным — `SHUTDOWN_CLIENT`
   для любой необработанной ошибки. Политика та же, но код больше не изображает
   выбор, которого нет.
2. Два пустых юнит-теста (`classifyResponseFindsCorruptedStoreExceptionInWrappedCause`,
   `classifyResponseDefaultsToShutdownForUnknownCause`) **удалены**.
3. Оставшиеся fail-fast-тесты переписаны на СОДЕРЖАТЕЛЬНЫЕ проверки: тип
   исключения В ЦЕПОЧКЕ ПРИЧИН (тест-хелпер `causeOfType`) плюс характерное
   сообщение — `customer=3`, `непредставимый агрегат`, `переполнение счётчика
   заказов`, `orders=-5`. Обход `getCause()` остался там, где он реально нужен и
   что-то доказывает — В ТЕСТЕ (Kafka Streams оборачивает исключение
   `process()` в `StreamsException`), а не в production-обработчике.
4. Java-тестов стало **8** (было 10: −2 пустых). Из статьи и этого файла убрано
   утверждение, что выбор ответа требует классификации по цепочке причин.

## Порядок прогона (как он реально исполнен)

`down -v` → `up -d` → `create-topics.sh` → регистрация коннектора → §1
(snapshot/streaming) → `seed.sh 50` → запуск Kafka Streams (чистый
`STATE_DIR`) → §2 (state store: `kill -9` + `rm -rf` state dir + рестарт +
функциональная проверка восстановления) → §3 (baseline/`-dup` на
`customer_totals`, переосмысленный) → §3.1 (НОВЫЙ изолированный
дедуп-эксперимент на `orders.events`) → `seed.sh 20` → §4
(инкрементальный подхват + backfill/replay) → §5 (лаг: `kill -9` Kafka
Streams БЕЗ очистки state dir, `seed.sh 30` ×2 при остановленном звене,
рестарт → `TaskCorruptedException` → самовосстановление → **прогон
Go-консьюмера сразу после**, сливающий бэклог в ClickHouse — без разрывов
между Kafka-уровнем и ClickHouse, в отличие от предыдущей редакции) → §6
(DLQ: синтаксически битое + семантически пустое `{}`) → §7 (эволюция схемы,
`schema-evolve.sh`, ТОЛЬКО после того как §6 полностью доехал).

## Версии

| Компонент | Версия | Источник |
|---|---|---|
| PostgreSQL | `18.4` (образ `postgres:18.4`) | `docker exec ds-postgres postgres --version` → `postgres (PostgreSQL) 18.4 (Debian 18.4-1.pgdg13+1)` |
| Kafka | `4.3.1` (образ `apache/kafka:4.3.1`) | `docker exec ds-kafka /opt/kafka/bin/kafka-topics.sh --version` → `4.3.1` |
| Kafka Connect (framework) | `4.3.0` | `curl localhost:8084/` → `{"version":"4.3.0","commit":"a9ce3221537b8653",...}` |
| Debezium connector | `3.6.0.Final` (образ `quay.io/debezium/connect:3.6.0.Final`) | `curl localhost:8084/connectors/ds-outbox-connector/status` → `"connector":{"state":"RUNNING",...,"version":"3.6.0.Final"}` |
| Kafka Streams | `4.3.1` | `streams/pom.xml`: `<kafka.version>4.3.1</kafka.version>` |
| Jackson (databind) | `2.18.2` | `streams/pom.xml` |
| exec-maven-plugin | `3.5.0` | `streams/pom.xml` |
| ClickHouse | `26.6.1.1193` (образ `clickhouse/clickhouse-server:26.6.1.1193`) | `SELECT version()` → `26.6.1.1193` |
| Go | `1.26.3` | `go version` (WSL, host) → `go version go1.26.3 linux/amd64` |
| franz-go / kadm | `v1.18.0` / `v1.14.0` | `consumer/go.mod` |
| clickhouse-go/v2 | `v2.30.0` | `consumer/go.mod` |
| shopspring/decimal | `v1.4.0` | `consumer/go.mod` |
| JDK (сборка Kafka Streams) | Temurin `21.0.11` | `~/jdk21/bin/java -version` → `openjdk version "21.0.11" 2026-04-21 LTS` |
| Maven | `3.9.16` | `mvn -v` |

## §1 snapshot → streaming

Коннектор зарегистрирован (`connect/debezium-outbox.json`) на пустую таблицу
`outbox` (только что созданную `01-postgres.sql`, ни одной строки). Дословный
лог снапшота (`docker logs ds-connect`):

```
2026-07-17T18:25:19,340 INFO   Postgres||snapshot  Snapshot step 7 - Snapshotting data   [io.debezium.relational.RelationalSnapshotChangeEventSource]
2026-07-17T18:25:19,351 INFO   Postgres||snapshot  	 Finished exporting 0 records for table 'public.outbox' (1 of 1 tables); total duration '00:00:00.005'   [io.debezium.relational.RelationalSnapshotChangeEventSource]
2026-07-17T18:25:19,352 INFO   Postgres||snapshot  Snapshot completed   [io.debezium.pipeline.source.AbstractSnapshotChangeEventSource]
2026-07-17T18:25:19,353 INFO   Postgres||snapshot  Snapshot ended with SnapshotResult [status=COMPLETED, ...]   [io.debezium.pipeline.ChangeEventSourceCoordinator]
2026-07-17T18:25:19,375 INFO   Postgres||streaming  Starting streaming   [io.debezium.pipeline.ChangeEventSourceCoordinator]
2026-07-17T18:25:19,390 INFO   Postgres||streaming  Starting replication stream from LSN LSN{0/1BB7648} with automaticFlush=false (mode=CONNECTOR)   [io.debezium.connector.postgresql.connection.PostgresReplicationConnection]
2026-07-17T18:25:19,404 INFO   Postgres||streaming  Processing messages   [io.debezium.connector.postgresql.PostgresStreamingChangeEventSource]
```

**Снапшот снял 0 строк** (`Finished exporting 0 records`) — таблица `outbox`
была пуста на момент регистрации коннектора. Сразу после снапшота Debezium
переключился на `streaming` (логическая репликация, WAL) и остался в режиме
`Processing messages`.

Затем выполнен `bash scripts/seed.sh 50` (50 заказов + 50 событий outbox,
одной транзакцией на заказ). Все 50 событий прошли ИМЕННО через `streaming`
(снапшот их не мог захватить — он завершился раньше, чем эти строки появились
в PG). Подтверждение — фактические оффсеты `orders.events` сразу после seed:

```
$ docker exec ds-kafka /opt/kafka/bin/kafka-get-offsets.sh --bootstrap-server localhost:9092 --topic orders.events
orders.events:0:22
orders.events:1:17
orders.events:2:11
```

`22+17+11=50` — ровно столько, сколько строк вставил `seed.sh`.

## §2 state store и changelog

Приложение (`StreamsApp.java`): `APPLICATION_ID_CONFIG=ds-streams`,
`STORE=customer-totals-store` → changelog-топик
**`ds-streams-customer-totals-store-changelog`** (3 партиции):

```
$ docker exec ds-kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --describe --topic ds-streams-customer-totals-store-changelog
Topic: ds-streams-customer-totals-store-changelog	PartitionCount: 3	ReplicationFactor: 1	Configs: min.insync.replicas=1,cleanup.policy=compact,message.timestamp.type=CreateTime
```

Содержимое (после обработки первых 50 заказов, `--from-beginning`,
`print.key=true`) — по одной записи на каждое обновление состояния клиента:

```
2	{"customer_id":2,"orders":8,"total":384.32}
3	{"customer_id":3,"orders":7,"total":388.52}
2	{"customer_id":2,"orders":9,"total":474.39}
3	{"customer_id":3,"orders":8,"total":413.0}
3	{"customer_id":3,"orders":9,"total":472.04}
3	{"customer_id":3,"orders":10,"total":521.99}
2	{"customer_id":2,"orders":10,"total":565.71}
4	{"customer_id":4,"orders":1,"total":8.83}
...
4	{"customer_id":4,"orders":9,"total":466.99}
4	{"customer_id":4,"orders":10,"total":492.2}
Processed a total of 50 messages
```

PG-истина в этот момент (`SELECT customer_id, count(*), sum(amount) FROM orders GROUP BY customer_id`):
`1|10|636.70`, `2|10|565.71`, `3|10|521.99`, `4|10|492.20`, `5|10|604.86` —
`customer_id=4` в changelog (`orders=10, total=492.2`) совпадает с PG до цента.

**Восстановление после `kill -9` + `rm -rf` state dir** (проверка отличается
от рестарта в §5 ниже: здесь состояние НАРОЧНО стёрто перед рестартом — нет
локального чекпойнта, с которым можно разойтись, поэтому и нет
`TaskCorruptedException`; это чистое восстановление "с нуля" ИЗ changelog):

```
$ kill -9 98051   # процесс Kafka Streams
$ rm -rf /tmp/ds-streams
$ ls /tmp/ds-streams
ls: cannot access '/tmp/ds-streams': No such file or directory
$ cd streams && KAFKA=localhost:9096 mvn -q compile exec:java   # рестарт
```

Лог рестарта — только штатная строка запуска, без единой строки об ошибке
или о восстановлении на INFO-уровне (`slf4j-simple`, дефолтный порог):

```
streams запущен: orders.events -> customer.totals (store=customer-totals-store)
```

Вместо строки лога — физическое и функциональное доказательство:

```
$ sleep 8; du -sh /tmp/ds-streams      # сразу после рестарта
4.1M	/tmp/ds-streams
$ sleep 10; du -sh /tmp/ds-streams     # чуть позже
13M	/tmp/ds-streams
$ find /tmp/ds-streams -path "*rocksdb*" | head -5
/tmp/ds-streams/ds-streams/1_0/rocksdb
/tmp/ds-streams/ds-streams/1_0/rocksdb/customer-totals-store
/tmp/ds-streams/ds-streams/1_0/rocksdb/customer-totals-store/LOG
/tmp/ds-streams/ds-streams/1_0/rocksdb/customer-totals-store/000004.log
```

RocksDB-каталоги материализовались с данными (0 → 4.1M → 13M) — восстановление
из changelog-топика реально произошло. Фальсифицируемая функциональная
проверка: до рестарта `customer_id=1` был на `orders=10, total=636.70` (см. PG
выше — идентично changelog). Один новый заказ на 10.00 после рестарта:

```
$ # INSERT одного заказа (customer_id=1, amount=10.00) через outbox, той же
$ # транзакцией, что и seed.sh
$ docker exec ds-kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server localhost:9092 --topic customer.totals --from-beginning --timeout-ms 8000 | grep '"customer_id":1,'
{"customer_id":1,"orders":1,"total":80.33}
...
{"customer_id":1,"orders":10,"total":636.7}
{"customer_id":1,"orders":11,"total":646.7}
```

`orders=11, total=646.70` (636.70+10.00) — счётчик ПРОДОЛЖИЛ накопление с 10,
а не сбросился на 1. Если бы состояние не восстановилось из changelog,
новая запись показала бы `orders=1, total=10.00` — это и есть падающий
вариант, которым проверка фальсифицируема.

PG-истина после этой вставки: `51` заказ, `sum=2831.46` — используется как
опорная точка для §3 ниже.

## §3 дубли на стоке customer_totals (переосмыслено)

**Важно (см. §3.1 ниже для настоящего доказательства дедупа):** этот
раздел показывает СЕМАНТИКУ ЧТЕНИЯ changelog накопительных снимков — не
эффект дублей. `customer_totals`/`raw_totals` заполняются из
`customer.totals`, а это changelog СОСТОЯНИЙ клиента (каждое сообщение —
"клиент X теперь на orders=N, total=M"), а не независимых событий. Наивная
`sum(total)` по такому changelog завышена УЖЕ в baseline, без единого
дубля — она складывает ~N РАСТУЩИХ частичных сумм на одного клиента вместо
одного последнего числа.

Baseline (`go run . -from-start -for 15s`, БЕЗ `-dup`, ClickHouse только что
пересоздан пустым):

```
консьюмер: вставлено=51 битых=0 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=false, from-start=true, mode=totals)
```

```
raw_totals count()	51
customer_totals count() (no FINAL)	51
customer_totals FINAL count()/sum(total)	5	2831.46
```

PG-истина в этот момент: `51|2831.46` — совпадает с `customer_totals FINAL`
до цента.

Тот же прогон с `-dup` (`go run . -dup -from-start -for 15s`, тот же топик,
тот же сброс оффсета на earliest — единственная переменная — флаг `-dup`):

```
консьюмер: вставлено=102 битых=0 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=true, from-start=true, mode=totals)
```

```
raw_totals count()	153
customer_totals count() (no FINAL)	153
customer_totals FINAL count()/sum(total)	5	2831.46
```

`customer_totals FINAL` **не изменилась** (2831.46, байт-в-байт) несмотря на
дубли — но, как объяснено выше, это доказывает устойчивость чтения ЭТОГО
конкретного changelog к дублям снимков, а не эффект дублей на "реальных"
данных (в этом changelog "реальных данных" в смысле независимых событий
нет — только растущие снимки).

**Числа, которые НЕ являются доказательством дедупа (сохранены как
диагностика семантики чтения, не как аргумент):**

```
raw_totals наивная sum(total) (без дедупа): 49779.99   -- врёт кратно; см. объяснение выше
raw_totals argMax(total, version) по customer_id (то, что делает FINAL): 2831.46   -- = PG
```

Наивная сумма (49779.99) искажена в первую очередь семантикой changelog
(суммирование растущих снимков), а не количеством физических дублей — она
была бы огромной и без единого `-dup` (при полном перечитывании топика
дважды подряд без `-dup` она тоже выросла бы кратно). Правильное чтение —
`argMax(total, version)` (то, что `FINAL` делает автоматически) — совпадает
с PG.

`customer_totals` и `raw_totals` физически ОДИНАКОВЫ **в этот момент** (153
строк в обеих — консьюмер льёт одни и те же строки в обе таблицы). Разницу
даёт модификатор `FINAL` (query-time), не движок сам по себе:

```
$ docker exec ds-clickhouse clickhouse-client -u ds --password ds -d ds -q "SELECT count() FROM raw_totals FINAL"
Received exception from server (version 26.6.1):
Code: 181. DB::Exception: Received from localhost:9000. DB::Exception: Storage MergeTree doesn't support FINAL. (ILLEGAL_FINAL)
(query: SELECT count() FROM raw_totals FINAL)
```

**Асинхронность дедупа, живой контролируемый эксперимент** (`system.part_log`,
снято В КОНЦЕ всего прогона, после §7 — те же пять входных кусков слиты в
обеих таблицах, в одну и ту же секунду):

```
$ docker exec ds-clickhouse clickhouse-client -u ds --password ds -d ds -q "
SELECT event_time, table, part_name, rows, event_type, merged_from
FROM system.part_log
WHERE table IN ('raw_totals','customer_totals') AND event_type LIKE 'Merge%'
ORDER BY event_time, table FORMAT TSVWithNames
"
event_time	table	part_name	rows	event_type	merged_from
2026-07-17 18:37:57	customer_totals	all_1_5_1	5	MergeParts	['all_1_1_0','all_2_2_0','all_3_3_0','all_4_4_0','all_5_5_0']
2026-07-17 18:37:58	raw_totals	all_1_5_1	304	MergeParts	['all_1_1_0','all_2_2_0','all_3_3_0','all_4_4_0','all_5_5_0']
```

Единственная переменная — движок. Оба слили одни и те же пять кусков
(`all_1_1_0`…`all_5_5_0`) в один и тот же по имени кусок `all_1_5_1`.
`MergeTree` (`raw_totals`) дал на выходе **304** строки — ни одна не
погашена. `ReplacingMergeTree` (`customer_totals`) на тех же пяти кусках дал
**5** строк — дубли схлопнуты прямо во время merge. Живая проверка
состояния таблиц в самом конце всего прогона (после §7, включая ещё одну
вставку поверх смёрдженного куска — отсюда несовпадение с "304"/"5" выше):

```
$ docker exec ds-clickhouse clickhouse-client -u ds --password ds -d ds -q "SELECT count() FROM raw_totals"
306
$ docker exec ds-clickhouse clickhouse-client -u ds --password ds -d ds -q "SELECT count() FROM customer_totals"
7
```

Паритет количества строк без `FINAL` — не гарантия, а случайность момента
замера; читать `customer_totals` нужно всегда через `FINAL`/`argMax`.

## §3.1 Изолированный дедуп-эксперимент (новое, orders.events)

Снят СРАЗУ после §3-`dup` (PG в этот момент: `51` заказ — до `seed.sh 20` из
§4). Источник — `orders.events` (события Debezium-outbox, формат
`{"amount":..,"order_id":..,"customer_id":..}`, сверено дословно с реальным
топиком):

```
$ docker exec ds-kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server localhost:9092 --topic orders.events --partition 0 --offset 0 --max-messages 3 --timeout-ms 8000
{"amount":59.68,"order_id":1,"customer_id":2}
{"amount":80.33,"order_id":5,"customer_id":1}
{"amount":87.63,"order_id":7,"customer_id":3}
```

Сток — `sql/02-clickhouse.sql`: `ds.order_counts_raw` (`SummingMergeTree`,
`cnt=1` на событие, движок складывает при merge) и `ds.order_dedup`
(`ReplacingMergeTree(version)` по `order_id`, `FINAL` оставляет одну строку
на уникальный заказ). Оба заполняет новый режим консьюмера
`-mode dedup-demo` (переиспользует `read_committed`/DLQ/at-least-once
обвязку из основного режима — см. `consumer/main.go`).

**Baseline (первое прочтение `orders.events` с начала):**

```
$ go run . -mode dedup-demo -from-start -for 15s
консьюмер: вставлено=51 битых=0 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=false, from-start=true, mode=dedup-demo)
```

```
order_counts_raw sum(cnt)	51
order_counts_raw count() (физические строки)	51
order_dedup FINAL count()	51
order_dedup count() (физические строки, no FINAL)	51
PG count(*) FROM orders	51
```

N=51 — совпадает с PG. Дублей ещё не было, обе таблицы физически совпадают
с N.

**Replay (`-from-start` ЕЩЁ РАЗ, тот же топик перечитан целиком второй раз —
чистая имитация повторной доставки/backfill без единого изменения в
источнике):**

```
$ go run . -mode dedup-demo -from-start -for 15s
консьюмер: вставлено=51 битых=0 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=false, from-start=true, mode=dedup-demo)
```

```
order_counts_raw sum(cnt)	102
order_counts_raw count() (физические строки)	102
order_dedup FINAL count()	51
order_dedup count() (физические строки, no FINAL)	102
```

**Это и есть чистое доказательство эффекта дубля, без искажения changelog-
семантикой:** `order_counts_raw` (без дедупа, `+1` на КАЖДОЕ событие) —
`sum(cnt)` буквально **удвоилась**, `51 → 102` (`2N`), потому что каждый
из 51 реального заказа физически посчитан дважды. `order_dedup`
(дедуп по `order_id`) — `FINAL count()` **не изменился**, `51` (`N`),
несмотря на 102 физические строки: повторная доставка того же `order_id`
гасится при merge/`FINAL`. `2N` против `N` на одном и том же входе,
единственная переменная — повторное чтение — это и есть прямой эффект
дубля, в отличие от §3 (где даже без единого дубля наивная сумма уже
искажена семантикой снимков состояния).

## §4 backfill/replay

Продолжение состояния §3.1 (PG=51 заказ, `raw_totals`=153,
`customer_totals FINAL`=5/2831.46). Досеяно 20 новых заказов (`seed.sh 20`),
`customer.totals` выросла до offset=77.

**Инкрементальный подхват (БЕЗ `-from-start`, продолжение с committed offset):**

```
сырых до: raw_totals=153, FINAL=5/2831.46
$ go run . -for 15s
консьюмер: вставлено=20 битых=0 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=false, from-start=false, mode=totals)
сырых после: raw_totals=173, FINAL=5/3757.82
```

PG в этот момент: `71|3757.82` — совпадает с FINAL до цента.

**Backfill/replay (`-from-start`: сброс committed offset на earliest, полное
перечитывание топика с начала):**

```
сырых до: raw_totals=173, FINAL=5/3757.82
$ go run . -from-start -for 15s
консьюмер: вставлено=71 битых=0 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=false, from-start=true, mode=totals)
сырых после: raw_totals=244, FINAL=5/3757.82
```

`raw_totals` выросла ещё на 71 (весь топик — 51 из §3-baseline + 20 новых —
перечитан заново и вставлен физически второй раз: 173+71=244).
`customer_totals FINAL` **не изменилась** (5/3757.82 — байт-в-байт та же
сумма), несмотря на 71 повторную физическую вставку.

## §5 лаг

**В покое** (Kafka Streams и Go-консьюмер простаивают):

```
=== группа ds-streams ===
GROUP       TOPIC                                    PARTITION  CURRENT-OFFSET  LOG-END-OFFSET  LAG
ds-streams  ds-streams-customer-totals-repartition    0          33              33              0
ds-streams  ds-streams-customer-totals-repartition    1          17              17              0
ds-streams  ds-streams-customer-totals-repartition    2          32              32              0
ds-streams  orders.events                             0          33              33              0
ds-streams  orders.events                             1          25              25              0
ds-streams  orders.events                             2          13              13              0
=== группа ds-sink ===
GROUP     TOPIC            PARTITION  CURRENT-OFFSET  LOG-END-OFFSET  LAG
ds-sink   customer.totals  0          76              77              1
```

`ds-streams` LAG=0 на всех партициях. `ds-sink` LAG=1 — на ЭТОМ конкретном
измерении (не постоянное свойство, зависит от того, была ли последняя
запись в топике control-маркером транзакционного продюсера EXACTLY_ONCE_V2 —
см. `scripts/lag.sh`; в другой момент, если последняя запись — данные, LAG
здесь мог бы быть и 0). Go-консьюмер физически не может "дочитать"
control-маркер — это не сообщение.

**Растущий лаг живьём.** Kafka Streams остановлен `kill -9` (БЕЗ очистки
state dir — контраст с §2, отсюда `TaskCorruptedException` при рестарте
ниже), `orders.events` продолжает наполняться:

```
$ bash scripts/seed.sh 30   # Streams остановлен
--- lag.sh (снимок 1) ---
ds-streams  orders.events   0   33   46   13
ds-streams  orders.events   1   25   33   8
ds-streams  orders.events   2   13   22   9
                                          (итого 30)

$ bash scripts/seed.sh 30   # ещё 30, Streams всё ещё остановлен
--- lag.sh (снимок 2) ---
ds-streams  orders.events   0   33   54   21
ds-streams  orders.events   1   25   41   16
ds-streams  orders.events   2   13   36   23
                                          (итого 60)
```

Лаг вырос с 30 до 60 — ровно на число заказов, добавленных, пока звено было
остановлено.

**Рестарт Kafka Streams — `TaskCorruptedException`, контраст с чистым §2:**

```
$ cd streams && KAFKA=localhost:9096 mvn -q compile exec:java
streams запущен: orders.events -> customer.totals (store=customer-totals-store)
[ds-streams-...-StreamThread-1] ERROR org.apache.kafka.streams.processor.internals.TaskManager - stream-thread [...] Get exceptions for the following tasks: {1_1=org.apache.kafka.streams.errors.TaskCorruptedException: Tasks [1_1] are corrupted and hence need to be re-initialized, 1_2=..., 1_0=...}
```

В §2 рестарт после `kill -9` был ЧИСТЫМ (без ошибки) — потому что state dir
там был предварительно стёрт `rm -rf`: не с чем расходиться. Здесь state dir
НЕ стёрт: локальный чекпойнт RocksDB не совпал с состоянием брокера после
нечистой остановки (`kill -9`) — Kafka Streams обнаружил это сам и
самовосстановился: переинициализировал таски, заново восстановил состояние
из changelog-топика и нагнал весь бэклог без вмешательства оператора.
Подтверждение — `lag.sh` после рестарта:

```
=== группа ds-streams ===
GROUP       TOPIC                                    PARTITION  CURRENT-OFFSET  LOG-END-OFFSET  LAG
ds-streams  ds-streams-customer-totals-repartition    0          58   58   0
ds-streams  ds-streams-customer-totals-repartition    1          30   30   0
ds-streams  ds-streams-customer-totals-repartition    2          57   57   0
ds-streams  orders.events                             0          54   54   0
ds-streams  orders.events                             1          41   41   0
ds-streams  orders.events                             2          36   36   0
=== группа ds-sink ===
GROUP     TOPIC            PARTITION  CURRENT-OFFSET  LOG-END-OFFSET  LAG
ds-sink   customer.totals  0          76   140  64
```

`ds-streams` снова LAG=0 на всех партициях — самовосстановление подтверждено
Kafka-уровнем. `ds-sink` LAG=64 — это Kafka-уровневый бэклог (Streams уже
нагнал и произвёл агрегаты, `customer.totals` выросла до offset=140), но он
ещё НЕ слит в ClickHouse.

**Слив бэклога в ClickHouse — прогнан СРАЗУ, без разрыва (в отличие от
предыдущей редакции, где этот шаг был пропущен и восстанавливался задним
числом по `system.part_log`):**

```
$ go run . -for 15s
консьюмер: вставлено=60 битых=0 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=false, from-start=false, mode=totals)
```

```
raw_totals count() после	304
customer_totals FINAL count()/sum(total) после	5	6492.23
PG (истина)	131	6492.23
```

Совпадает до цента. Лаг после слива:

```
=== группа ds-sink ===
GROUP     TOPIC            PARTITION  CURRENT-OFFSET  LOG-END-OFFSET  LAG
ds-sink   customer.totals  0          139  140  1
```

LAG вернулся к 1 (тот же control-маркер, см. оговорку в начале раздела).

## §6 DLQ

Продолжение состояния после §5 (`raw_totals=304`, DLQ пуст, offset=0).

### Синтаксически битое (не-JSON)

```
$ bash scripts/inject-bad.sh
битое событие отправлено в customer.totals

$ cd consumer && go run . -for 15s
битое событие offset=140: invalid character 'o' in literal null (expecting 'u')
консьюмер: вставлено=0 битых=1 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=false, from-start=false, mode=totals)
```

Пайплайн не упал, битых=1, ошибок_вставки=0, dlq_ошибок=0.

### Семантически пустое (`{}`) — новая проверка A2

```
$ echo '{}' | docker exec -i ds-kafka /opt/kafka/bin/kafka-console-producer.sh --bootstrap-server localhost:9092 --topic customer.totals

$ cd consumer && go run . -for 15s
битое событие offset=141: семантически невалидная запись: customer_id=0 (диапазон клиентов стенда — 1..5, см. scripts/seed.sh)
консьюмер: вставлено=0 битых=1 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=false, from-start=false, mode=totals)
```

`{}` — синтаксически валидный JSON (`json.Unmarshal` не вернул бы ошибку до
ремедиации A2), но семантически пустой (`customer_id=0`, недостижимо для
реального события стенда) — новая проверка поймала его тем же путём, что и
синтаксически битое сообщение выше.

### Семантически неполное (`{"customer_id":1}`, orders/total отсутствуют) — ремедиация круга 2

Живая проверка нового усиления валидации (см. «Ремедиация круга 2» ниже,
п.2): `{"customer_id":1}` синтаксически валиден и раньше (проверка только
`customer_id==0`) ПРОХОДИЛ бы в витрину как `orders=0, total=0` — поля
orders/total просто отсутствуют в JSON, Go оставляет им нулевые значения
типа, `json.Unmarshal` это не считает ошибкой. Снято на живом стенде
(контейнеры подняты те же 4 часа, что и раньше — без `down -v`/пересборки;
`customer.totals` ДО вставки была на offset=146 (146 сообщений, `0`..`145`),
DLQ ДО вставки — на offset=3, а не 2 (см. примечание про офсет=145 ниже)):

```
$ docker exec ds-kafka /opt/kafka/bin/kafka-get-offsets.sh --bootstrap-server localhost:9092 --topic customer.totals
customer.totals:0:146

$ echo '{"customer_id":1}' | docker exec -i ds-kafka /opt/kafka/bin/kafka-console-producer.sh --bootstrap-server localhost:9092 --topic customer.totals

$ docker exec ds-kafka /opt/kafka/bin/kafka-get-offsets.sh --bootstrap-server localhost:9092 --topic customer.totals
customer.totals:0:147

$ cd consumer && go run . -for 15s
битое событие offset=146: семантически невалидная запись: orders=0 у customer_id=1 (реальный агрегат customer.totals всегда orders>=1, см. TotalsProcessor.process())
консьюмер: вставлено=0 битых=1 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=false, from-start=false, mode=totals)
```

Точный `dlq-reason` называет ИМЕННО то, что не так (`orders=0`, а не общее
«customer_id=0», которое здесь неверно — customer_id=1 валиден): проверка
`orders==0` (новая, независимая от диапазона customer_id) поймала запись,
которую старая проверка `customer_id==0` пропустила бы.

**Честно про офсет=145 (не часть этого кейса):** перед вставкой офсета=146
DLQ уже содержал 3 записи, а не 2, как в исходном §6 выше (`offset=140`,
`offset=141`) — обнаружена третья запись `dlq-origin-offset:145`, тот же
`{}`/`customer_id=0` сценарий, с текстом причины ДО ремедиации круга 2
(«customer_id=0 (диапазон клиентов стенда — 1..5, см. scripts/seed.sh)»,
без упоминания orders). Она не создана в рамках этой ремедиации и не
описана в исходном §6 — предположительно осталась от отдельной проверки
между исходным прогоном FIXTURES (§1–§7, дата 2026-07-17) и этой
ремедиацией; независимо от происхождения, на новый кейс (offset=146) она не
влияет. Приводится как есть, без досочинённого объяснения.

### Отсутствующий `total` (`{"customer_id":1,"orders":1}`) — ремедиация круга 3

Живая проверка нового усиления валидации (см. «Ремедиация круга 3» выше,
п.2): `{"customer_id":1,"orders":1}` — `orders` присутствует и ненулевой
(проходит проверку `orders==0`, в отличие от кейса `{"customer_id":1}` выше),
но `total` ОТСУТСТВУЕТ вовсе. ДО этой правки `parseValidTotal` не проверяла
`total` — запись прошла бы в витрину как `total=0`, хотя `total` нигде не
присылался. Снято на том же живом стенде (контейнеры подняты без
`down -v`/пересборки с момента исходного прогона; `customer.totals` ДО
вставки была на offset=147 (147 сообщений, `0`..`146`), DLQ ДО вставки — на
offset=4):

```
$ docker exec ds-kafka /opt/kafka/bin/kafka-get-offsets.sh --bootstrap-server localhost:9092 --topic customer.totals
customer.totals:0:147

$ docker exec ds-kafka /opt/kafka/bin/kafka-get-offsets.sh --bootstrap-server localhost:9092 --topic customer.totals.dlq
customer.totals.dlq:0:4

$ echo '{"customer_id":1,"orders":1}' | docker exec -i ds-kafka /opt/kafka/bin/kafka-console-producer.sh --bootstrap-server localhost:9092 --topic customer.totals

$ docker exec ds-kafka /opt/kafka/bin/kafka-get-offsets.sh --bootstrap-server localhost:9092 --topic customer.totals
customer.totals:0:148

$ cd consumer && go run . -for 15s
битое событие offset=147: семантически невалидная запись: total отсутствует у customer_id=1; обязательное поле реального агрегата (см. TotalsProcessor.process())
консьюмер: вставлено=0 битых=1 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=false, from-start=false, mode=totals)
```

Точный `dlq-reason` называет ИМЕННО то, что не так (`total отсутствует`, а
не `orders=0` — `orders=1` здесь валиден): проверка `Total == nil` (новая,
независимая от `orders`) поймала запись, которую ДО этой правки не поймала
бы ни одна из существовавших проверок (`customer_id` в диапазоне, `orders`
ненулевой — обе выполняются).

### Содержимое DLQ — снимок после ремедиации круга 3 (5 записей, ранее задокументированы 4)

```
$ docker exec ds-kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server localhost:9092 --topic customer.totals.dlq --from-beginning --formatter-property print.headers=true --formatter-property print.key=true --timeout-ms 8000
dlq-reason:invalid character 'o' in literal null (expecting 'u'),dlq-origin-topic:customer.totals,dlq-origin-offset:140	null	not-a-json{{{
dlq-reason:семантически невалидная запись: customer_id=0 (диапазон клиентов стенда — 1..5, см. scripts/seed.sh),dlq-origin-topic:customer.totals,dlq-origin-offset:141	null	{}
dlq-reason:семантически невалидная запись: customer_id=0 (диапазон клиентов стенда — 1..5, см. scripts/seed.sh),dlq-origin-topic:customer.totals,dlq-origin-offset:145	null	{}
dlq-reason:семантически невалидная запись: orders=0 у customer_id=1 (реальный агрегат customer.totals всегда orders>=1, см. TotalsProcessor.process()),dlq-origin-topic:customer.totals,dlq-origin-offset:146	null	{"customer_id":1}
dlq-reason:семантически невалидная запись: total отсутствует у customer_id=1; обязательное поле реального агрегата (см. TotalsProcessor.process()),dlq-origin-topic:customer.totals,dlq-origin-offset:147	null	{"customer_id":1,"orders":1}
Processed a total of 5 messages
```

Причина (`dlq-reason`), исходный топик/оффсет и тело как есть — ничего не
потеряно молча, для всех пяти записей, включая необъяснённую offset=145
(см. примечание выше).

### Содержимое DLQ — снимок после ремедиации круга 2 (4 записи, ранее задокументированы 2)

```
$ docker exec ds-kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server localhost:9092 --topic customer.totals.dlq --from-beginning --formatter-property print.headers=true --formatter-property print.key=true --timeout-ms 8000
dlq-reason:invalid character 'o' in literal null (expecting 'u'),dlq-origin-topic:customer.totals,dlq-origin-offset:140	null	not-a-json{{{
dlq-reason:семантически невалидная запись: customer_id=0 (диапазон клиентов стенда — 1..5, см. scripts/seed.sh),dlq-origin-topic:customer.totals,dlq-origin-offset:141	null	{}
dlq-reason:семантически невалидная запись: customer_id=0 (диапазон клиентов стенда — 1..5, см. scripts/seed.sh),dlq-origin-topic:customer.totals,dlq-origin-offset:145	null	{}
dlq-reason:семантически невалидная запись: orders=0 у customer_id=1 (реальный агрегат customer.totals всегда orders>=1, см. TotalsProcessor.process()),dlq-origin-topic:customer.totals,dlq-origin-offset:146	null	{"customer_id":1}
Processed a total of 4 messages
```

Причина (`dlq-reason`), исходный топик/оффсет и тело как есть — ничего не
потеряно молча, для всех четырёх записей, включая необъяснённую offset=145.

### Содержимое DLQ (обе записи) — исходный снимок §6, не переснимался

```
$ docker exec ds-kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server localhost:9092 --topic customer.totals.dlq --from-beginning --property print.headers=true --property print.key=true --timeout-ms 8000
dlq-reason:invalid character 'o' in literal null (expecting 'u'),dlq-origin-topic:customer.totals,dlq-origin-offset:140	null	not-a-json{{{
dlq-reason:семантически невалидная запись: customer_id=0 (диапазон клиентов стенда — 1..5, см. scripts/seed.sh),dlq-origin-topic:customer.totals,dlq-origin-offset:141	null	{}
Processed a total of 2 messages
```

Причина (`dlq-reason`), исходный топик/оффсет и тело как есть — ничего не
потеряно молча, для обеих записей.

## §7 эволюция схемы

Полный дословный вывод `bash scripts/schema-evolve.sh` (exit 0), выполнено
СРАЗУ после §6 (DLQ уже доехал до конца):

```
Kafka Streams: pid=3021, запущен с: Fri Jul 17 21:36:46 2026
orders.events до вставки: p0=54 p1=41 p2=36
customer.totals до вставки: offset=142
customer.totals.dlq до вставки: offset=2
событие с новым полем отправлено: order_id=132 customer_id=3 amount=42.00 currency=RUB
PG (истина, независимо от Kafka): у customer_id=3 теперь orders=27 total=1112.08
--- сырое событие в orders.events (партиция 0) ---
{"amount":42.0,"currency":"RUB","order_id":132,"customer_id":3}
ПРОВЕРЕНО: Debezium передал currency=RUB в orders.events дословно (роутер сконфигурирован expand.json.payload=true + schemas.enable=false, см. разбор в шапке файла)
ПРОВЕРЕНО: процесс Kafka Streams не менялся (pid=3021) — но это вспомогательная проверка, не главное доказательство (см. п.5: главное — правильно посчитанный агрегат)
--- новая агрегация в customer.totals (посчитана Kafka Streams, ДО ClickHouse) ---
{"customer_id":3,"orders":27,"total":1112.08}
ПРОВЕРЕНО: currency НЕ попал в customer.totals — TotalsProcessor пересобирает JSON заново (customer_id/orders/total), поле обрезается ЗДЕСЬ.
Streams (Kafka-уровень, независимо от ClickHouse): orders=27 total=1112.08
ПРОВЕРЕНО (Kafka-уровень, ДО ClickHouse, falsifiable): Streams независимо посчитал orders=27 total=1112.08 — совпадает с PG (27/1112.08) до цента. Новое поле не сломало и не исказило агрегацию.
синтетический пробник отправлен напрямую в customer.totals (минуя Streams): customer_id=999 orders=1 total=9.99 + лишнее поле currency=RUB
CH customer_totals FINAL до консьюмера: 5	6492.23
CH raw_totals до консьюмера: count=304
--- запускаем Go-консьюмер (разовый прогон, не демон): customer.totals -> ClickHouse ---
консьюмер: вставлено=2 битых=0 dlq_ошибок=0 ошибок_вставки=0 ошибок_fetch=0 (dup=false, from-start=false, mode=totals)
Go-консьюмер: вставлено=2 битых=0 — штатный формат totals не изменился, консьюмер не заметил эволюцию схемы (потому что физически её не видит); синтетический пробник в этой сводке тоже НЕ битый.
CH customer_totals FINAL после консьюмера (реальные клиенты, без синтетического пробника): count=5 sum=6534.23
PG (истина): count=132 sum=6534.23
ПРОВЕРЕНО (сквозной уровень, falsifiable): FINAL-сумма витрины реальных клиентов (6534.23) сходится с PG (6534.23) до цента после эволюции схемы.
ПРОВЕРЕНО (Important-4, falsifiable, реальный конвейер, не мок): синтетическое сообщение с лишним полем currency, отправленное напрямую в customer.totals в обход Streams, дошло до Go-консьюмера и ClickHouse БЕЗ ИСКАЖЕНИЙ (orders=1 total=9.99) и не помечено битым/DLQ.
CH customer_id=3 (после консьюмера): orders=27 total=1112.08
raw_totals: +2 записей, совпадает со сводкой консьюмера (вставлено=2) — без потерь и без незамеченных дублей
DLQ не изменился (offset=2) — ни одно сообщение с новым полем не помечено битым
```

Разбор по звеньям (честно, без утверждений за границей серии):

1. **Debezium/EventRouter** — `currency` доехал до `orders.events` дословно.
   Причина — конфигурация SMT (`expand.json.payload=true` +
   `schemas.enable=false`), не внутренности Debezium.
2. **Kafka Streams** — не упал, независимо посчитанный агрегат совпал с PG
   до цента.
3. **`customer.totals`** — `currency` НЕ содержит: `TotalsProcessor`
   пересобирает JSON заново из трёх полей.
4. **Go-консьюмер, реальный трафик** — `currency` физически не видит (см.
   п.3): штатный формат разобран без ошибок (битых=0), сумма в CH
   сходится с PG.
5. **Go-консьюмер, изолированный синтетический пробник**
   (`customer_id=999`, вне диапазона реальных 1..5) — дошёл до ClickHouse
   БЕЗ ИСКАЖЕНИЙ, не помечен битым/DLQ. Фальсифицируемое доказательство
   устойчивости `encoding/json.Unmarshal` к незнакомым полям. **Историческая
   оговорка (ремедиация круга 2):** это поведение зафиксировано ДО
   ужесточения валидации (`customer_id<1 || customer_id>5`, см. «Ремедиация
   круга 2» в начале файла) — с текущим кодом `customer_id=999` уходил бы в
   DLQ, а не в витрину. §7 не переснимался, эта запись остаётся историческим
   снимком поведения на момент круга 1.
6. **ClickHouse (FINAL) и DLQ** — сумма реальных клиентов сходится с PG до
   цента, DLQ не вырос.

## Финальное состояние всех витрин (конец прогона)

```
raw_totals count()	306
customer_totals count() (no FINAL)	7
customer_totals FINAL (все, вкл. синтетический customer_id=999)	6	6544.22
customer_totals FINAL WHERE customer_id != 999	5	6534.23
order_counts_raw sum(cnt)	102   -- снято в §3.1, ДО seed.sh 20/30/30 из §4/§5 — не отражает финальные 132 заказа, см. §3.1
order_dedup FINAL count()	51    -- та же оговорка
PG orders: count(*), sum(amount)	132	6534.23
```

`customer_totals FINAL` (реальные клиенты) сходится с PG до цента на всём
протяжении прогона (§3 → §7). `order_counts_raw`/`order_dedup` — снимок
именно момента §3.1 (изолированный эксперимент был поставлен и завершён
до того, как §4 досеял ещё 71 заказ) — это осознанный выбор: эксперимент
самодостаточен и не требует повторного запуска на финальных данных, его
доказательная сила (2N против N) не зависит от того, в какой момент прогона
он снят.

## Особенности окружения

- **`TaskCorruptedException` после `kill -9` во время активной работы Kafka
  Streams, когда локальный state dir НЕ был предварительно очищен.**
  Наблюдалось в §5 (контраст с §2, где `kill -9` + `rm -rf` state dir дал
  чистый рестарт без ошибки). Kafka Streams обнаружил рассинхронизацию сам
  и самовосстановился: переинициализировал таски, восстановил состояние из
  changelog-топика и нагнал весь бэклог без вмешательства оператора
  (`lag.sh` после — `ds-streams` LAG=0 на всех партициях). Живое,
  воспроизведённое дважды в этом прогоне наблюдение (реально попадалось и
  при первом чистом старте до seed — не документировано отдельно, так как
  вызвано посторонним артефактом хостовой файловой системы, не частью
  демонстрируемого сценария); механика транзакционного протокола Kafka — за
  пределами этой серии (`messaging/kafka`), здесь фиксируется только
  наблюдаемое поведение.
- **`read_committed` (ремедиация A1) не изменила ни одного числа в этом
  прогоне** — честно: за весь прогон Kafka Streams ни разу не прервал
  (abort) транзакцию, поэтому разницы между `read_committed` и
  `read_uncommitted` на наблюдаемых данных здесь никто не увидел бы.
  Изменение защитное/структурное (устраняет КЛАСС риска — видимость
  прерванных транзакций), не воспроизведённое как наблюдаемый до/после
  результат в этом прогоне; форсировать реальный abort транзакции
  (например, убить брокера посреди коммита) — отдельная, более рискованная
  для стенда демонстрация, вне рамок этой ремедиации.
- **Kafka 4.3.1 + Debezium Connect 3.6.0.Final** — совместимы на всём
  прогоне, коннектор всё время оставался `RUNNING`, отказов не было.
- **Строка восстановления state store в §2 не появилась в stdout/stderr**
  приложения на INFO-уровне — вместо неё использовано физическое (файлы
  RocksDB, рост `du -sh`) и функциональное (продолжение счётчика, а не
  сброс) доказательство, см. §2.

## Тесты — ИСТОРИЧЕСКИЙ снимок (ремедиация круга 2, УСТАРЕЛ)

> ⚠️ **Этот раздел устарел и оставлен как история.** Числа ниже (8 Go-кейсов,
> 2 Java-теста) верны на момент круга 2; после кругов 3 и 4 тестовая база
> выросла, а формулировка про «косвенно проверяет repartition» была смягчена
> в самом исходнике теста. **Актуальный прогон — в разделе «Тесты (актуальные,
> кругов 3–4)» в конце файла.**

Раньше `go test`/`mvn test` только компилировали — тестов не было. Добавлены:

**Go** (`consumer/validate_test.go`, table-driven): `TestParseValidTotal` (8
кейсов: пустой `{}`, `{"customer_id":1}` без orders/total, customer_id вне
1..5, customer_id=0, orders=0, битый JSON, 2 валидных), `TestParseValidOrderEvent`
(6 кейсов: order_id=0 отсутствует/явно, customer_id вне диапазона/=0, битый
JSON, валидное событие), `TestDupFactor` (1 кейс: `dupFactor(false)==1`,
`dupFactor(true)==2`). `go test ./...` реально прогоняет — живой вывод:

```
$ go test ./... -v
=== RUN   TestParseValidTotal
--- PASS: TestParseValidTotal (0.00s)
    (8/8 подтестов PASS)
=== RUN   TestParseValidOrderEvent
--- PASS: TestParseValidOrderEvent (0.00s)
    (6/6 подтестов PASS)
=== RUN   TestDupFactor
--- PASS: TestDupFactor (0.00s)
PASS
ok  	khorost.tech/data-streaming/consumer	0.006s
```

**Java** (`streams/src/test/java/StreamsAppTest.java`, `TopologyTestDriver`,
добавлена зависимость `kafka-streams-test-utils` версии `${kafka.version}`
=`4.3.1`, scope test): `aggregatesOrdersPerCustomer` — несколько заказов
двух разных клиентов на вход `orders.events`, проверка, что последний
снимок каждого клиента в `customer.totals` равен сумме именно ЕГО заказов
(3/16.0 и 2/22.5) — косвенно проверяет repartition (заказы не смешались
между клиентами) и накопление в state store. `corruptedStoreRecordTriggersFailFast`
— стор портится напрямую через `driver.getKeyValueStore(...)`
(`{"orders":"not-a-number","total":10}`), затем событие для того же
customer_id — проверка, что где-то в цепочке причин брошенного исключения
есть `StreamsApp.CorruptedStateStoreException` (обёртку Kafka Streams в
`StreamsException` тест не предполагает заранее — проверяет по цепочке
`getCause()`). `mvn test` реально прогоняет — живой вывод:

```
[INFO] Tests run: 2, Failures: 0, Errors: 0, Skipped: 0, Time elapsed: 2.057 s -- in StreamsAppTest
[INFO] Tests run: 2, Failures: 0, Errors: 0, Skipped: 0
[INFO] BUILD SUCCESS
```

Оба теста в файле — реальный запуск на этой машине после правок кода (см.
«Ремедиация круга 2» в начале файла), не выдуманные числа.

## Тесты (актуальные, кругов 3–6)

Замена устаревшему разделу выше. Прогон на этой машине после правок кругов 3
(обязательный `total`, fail-fast по умолчанию), 4 (отказ на non-finite `amount`,
защита от переполнения суммы), 5 (алиасинг `customer_id`, переполнение счётчика
заказов) и 6 (безусловный обработчик вместо мнимой классификации; тесты проверяют
тип исключения в цепочке причин и сообщение, а не константный ответ).

**Go** (`consumer/validate_test.go`): три тест-функции, **16 подтестов**.
`TestParseValidTotal` — **10 кейсов**: пустой `{}`; `{"customer_id":1}` (orders
и total отсутствуют); `customer_id` вне 1..5; `customer_id=0` явно; `orders=0`
явно; **`{"customer_id":1,"orders":1}` — orders есть, `total` ОТСУТСТВУЕТ**
(круг 4/P2); **`total` явно равен 0** (присутствует → валиден); битый JSON;
валидная запись; валидная на границе диапазона (`customer_id=5`).
`TestParseValidOrderEvent` — 6 кейсов (order_id отсутствует/явный 0,
customer_id вне диапазона/=0, битый JSON, валидное событие). `TestDupFactor`.

```
$ go test ./... -count=1
ok  	khorost.tech/data-streaming/consumer	0.005s
```

**Java** (`streams/src/test/java/StreamsAppTest.java`, `TopologyTestDriver`):
**8 тестов**. Ответ обработчика (`SHUTDOWN_CLIENT`) НЕ проверяется ни одним из
них — он безусловен, и такая проверка была бы тавтологией (см. «Ремедиация
круга 6»). Fail-fast-тесты проверяют ТИП исключения в цепочке причин
(тест-хелпер `causeOfType`) и характерное СООБЩЕНИЕ.
1. `aggregatesOrdersPerCustomer` — последний снимок каждого клиента равен сумме
   именно ЕГО заказов. Проверяет КОРРЕКТНОСТЬ АГРЕГАЦИИ; полноценное
   подтверждение co-partitioning даёт интеграционный прогон на 3 партициях
   (см. §1/§2), а не `TopologyTestDriver`.
2. `corruptedStoreRecordTriggersFailFast` — стор портится напрямую через
   `driver.getKeyValueStore(...)`; в цепочке причин обязан лежать
   `CorruptedStateStoreException` с сообщением, называющим клиента (`customer=3`).
3. `parseEventRejectsNonFiniteAmount` (круг 4/P1) — `"NaN"`, `"Infinity"`,
   `"-Infinity"` отбрасываются на разборе (`Double.parseDouble` принимает их как
   валидные литералы, поэтому нужна явная проверка `Double.isFinite`).
4. `nonFiniteAmountEventIsDroppedNotAggregated` (круг 4/P1) — то же через
   реальную топологию: событие с `amount="NaN"` не даёт ни одной выходной записи.
5. `totalOverflowTriggersFailFast` (круг 4/P1) — слагаемые конечны, но сумма
   выходит за пределы double: `IllegalStateException` с сообщением
   «непредставимый агрегат», а не запись `9.223372036854776E16`.
6. `parseEventRejectsCustomerIdOutsideLongRange` (круг 5/P1) — `customer_id`
   вне диапазона `long` (2^64+1) отбрасывается, а НЕ усекается `asLong()` до
   `1`, то есть не приписывается реальному клиенту. Тем же тестом проверен
   строковый вариант того же идентификатора.
7. `ordersCounterOverflowTriggersFailFast` (круг 5/P1) — `orders=Long.MAX_VALUE`
   в сторе: `Math.addExact` ловит переполнение,
   `CorruptedStateStoreException` («переполнение счётчика заказов») вместо тихой
   записи `Long.MIN_VALUE` (отрицательного числа заказов) в changelog.
8. `negativeOrdersInStoreTriggersFailFast` (круг 5/P1) — отрицательный `orders`
   в сторе (заведомо невозможное состояние) → `CorruptedStateStoreException`
   с сообщением, называющим значение (`orders=-5`).

```
[INFO] Tests run: 8, Failures: 0, Errors: 0, Skipped: 0 -- in StreamsAppTest
[INFO] BUILD SUCCESS
```
