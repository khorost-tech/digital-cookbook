// Минимальный: axum — роутер поверх hyper и tower, экстракторы Path и Json.
use axum::{extract::Path, http::StatusCode, routing::{get, post}, Json, Router};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

#[derive(Deserialize, Serialize)]
struct Item {
    name: String,
    #[serde(default)]
    qty: i64,
}

async fn get_item(Path(id): Path<i64>) -> Json<Value> {
    Json(json!({"id": id, "name": "item"}))
}

async fn create(Json(it): Json<Item>) -> (StatusCode, Json<Item>) {
    (StatusCode::CREATED, Json(it))
}

async fn boom() -> &'static str {
    panic!("boom: секрет в сообщении marker-7f3a9c")
}

#[tokio::main]
async fn main() {
    let app = Router::new().route("/items/{id}", get(get_item)).route("/items", post(create)).route("/boom", get(boom));
    let listener = tokio::net::TcpListener::bind("0.0.0.0:8080").await.unwrap();
    axum::serve(listener, app).await.unwrap();
}
