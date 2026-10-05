-- Схема намеренно минимальна и идентична для SQLite и PostgreSQL:
-- любое различие в схеме стало бы ещё одной переменной в сравнении.
CREATE TABLE IF NOT EXISTS events (
    id      INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL,
    amount  INTEGER NOT NULL,
    payload TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_user ON events(user_id);
