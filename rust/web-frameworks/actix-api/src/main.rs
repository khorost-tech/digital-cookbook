//! Actix-web: тот же REST-эндпоинт POST /users с валидацией, middleware и error handling.
//! Демо к статье «Rust web-фреймворки: Axum vs Actix». https://khorost.tech/rust/rust-web-frameworks/
//!
//! Запуск сервера:  cargo run -p actix-api    (слушает 0.0.0.0:8080)
//! Тесты handler'а: cargo test -p actix-api    (через actix_web::test, без реального порта)

use actix_cors::Cors;
use actix_web::{error::ResponseError, http::StatusCode, middleware::Logger, post, web, App, HttpResponse, HttpServer};
use serde::{Deserialize, Serialize};
use std::fmt;

#[derive(Deserialize)]
struct CreateUser {
    name: String,
}

#[derive(Serialize)]
struct User {
    id: i64,
    name: String,
}

/// Доменная ошибка → HTTP через трейт ResponseError (идиома Actix, аналог IntoResponse в Axum).
#[derive(Debug)]
enum AppError {
    EmptyName,
}

impl fmt::Display for AppError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "name must not be empty")
    }
}

impl ResponseError for AppError {
    fn status_code(&self) -> StatusCode {
        StatusCode::BAD_REQUEST
    }
}

#[post("/users")]
async fn create_user(payload: web::Json<CreateUser>) -> Result<HttpResponse, AppError> {
    if payload.name.is_empty() {
        return Err(AppError::EmptyName);
    }
    Ok(HttpResponse::Created().json(User { id: 1, name: payload.name.clone() }))
}

#[actix_web::main]
async fn main() -> std::io::Result<()> {
    println!("actix-api on http://0.0.0.0:8080  (POST /users {{\"name\":\"...\"}})");
    HttpServer::new(|| {
        App::new()
            .wrap(Logger::default()) // логирование запросов
            .wrap(Cors::permissive()) // CORS (demo: permissive; в проде — явный список)
            .service(create_user)
    })
    .bind(("0.0.0.0", 8080))?
    .run()
    .await
}

#[cfg(test)]
mod tests {
    use super::*;
    use actix_web::test;

    #[actix_web::test]
    async fn creates_user() {
        let app = test::init_service(App::new().service(create_user)).await;
        let req = test::TestRequest::post()
            .uri("/users")
            .set_json(serde_json::json!({"name": "alice"}))
            .to_request();
        let resp = test::call_service(&app, req).await;
        assert_eq!(resp.status(), StatusCode::CREATED);
    }

    #[actix_web::test]
    async fn rejects_empty_name() {
        let app = test::init_service(App::new().service(create_user)).await;
        let req = test::TestRequest::post()
            .uri("/users")
            .set_json(serde_json::json!({"name": ""}))
            .to_request();
        let resp = test::call_service(&app, req).await;
        assert_eq!(resp.status(), StatusCode::BAD_REQUEST);
    }
}
