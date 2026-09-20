-- +goose Up
CREATE TABLE migration_probe (
    id UNSIGNED NOT NULL,
    title TEXT NOT NULL,
    PRIMARY KEY (id)
)
USING memtx
DISTRIBUTED BY (id);

-- +goose Down
DROP TABLE migration_probe;
