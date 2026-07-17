//! Роуты: liveness/readiness health-checks + users CRUD на sqlx с проверкой SQL на компиляции.

use axum::{
    extract::{Path, State},
    http::StatusCode,
    Json,
};
use serde::{Deserialize, Serialize};
use sqlx::PgPool;

use crate::error::AppError;

#[derive(Serialize)]
pub struct User {
    pub id: i64,
    pub name: String,
}

#[derive(Deserialize)]
pub struct CreateUser {
    pub name: String,
}

/// liveness: «процесс жив», без проверки зависимостей — иначе кратковременный сбой БД
/// перезапустил бы все поды разом.
pub async fn healthz() -> &'static str {
    "ok\n"
}

/// readiness: «готов принимать трафик» — проверяет критичную зависимость (БД).
pub async fn readyz(State(pool): State<PgPool>) -> Result<&'static str, AppError> {
    sqlx::query!("SELECT 1 AS one").fetch_one(&pool).await?;
    Ok("ready\n")
}

pub async fn create_user(
    State(pool): State<PgPool>,
    Json(body): Json<CreateUser>,
) -> Result<(StatusCode, Json<User>), AppError> {
    if body.name.is_empty() {
        return Err(AppError::Validation("name must not be empty".into()));
    }
    // query_as! сверяет запрос со схемой реальной базы на этапе компиляции.
    let user = sqlx::query_as!(
        User,
        "INSERT INTO users (name) VALUES ($1) RETURNING id, name",
        body.name
    )
    .fetch_one(&pool)
    .await?;
    Ok((StatusCode::CREATED, Json(user)))
}

pub async fn get_user(
    State(pool): State<PgPool>,
    Path(id): Path<i64>,
) -> Result<Json<User>, AppError> {
    let user = sqlx::query_as!(User, "SELECT id, name FROM users WHERE id = $1", id)
        .fetch_optional(&pool)
        .await?
        .ok_or(AppError::NotFound)?;
    Ok(Json(user))
}
