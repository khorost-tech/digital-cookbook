DSN ?= postgres://cookbook:cookbook@localhost:5432/cookbook?sslmode=disable

.PHONY: up down migrate gen run-gorm run-jet tidy

up:
	docker compose up -d

down:
	docker compose down -v

migrate:
	docker compose exec -T db psql -U cookbook -d cookbook < migrations/0001_init.sql

# Кодоген go-jet из живой схемы. Требует установленный jet:
#   go install github.com/go-jet/jet/v2/cmd/jet@latest
gen:
	jet -dsn="$(DSN)" -schema=public -path=./jet/.gen

run-gorm:
	go run ./gorm

run-jet:
	go run ./jet

tidy:
	go mod tidy
