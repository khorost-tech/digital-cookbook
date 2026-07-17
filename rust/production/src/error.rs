//! Доменная ошибка (thiserror) → HTTP-ответ (IntoResponse). Внутренние ошибки логируются, но
//! наружу отдаётся generic-сообщение; тело — единым форматом (в духе RFC 7807 problem+json).

use axum::{
    http::StatusCode,
    response::{IntoResponse, Response},
    Json,
};
use thiserror::Error;

#[derive(Error, Debug)]
pub enum AppError {
    #[error("not found")]
    NotFound,
    #[error("invalid input: {0}")]
    Validation(String),
    #[error(transparent)]
    Internal(#[from] anyhow::Error),
}

// Ошибки sqlx — внутренние: заворачиваем в Internal (наружу не утекают).
impl From<sqlx::Error> for AppError {
    fn from(e: sqlx::Error) -> Self {
        AppError::Internal(e.into())
    }
}

impl IntoResponse for AppError {
    fn into_response(self) -> Response {
        let (status, title, detail) = match &self {
            AppError::NotFound => (StatusCode::NOT_FOUND, "Not Found", self.to_string()),
            AppError::Validation(m) => (StatusCode::BAD_REQUEST, "Validation Failed", m.clone()),
            AppError::Internal(e) => {
                // Внутренние ошибки логируем, но наружу — только generic-сообщение.
                tracing::error!(error = ?e, "internal error");
                (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "Internal Server Error",
                    "internal error".to_string(),
                )
            }
        };
        // RFC 7807: тело с type/title/status/detail и Content-Type: application/problem+json.
        let body = serde_json::json!({
            "type": "about:blank",
            "title": title,
            "status": status.as_u16(),
            "detail": detail,
        });
        let mut resp = (status, Json(body)).into_response();
        resp.headers_mut().insert(
            axum::http::header::CONTENT_TYPE,
            axum::http::HeaderValue::from_static("application/problem+json"),
        );
        resp
    }
}
