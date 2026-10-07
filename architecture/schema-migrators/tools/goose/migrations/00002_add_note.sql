-- +goose Up
ALTER TABLE probe ADD COLUMN note TEXT;

-- +goose Down
ALTER TABLE probe DROP COLUMN note;
