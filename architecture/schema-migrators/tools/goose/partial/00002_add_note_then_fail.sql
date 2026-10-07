-- +goose Up
ALTER TABLE probe ADD COLUMN note TEXT;
INSERT INTO missing_table VALUES (1);

-- +goose Down
ALTER TABLE probe DROP COLUMN note;
