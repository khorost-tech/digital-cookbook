# musl-статический бинарник в scratch: самый компактный вариант (см. раздел статьи о cargo-chef).
# Сборка: docker build -t rust-app:musl .
#
# cargo-chef отделяет слой зависимостей от кода: пока recipe.json (из Cargo.lock) не менялся,
# зависимости берутся из Docker-кеша, а пересобирается только ваш код.
FROM rust:1-alpine AS chef
RUN apk add --no-cache musl-dev && cargo install cargo-chef --locked
WORKDIR /app

FROM chef AS planner
COPY . .
RUN cargo chef prepare --recipe-path recipe.json

FROM chef AS builder
COPY --from=planner /app/recipe.json recipe.json
# Слой зависимостей кешируется, пока recipe.json не изменился:
RUN cargo chef cook --release --target x86_64-unknown-linux-musl --recipe-path recipe.json
COPY . .
RUN cargo build --release --target x86_64-unknown-linux-musl

FROM scratch
# Статический бинарник + CA-сертификаты (в scratch их нет — иначе исходящий HTTPS упадёт):
COPY --from=builder /app/target/x86_64-unknown-linux-musl/release/app /app
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
USER 1000:1000
EXPOSE 8080
ENTRYPOINT ["/app"]
