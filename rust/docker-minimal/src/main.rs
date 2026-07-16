//! Минимальный HTTP-сервис для демонстрации упаковки Rust в маленький Docker-образ.
//! Демо к статье «Rust в Docker: минимальные образы». https://khorost.tech/rust/rust-docker-minimal-images/
//!
//! Один эндпоинт GET /health → "ok". Смысл демо — не в сервисе, а в Dockerfile'ах:
//!   Dockerfile             — cargo-chef + musl-статика в scratch;
//!   Dockerfile.distroless  — glibc в distroless/cc.

use axum::{routing::get, Router};

async fn health() -> &'static str {
    "ok\n"
}

#[tokio::main]
async fn main() {
    let app = Router::new().route("/health", get(health));
    let listener = tokio::net::TcpListener::bind("0.0.0.0:8080").await.unwrap();
    println!("app on http://0.0.0.0:8080 (GET /health)");
    axum::serve(listener, app).await.unwrap();
}
