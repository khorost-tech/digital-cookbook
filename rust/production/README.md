# Rust в production: каркас надёжного сервиса

Companion-демо к статье [«Rust в production: паттерны для надёжного backend»](https://khorost.tech/rust/rust-production-patterns/)
(финал серии «Rust для backend»).

Минимальный, но «взрослый» Axum-сервис, собирающий вместе production-паттерны из статьи:

| Паттерн | Где |
|---------|-----|
| Конфиг из env + fail-fast | `src/config.rs` (`envy`, обязательный `DATABASE_URL`) |
| Структурные JSON-логи | `src/main.rs` (`tracing` + `tracing-subscriber` json, фильтр из env) |
| Пул БД + миграции при старте | `src/main.rs` (`sqlx::PgPool`, `sqlx::migrate!`) |
| SQL с проверкой на компиляции | `src/routes.rs` (`sqlx::query_as!` сверяется со схемой) |
| liveness / readiness | `GET /healthz` (процесс) / `GET /readyz` (`SELECT 1` к БД) |
| Error handling | `src/error.rs` (`thiserror`/`anyhow` → problem+json, внутренние ошибки не утекают) |
| Graceful shutdown | `src/main.rs` (`SIGTERM` + `Ctrl-C` → `with_graceful_shutdown`) |

## Сборка (без БД)

Метаданные SQL-запросов закешированы в `.sqlx/`, а `.cargo/config.toml` включает `SQLX_OFFLINE`,
поэтому проект **компилируется без живого Postgres**:

```bash
cargo build
cargo clippy --all-targets -- -D warnings
```

## Запуск (с БД)

```bash
docker compose up -d db                              # Postgres :5433 (demo-креды)
export DATABASE_URL=postgres://demo:demo@localhost:5433/demo
cargo run                                            # миграции применятся при старте
```

Проверка эндпоинтов:

```bash
curl -s localhost:8080/healthz                                                   # ok (liveness)
curl -s localhost:8080/readyz                                                    # ready (readiness, ходит в БД)
curl -s -XPOST localhost:8080/users -H 'content-type: application/json' -d '{"name":"alice"}'  # 201
curl -s localhost:8080/users/1                                                   # {"id":1,"name":"alice"}
curl -s -XPOST localhost:8080/users -H 'content-type: application/json' -d '{"name":""}'  # 400 application/problem+json
```

Ошибки отдаются в формате [RFC 7807](https://www.rfc-editor.org/rfc/rfc7807) (`application/problem+json`,
поля `type`/`title`/`status`/`detail`); внутренние ошибки логируются, но наружу уходит generic-сообщение.

## Тесты

Интеграционный тест поднимает **реальный Postgres в контейнере** (`testcontainers`), накатывает
миграции и гоняет эндпоинты через роутер без реального порта — создание (201), чтение (200) и
`problem+json` на отсутствующем (404). Нужен запущенный Docker:

```bash
cargo test        # tests::create_get_and_missing_user (поднимает Postgres на время теста)
```

## Границы демо: наблюдаемость

Демо ограничено **структурными JSON-логами** через `tracing`/`tracing-subscriber`. OTLP-экспорт в
OpenTelemetry Collector (как в статье) намеренно не включён: API `opentelemetry-rust` заметно меняется
между релизами, и привязка к конкретным версиям быстро устарела бы. Обвязка добавляется поверх того же
`tracing` — крейты `opentelemetry`, `opentelemetry_sdk`, `opentelemetry-otlp`, `tracing-opentelemetry`
собирают `Registry` из fmt-слоя и `tracing_opentelemetry::layer()` с OTLP-экспортёром; точные сигнатуры
берите из актуальной документации крейтов.

## Перегенерация sqlx-кеша (при изменении запросов)

`query!`/`query_as!` проверяются против **реальной** схемы. Чтобы обновить `.sqlx/`:

```bash
cargo install sqlx-cli --no-default-features --features postgres,native-tls   # один раз
docker compose up -d db
export DATABASE_URL=postgres://demo:demo@localhost:5433/demo
sqlx migrate run                 # накатить схему
cargo sqlx prepare               # записать метаданные запросов в .sqlx/
```

## Demo-only

`docker-compose.yml` и `.env.example` содержат demo-креды (`demo`/`demo`) — только для локального
стенда. sqlx собран без TLS (локальный Postgres, plain TCP); в проде добавьте `tls-native-tls`/
`tls-rustls` и `sslmode=require`. Минимальный образ сервиса — в
[`rust/docker-minimal`](../docker-minimal).

Проверено на стабильном Rust (edition 2021): `axum = "0.8"`, `sqlx = "0.8"`, Postgres 17.
