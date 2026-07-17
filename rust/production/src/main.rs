//! Production-каркас Rust-сервиса к статье «Rust в production: паттерны для надёжного backend».
//! https://khorost.tech/rust/rust-production-patterns/
//!
//! Собирает вместе: конфиг из env (fail-fast), структурные JSON-логи, пул sqlx + миграции при
//! старте, liveness/readiness, graceful shutdown по SIGTERM/Ctrl-C.
//!
//! Запуск: docker compose up -d db && DATABASE_URL=postgres://demo:demo@localhost:5433/demo cargo run

mod config;
mod error;
mod routes;

use anyhow::Context;
use axum::{
    routing::{get, post},
    Router,
};
use sqlx::postgres::PgPoolOptions;
use tokio::signal;
use tracing_subscriber::{fmt, EnvFilter};

/// Ловим и SIGTERM (от оркестратора), и Ctrl-C — для with_graceful_shutdown.
async fn shutdown_signal() {
    let ctrl_c = async {
        signal::ctrl_c().await.ok();
    };
    #[cfg(unix)]
    let term = async {
        signal::unix::signal(signal::unix::SignalKind::terminate())
            .expect("install SIGTERM handler")
            .recv()
            .await;
    };
    #[cfg(not(unix))]
    let term = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {},
        _ = term => {},
    }
}

/// Роутер собран отдельной функцией — так его гоняет интеграционный тест без main().
fn build_app(pool: sqlx::PgPool) -> Router {
    Router::new()
        .route("/healthz", get(routes::healthz)) // liveness
        .route("/readyz", get(routes::readyz)) // readiness
        .route("/users", post(routes::create_user))
        .route("/users/{id}", get(routes::get_user))
        .with_state(pool)
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let cfg = config::Config::from_env().context("invalid configuration")?;

    // Структурные JSON-логи, уровень из конфига (RUST_LOG-совместимый фильтр).
    fmt()
        .json()
        .with_env_filter(EnvFilter::new(&cfg.rust_log))
        .init();

    // Пул с лимитом соединений + миграции из репозитория при старте.
    let pool = PgPoolOptions::new()
        .max_connections(10)
        .connect(&cfg.database_url)
        .await
        .context("connect to database")?;
    sqlx::migrate!("./migrations")
        .run(&pool)
        .await
        .context("run migrations")?;

    let app = build_app(pool);

    let addr = format!("0.0.0.0:{}", cfg.port);
    let listener = tokio::net::TcpListener::bind(&addr)
        .await
        .with_context(|| format!("bind {addr}"))?;
    tracing::info!(%addr, "listening");
    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal())
        .await
        .context("server error")?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::Body;
    use axum::http::{Request, StatusCode};
    use sqlx::postgres::PgPoolOptions;
    use testcontainers_modules::postgres::Postgres;
    use testcontainers_modules::testcontainers::runners::AsyncRunner;
    use tower::ServiceExt; // oneshot

    /// Интеграционный тест на реальном Postgres в контейнере (как в разделе про testcontainers):
    /// поднимает БД, накатывает миграции и гоняет эндпоинты через роутер без реального порта.
    #[tokio::test]
    async fn create_get_and_missing_user() {
        let node = Postgres::default().start().await.unwrap();
        let port = node.get_host_port_ipv4(5432).await.unwrap();
        let url = format!("postgres://postgres:postgres@127.0.0.1:{port}/postgres");

        let pool = PgPoolOptions::new().connect(&url).await.unwrap();
        sqlx::migrate!("./migrations").run(&pool).await.unwrap();
        let app = build_app(pool);

        // create → 201
        let resp = app
            .clone()
            .oneshot(
                Request::builder()
                    .method("POST")
                    .uri("/users")
                    .header("content-type", "application/json")
                    .body(Body::from(r#"{"name":"alice"}"#))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::CREATED);

        // get созданного → 200
        let resp = app
            .clone()
            .oneshot(Request::builder().uri("/users/1").body(Body::empty()).unwrap())
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::OK);

        // несуществующий → 404 в формате problem+json
        let resp = app
            .oneshot(Request::builder().uri("/users/999").body(Body::empty()).unwrap())
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::NOT_FOUND);
        assert_eq!(
            resp.headers().get("content-type").unwrap(),
            "application/problem+json"
        );
    }
}
