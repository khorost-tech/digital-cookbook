-- +goose Up
CREATE TABLE broken (id INT PRIMARY KEY,, name TEXT);

-- +goose Down
DROP TABLE broken;
