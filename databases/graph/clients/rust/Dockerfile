# Rust-клиент стенда: только PostgreSQL (AGE + baseline) через tokio-postgres.
# Multi-stage: сборка release-бинаря, запуск в slim-образе.
FROM rust:1-slim-bookworm AS build
WORKDIR /src
COPY Cargo.toml Cargo.lock ./
COPY src ./src
COPY tests ./tests
RUN cargo build --release --bin graph-client

FROM debian:bookworm-slim
RUN useradd -u 10001 -m app
USER app
COPY --from=build /src/target/release/graph-client /graph-client
ENTRYPOINT ["/graph-client"]
