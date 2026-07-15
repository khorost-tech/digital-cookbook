# Rust web-фреймворки: Axum vs Actix

Companion-демо к статье [«Rust web-фреймворки: Axum vs Actix и выбор под задачу»](https://khorost.tech/rust/rust-web-frameworks/)
(серия «Rust для backend»).

**Один и тот же REST API** — `POST /users` с валидацией имени, middleware (логирование + CORS) и
error handling через доменную ошибку — реализован на двух фреймворках, чтобы сравнение было
предметным, а не на словах:

| Крейт | Фреймворк | Middleware | Ошибка → HTTP |
|-------|-----------|-----------|----------------|
| `axum-api`  | Axum 0.8 (на Tower/Hyper) | `tower-http`: `TraceLayer`, `CorsLayer` | тип + `IntoResponse` |
| `actix-api` | Actix-web 4              | `Logger`, `actix-cors`                  | трейт `ResponseError` |

## Запуск сервера

```bash
cargo run -p axum-api     # http://0.0.0.0:8080
cargo run -p actix-api    # http://0.0.0.0:8080 (останавливайте первый перед вторым — порт общий)
```

Проверка:

```bash
curl -s -XPOST localhost:8080/users -H 'content-type: application/json' -d '{"name":"alice"}'   # 201 Created
curl -s -XPOST localhost:8080/users -H 'content-type: application/json' -d '{"name":""}'         # 400 Bad Request
```

## Тесты

Оба handler'а покрыты тестами (создание + отказ на пустом имени), которые гоняют роутер **без
поднятия порта** — Axum через `tower::ServiceExt::oneshot`, Actix через `actix_web::test`:

```bash
cargo test        # 2 теста на крейт: creates_user, rejects_empty_name
```

## Проверка целиком

```bash
cargo test                              # поведение обоих API
cargo clippy --all-targets -- -D warnings   # линт без предупреждений
```

Проверено на стабильном Rust (edition 2021): `axum = "0.8"`, `actix-web = "4"`. Слой БД в демо не
поднимается (в комментариях показано, куда лёг бы пул `sqlx`) — фокус на различиях API фреймворков.
