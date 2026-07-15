-- Схема стенда №1: счёт для сценария lost update и дежурства для write skew.
CREATE TABLE IF NOT EXISTS accounts (
    id      BIGINT PRIMARY KEY,
    balance BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS on_call (
    doctor  TEXT PRIMARY KEY,
    on_duty BOOLEAN NOT NULL
);
