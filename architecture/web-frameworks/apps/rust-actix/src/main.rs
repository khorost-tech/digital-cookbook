// Полный: actix-web — свой рантайм акторов, экстракторы, middleware, лимиты тела из коробки.
use actix_web::{get, post, web, App, HttpResponse, HttpServer, Responder};
use serde::{Deserialize, Serialize};
use serde_json::json;

#[derive(Deserialize, Serialize)]
struct Item {
    name: String,
    #[serde(default)]
    qty: i64,
}

#[get("/items/{id}")]
async fn get_item(id: web::Path<i64>) -> impl Responder {
    HttpResponse::Ok().json(json!({"id": id.into_inner(), "name": "item"}))
}

#[post("/items")]
async fn create(it: web::Json<Item>) -> impl Responder {
    HttpResponse::Created().json(it.into_inner())
}

#[get("/boom")]
async fn boom() -> impl Responder {
    panic!("boom: секрет в сообщении marker-7f3a9c");
    #[allow(unreachable_code)]
    HttpResponse::Ok().finish()
}

#[actix_web::main]
async fn main() -> std::io::Result<()> {
    HttpServer::new(|| App::new().service(get_item).service(create).service(boom)).bind(("0.0.0.0", 8080))?.run().await
}
