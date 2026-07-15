//! Axum: REST-эндпоинт POST /users с состоянием, валидацией, middleware и error handling.
//! Демо к статье «Rust web-фреймворки: Axum vs Actix». https://khorost.tech/rust/rust-web-frameworks/
//!
//! Запуск сервера:  cargo run -p axum-api    (слушает 0.0.0.0:8080)
//! Тесты handler'а: cargo test -p axum-api    (через tower oneshot, без поднятия порта)

use axum::{
    extract::State,
    http::StatusCode,
    response::{IntoResponse, Response},
    routing::post,
    Json, Router,
};
use serde::{Deserialize, Serialize};
use std::sync::Arc;
use tower_http::{cors::CorsLayer, trace::TraceLayer};

#[derive(Clone, Default)]
struct AppState {
    // сюда лёг бы пул БД, напр. db: sqlx::PgPool
}

#[derive(Deserialize)]
struct CreateUser {
    name: String,
}

#[derive(Serialize)]
struct User {
    id: i64,
    name: String,
}

/// Доменная ошибка сама решает, в какой HTTP-ответ превратиться (идиома Axum: тип + IntoResponse).
enum AppError {
    EmptyName,
}

impl IntoResponse for AppError {
    fn into_response(self) -> Response {
        match self {
            AppError::EmptyName => (StatusCode::BAD_REQUEST, "name must not be empty").into_response(),
        }
    }
}

async fn create_user(
    State(_state): State<Arc<AppState>>,
    Json(payload): Json<CreateUser>,
) -> Result<(StatusCode, Json<User>), AppError> {
    if payload.name.is_empty() {
        return Err(AppError::EmptyName);
    }
    Ok((StatusCode::CREATED, Json(User { id: 1, name: payload.name })))
}

/// Роутер собран отдельной функцией — так его гоняют тесты без поднятия сокета.
fn app(state: Arc<AppState>) -> Router {
    Router::new()
        .route("/users", post(create_user))
        .layer(TraceLayer::new_for_http()) // логирование/трейсинг запросов (tower-http)
        .layer(CorsLayer::permissive()) // CORS (demo: permissive; в проде — явный список origin)
        .with_state(state)
}

#[tokio::main]
async fn main() {
    let state = Arc::new(AppState::default());
    let listener = tokio::net::TcpListener::bind("0.0.0.0:8080").await.unwrap();
    println!("axum-api on http://0.0.0.0:8080  (POST /users {{\"name\":\"...\"}})");
    axum::serve(listener, app(state))
        .with_graceful_shutdown(async {
            tokio::signal::ctrl_c().await.ok();
        })
        .await
        .unwrap();
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::Body;
    use axum::http::Request;
    use tower::ServiceExt; // oneshot

    fn post_users(body: &'static str) -> Request<Body> {
        Request::builder()
            .method("POST")
            .uri("/users")
            .header("content-type", "application/json")
            .body(Body::from(body))
            .unwrap()
    }

    #[tokio::test]
    async fn creates_user() {
        let resp = app(Arc::new(AppState::default()))
            .oneshot(post_users(r#"{"name":"alice"}"#))
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::CREATED);
    }

    #[tokio::test]
    async fn rejects_empty_name() {
        let resp = app(Arc::new(AppState::default()))
            .oneshot(post_users(r#"{"name":""}"#))
            .await
            .unwrap();
        assert_eq!(resp.status(), StatusCode::BAD_REQUEST);
    }
}
