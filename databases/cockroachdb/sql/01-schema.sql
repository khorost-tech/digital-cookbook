-- Базовая схема стенда.
--
-- accounts_* специально существуют в двух вариантах ключа: seq (монотонный,
-- воспроизводит hotspot) и uuid (разнесённый). Обе наполняются одинаковой
-- нагрузкой, чтобы сравнение в 03-txn было честным.

CREATE DATABASE IF NOT EXISTS bank;
USE bank;

CREATE TABLE IF NOT EXISTS accounts_seq (
    id      INT8 PRIMARY KEY DEFAULT unique_rowid(),
    owner   STRING NOT NULL,
    balance DECIMAL(14, 2) NOT NULL DEFAULT 0,
    updated TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS accounts_uuid (
    id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner   STRING NOT NULL,
    balance DECIMAL(14, 2) NOT NULL DEFAULT 0,
    updated TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Одна горячая строка для сценария contention.
CREATE TABLE IF NOT EXISTS hot_counter (
    id   INT8 PRIMARY KEY,
    hits INT8 NOT NULL DEFAULT 0
);

INSERT INTO hot_counter (id, hits) VALUES (1, 0) ON CONFLICT (id) DO NOTHING;
