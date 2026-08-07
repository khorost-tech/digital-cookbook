package main

import "context"

// schemaSQL — схема event store и read-моделей.
//
// Ключевое место всего стенда — PRIMARY KEY(stream_id, version) на таблице events:
// именно он даёт оптимистичную блокировку. Две параллельные записи version=N в один
// стрим невозможны — вторая падает на нарушении PK (SQLSTATE 23505), и это сигнал
// «кто-то тебя опередил, перечитай и повтори». Никаких SELECT ... FOR UPDATE и
// блокировок строк — конкурентность разруливается самой уникальностью версии.
//
// global_pos (bigserial) — глобальный монотонный порядок событий через все стримы.
// По нему читают проекторы: «дай всё, что появилось после моего чекпоинта».
const schemaSQL = `
DROP TABLE IF EXISTS events, balances, balances_v2, snapshots, projection_checkpoint CASCADE;

-- Event store. Источник истины: события неизменяемы, только append.
CREATE TABLE events (
    stream_id   uuid        NOT NULL,          -- идентификатор агрегата (резервуар)
    version     bigint      NOT NULL,          -- версия внутри стрима, начиная с 1
    event_type  text        NOT NULL,
    schema_ver  int         NOT NULL DEFAULT 1,-- версия схемы события (для эволюции payload)
    data        jsonb       NOT NULL,
    metadata    jsonb       NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    global_pos  bigserial   NOT NULL,          -- глобальный порядок через все стримы
    PRIMARY KEY (stream_id, version)           -- <-- оптимистичная блокировка
);
CREATE INDEX events_global_pos_idx ON events (global_pos);

-- Read-модель: текущий уровень кислорода по каждому резервуару.
-- version — версия стрима, до которой проекция «доехала» (нужна для идемпотентности).
CREATE TABLE balances (
    stream_id uuid   PRIMARY KEY,
    level     double precision NOT NULL,       -- литров кислорода сейчас
    capacity  double precision NOT NULL,       -- ёмкость резервуара
    version   bigint NOT NULL
);

-- Цель blue-green rebuild: собирается реплеем всей истории, затем атомарно
-- переключается с balances.
CREATE TABLE balances_v2 (
    stream_id uuid   PRIMARY KEY,
    level     double precision NOT NULL,
    capacity  double precision NOT NULL,
    version   bigint NOT NULL
);

-- Снапшоты состояния агрегата на конкретной версии.
-- Загрузка агрегата = последний снапшот + хвост событий после него.
CREATE TABLE snapshots (
    stream_id  uuid        NOT NULL,
    version    bigint      NOT NULL,
    state      jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (stream_id, version)
);

-- Чекпоинты проекторов: до какого global_pos дочитал каждый проектор.
-- Обновляется В ТОЙ ЖЕ транзакции, что и запись в read-модель.
CREATE TABLE projection_checkpoint (
    name     text   PRIMARY KEY,
    last_pos bigint NOT NULL
);
`

// applySchema пересоздаёт схему начисто (стенд можно перезапускать сколько угодно).
func applySchema(ctx context.Context, db execer) error {
	_, err := db.Exec(ctx, schemaSQL)
	return err
}
