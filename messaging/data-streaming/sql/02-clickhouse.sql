-- Витрина. ReplacingMergeTree — дедуп по ключу сортировки: при слиянии остаётся
-- строка с максимальной version. Это ответ на то, что Kafka EOS до ClickHouse
-- НЕ дотягивается: запись в CH вне транзакции Kafka, значит дубли возможны.
--
-- Дедуп ReplacingMergeTree АСИНХРОННЫЙ: слияние кусков — фоновый процесс, не
-- привязанный к моменту вставки. Поэтому наивный SELECT * может годами
-- возвращать дубли, даже если строки давно вставлены, — читать нужно через
-- FINAL (или argMax(...) в запросе), а не полагаться на то, что merge уже прошёл.
-- Контраст с этим — raw_totals ниже: там дубли не гасятся вообще, ни асинхронно,
-- ни при чтении.
CREATE TABLE IF NOT EXISTS ds.customer_totals
(
    customer_id UInt64,
    orders      UInt64,
    total       Decimal(12,2),
    -- version = offset исходного сообщения Kafka. Offset монотонен ТОЛЬКО в
    -- пределах одной партиции; в этом стенде у customer.totals партиция одна,
    -- поэтому это работает. При нескольких партициях offset разных партиций
    -- несравним, а при изменении их числа ReplacingMergeTree(version) начнёт
    -- выбирать между дублями произвольно, а не заведомо «последнюю» запись.
    -- В проде вместо offset нужен глобально монотонный источник (например,
    -- метка времени события) плюс явный тай-брейк.
    version     UInt64,
    updated_at  DateTime DEFAULT now()
)
ENGINE = ReplacingMergeTree(version)
ORDER BY customer_id;

-- Контрольная витрина: та же структура, что и customer_totals, но БЕЗ движка
-- дедупликации (обычный MergeTree). Консьюмер пишет каждое сообщение в ОБЕ
-- таблицы (см. consumer/clickhouse.go) — СРАЗУ ПОСЛЕ вставки, до того как
-- отработал первый фоновый merge, обе таблицы физически получают одинаковое
-- число строк (проверено живьём на конкретном прогоне, см. FIXTURES.md §3).
-- Это НЕ инвариант на любой момент времени, а мгновенный замер: "тот же
-- запрос, другой движок" ДЛЯ FINAL здесь физически невозможен —
-- `SELECT ... FROM ds.raw_totals FINAL` → `Code: 181, MergeTree doesn't
-- support FINAL` (см. FIXTURES.md §3, дословный вывод) — но count() без
-- FINAL НЕ гарантирует паритет во времени. Что реально несёт движок
-- (наблюдалось живьём на этом стенде, FIXTURES.md §3 и §7, STOP MERGES не
-- потребовался): ReplacingMergeTree способен схлопнуть дубли фоновым merge
-- без гарантии таймингов (на этом прогоне — через считаные секунды после
-- вставки пятого куска, `customer_totals` count() без FINAL схлопнулся с
-- 304 физических строк до 5; финальное состояние на момент FIXTURES.md §7 —
-- 7, после ещё одной вставки поверх смёрдженного куска), обычный MergeTree
-- (raw_totals) — не способен НИКОГДА, ни фоном, ни явно, поэтому его count()
-- продолжал расти линейно со вставками (306 на момент §7). Из-за этой асинхронности читать
-- дедуплицированно нужно ВСЕГДА через FINAL/argMax, а не полагаться на
-- состояние count() в моменте — ни на паритет "сразу после вставки", ни на
-- то, что merge уже прошёл. Дедуп для raw_totals возможен только вручную, в
-- каждом запросе (argMax).
CREATE TABLE IF NOT EXISTS ds.raw_totals
(
    customer_id UInt64,
    orders      UInt64,
    total       Decimal(12,2),
    version     UInt64,
    updated_at  DateTime DEFAULT now()
)
ENGINE = MergeTree
ORDER BY customer_id;

-- ИЗОЛИРОВАННЫЙ дедуп-эксперимент (§3 наивной sum(raw_totals) недостаточно —
-- customer_totals/raw_totals это changelog НАКОПИТЕЛЬНЫХ снимков состояния
-- клиента: sum(total) по ним завышена уже в baseline БЕЗ единого дубля,
-- потому что суммирует ~N растущих частичных сумм на одного клиента, а не
-- N независимых чисел. Это измеряет семантику ЧТЕНИЯ changelog (нужен
-- FINAL/argMax для последнего состояния), а НЕ эффект дублей — см. FIXTURES.md
-- §3/§3.1.
--
-- Здесь другой источник: orders.events — топик СОБЫТИЙ (каждое сообщение,
-- Debezium-outbox одного заказа), а не снимков состояния. Одно событие = +1,
-- без накопительного искажения: если дубль появится (повторная доставка,
-- backfill/replay), он либо удвоит число (raw), либо будет погашен по
-- уникальному order_id (dedup) — эффект дубля виден НАПРЯМУЮ, в отличие от
-- customer_totals/raw_totals.
--
-- Заполняется отдельным режимом консьюмера: `go run . -mode dedup-demo`
-- (см. consumer/main.go) — переключает вход на orders.events и сток на эти
-- две таблицы; ReadCommitted (см. A1/consumer/main.go) действует одинаково
-- в обоих режимах, это свойство клиента, а не режима.
CREATE TABLE IF NOT EXISTS ds.order_counts_raw
(
    customer_id UInt64,
    cnt         UInt64
)
ENGINE = SummingMergeTree
ORDER BY customer_id;

CREATE TABLE IF NOT EXISTS ds.order_dedup
(
    order_id    UInt64,
    customer_id UInt64,
    -- version = offset исходного сообщения orders.events. Тот же приём и та
    -- же оговорка про монотонность offset только в пределах одной партиции,
    -- что и у customer_totals.version выше — но здесь это НЕ проблема:
    -- ORDER BY у этой таблицы — order_id, а не customer_id, и order_id
    -- уникален глобально (bigserial PK в PG, см. sql/01-postgres.sql), в
    -- отличие от customer_id, который встречается у многих заказов. Дедуп
    -- ReplacingMergeTree(version) здесь выбирает между версиями ОДНОГО И
    -- ТОГО ЖЕ order_id (несколько доставок одного и того же события), а не
    -- между разными событиями одного клиента — несравнимость offset между
    -- партициями (orders.events — 3 партиции) тут ни на что не влияет:
    -- FINAL всё равно оставит РОВНО одну строку на order_id, какая из версий
    -- останется — не имеет значения для count() (все версии одного
    -- order_id несут одни и те же customer_id/order_id).
    version     UInt64
)
ENGINE = ReplacingMergeTree(version)
ORDER BY order_id;
