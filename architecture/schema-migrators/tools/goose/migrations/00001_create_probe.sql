-- +goose Up
CREATE TABLE probe (id INT PRIMARY KEY, name TEXT);

-- +goose Down
DROP TABLE probe;
