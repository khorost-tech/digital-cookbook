-- Схема event store. DROP+CREATE — стенд перезапускается сколько угодно.
DROP TABLE IF EXISTS events CASCADE;
DROP TABLE IF EXISTS snapshots CASCADE;
DROP TABLE IF EXISTS balances CASCADE;
DROP TABLE IF EXISTS balances_shadow CASCADE;
DROP TABLE IF EXISTS checkpoints CASCADE;

-- PRIMARY KEY (stream_id, version) — это и есть оптимистичная блокировка:
-- две попытки записать одну и ту же версию дают конфликт по PK (SQLSTATE 23505),
-- без строковых локов и SELECT ... FOR UPDATE.
-- global_pos — BIGSERIAL, и это ВАЖНАЯ оговорка: он НЕ является порядком
-- коммитов. Значение из сиквенса выдаётся в момент INSERT, а видимым для
-- читателей ряд становится в момент COMMIT, и между этими двумя моментами
-- может пройти сколько угодно времени. Транзакция A может взять позицию
-- 101 и задержаться, транзакция B — взять 102 и закоммититься первой.
-- Проектор, идущий курсором «global_pos > checkpoint», увидит 102,
-- сдвинет чекпоинт — и позже закоммиченная 101 не попадёт под условие
-- НИКОГДА. Событие потеряно безвозвратно, причём тихо.
--
-- Поэтому рядом лежит xid — идентификатор транзакции, записавшей событие.
-- Курсор проектора — пара (xid, global_pos), а не один global_pos, и
-- читает проектор только те события, чья транзакция гарантированно уже
-- завершилась: xid < pg_snapshot_xmin(pg_current_snapshot()). См.
-- go/projector.go и FIXTURES.md, «Артефакт 03».
CREATE TABLE events (
    stream_id   TEXT        NOT NULL,
    version     BIGINT      NOT NULL,
    event_type  TEXT        NOT NULL,
    schema_ver  INT         NOT NULL DEFAULT 1,
    data        JSONB       NOT NULL,
    metadata    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    global_pos  BIGSERIAL   NOT NULL,
    xid         xid8        NOT NULL DEFAULT pg_current_xact_id(),
    PRIMARY KEY (stream_id, version),
    -- Тип события хранится ДВАЖДЫ: столбцом event_type (по нему идут
    -- индексы, сверка и список допустимых типов) и внутри data->>'type' (по
    -- нему свою ветку выбирает проектор, потому что читает событие целиком).
    -- Два источника истины — приглашение к тихому расхождению: строка с
    -- event_type='OxygenConsumed' и data.type='OxygenAdded' проходит все
    -- проверки на «известные типы» (оба типа известны!), а проектор
    -- применяет её со знаком ПЛЮС вместо минуса. Ревью показало это
    -- контрпримером: ops=1, errors=0, чекпоинт сдвинут, unknown types = 0.
    --
    -- Проще всего сделать такое состояние НЕЗАПИСЫВАЕМЫМ. Ограничение ниже
    -- дешевле любой проверки на чтении и не требует, чтобы о нём помнили.
    CONSTRAINT events_type_matches_payload
        CHECK (data ? 'type' AND data->>'type' = event_type)
);
CREATE INDEX events_global_pos_idx ON events (global_pos);
-- Индекс под курсор проектора: (xid, global_pos) в том же порядке, в каком
-- по ним идёт ORDER BY.
CREATE INDEX events_cursor_idx ON events (xid, global_pos);

CREATE TABLE snapshots (
    stream_id  TEXT   PRIMARY KEY,
    version    BIGINT NOT NULL,
    state      JSONB  NOT NULL
);

CREATE TABLE balances (
    stream_id TEXT   PRIMARY KEY,
    version   BIGINT NOT NULL,
    liters    NUMERIC(14,3) NOT NULL DEFAULT 0
);

-- balances_shadow — рабочая таблица blue-green пересборки (задача 6,
-- rebuild-bluegreen): та же структура, что у balances, но отдельная
-- физическая таблица. Пересборка наполняет её заново с global_pos=0, пока
-- balances продолжает обслуживать чтения и онлайн-проектор продолжает её
-- обновлять; готовая пересобранная проекция подменяет боевую атомарным
-- тройным ALTER TABLE ... RENAME внутри одной транзакции (см. go/rebuild.go,
-- SwapBlueGreen). После подмены таблица balances_shadow не удаляется —
-- в неё уезжает СТАРОЕ содержимое balances (то, что было до подмены), и
-- следующий прогон затирает её TRUNCATE перед новой пересборкой.
CREATE TABLE balances_shadow (
    stream_id TEXT   PRIMARY KEY,
    version   BIGINT NOT NULL,
    liters    NUMERIC(14,3) NOT NULL DEFAULT 0
);

-- Чекпоинт проектора двигается В ТОЙ ЖЕ транзакции, что и применение события.
-- Чекпоинт — ПАРА (xid, global_pos), а не одно число: см. комментарий к
-- events.xid выше. Одного global_pos недостаточно, чтобы курсор был
-- монотонным относительно порядка коммитов.
CREATE TABLE checkpoints (
    projector  TEXT   PRIMARY KEY,
    global_pos BIGINT NOT NULL DEFAULT 0,
    xid        xid8   NOT NULL DEFAULT '0'::xid8
);
