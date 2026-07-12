# go/orm-gorm-vs-jet

Запускаемый пример к статье [«ORM в Go: где он уместен и что выбрать между GORM и go-jet»](https://khorost.tech/go/go-orm-gorm-vs-go-jet/).

Один и тот же домен (`users`, `posts` с `metadata jsonb` и счётчиками, `tags` many-to-many) и одни и те же пять операций — в **GORM** и в **go-jet**:

1. CRUD;
2. список пользователей с постами (Preload / N+1 vs LEFT JOIN + вложенный маппинг);
3. агрегат `COUNT / GROUP BY`;
4. фильтр по `jsonb`;
5. partial update, где важны нулевые значения (`likes = 0`).

## Структура

```
migrations/0001_init.sql   схема домена
gorm/main.go               те же 5 кейсов на GORM (компилируется как есть)
jet/main.go                те же 5 кейсов на go-jet (нужен codegen — см. ниже)
docker-compose.yml         PostgreSQL 16
Makefile                   up / migrate / gen / run-*
```

## Запуск

```bash
make up                 # поднять PostgreSQL
make migrate            # применить schema из migrations/

# GORM — работает сразу:
make run-gorm

# go-jet — сначала кодоген из живой схемы, потом запуск:
make gen                # jet -dsn=... -schema=public -path=./jet/.gen
make run-jet
```

## Про go-jet и codegen

go-jet **генерирует** пакеты `table`/`model` из реальной схемы БД (`make gen`), поэтому `jet/main.go` компилируется только **после** `make gen` — сгенерированный код лежит в `jet/.gen/` (в git не коммитится). Это и есть суть SQL-first: источник истины — схема, а не Go-структуры.

DSN по умолчанию: `postgres://cookbook:cookbook@localhost:5432/cookbook?sslmode=disable`.

## Замечания

- `AutoMigrate` в GORM-примере — только для демо; в проде используйте нормальный мигратор (здесь схема применяется из `migrations/`).
- go-jet исполняется через `database/sql`; с `pgx` — в режиме `stdlib` (`github.com/jackc/pgx/v5/stdlib`), а не нативный `pgxpool`.
