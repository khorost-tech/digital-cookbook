use std::collections::HashMap;

use serde::Deserialize;
use shors::transport::{
    Context,
    http::{
        Request, Response,
        route::{Builder, Route},
    },
};

use crate::handlers::endpoints::top_products::{TopRequest, top_products};

/// Параметры строки запроса `?category=tools&limit=5`.
#[derive(Deserialize)]
struct TopQuery {
    category: String,
    limit: u32,
}

#[must_use]
pub fn routes() -> Vec<Route<anyhow::Error>> {
    let hello_route = Builder::new().with_method("GET").with_path("/hello").build(
        |_: &mut Context, _: Request| -> anyhow::Result<_> {
            let message: &str = "Hello there! This is Pike. Use cargo pike --help for more tips.";
            Ok(Response {
                status: 200,
                headers: HashMap::from([(
                    "content-type".to_string(),
                    "text/plain; charset=utf8".to_string(),
                )]),
                body: message.as_bytes().to_vec(),
            })
        },
    );

    // Тот же расчёт, что и за RPC-путём `/top_products`, но по HTTP: так
    // самопроверка стенда обходится curl и не тянет отдельный клиент. Логика
    // общая — обе точки входа зовут top_products().
    let top_route = Builder::new()
        .with_method("GET")
        .with_path("/top_products")
        .build(|_: &mut Context, request: Request| -> anyhow::Result<_> {
            let q: TopQuery = request.query()?;
            let response = top_products(&TopRequest {
                category: q.category,
                limit: q.limit,
            })?;
            Ok(Response {
                status: 200,
                headers: HashMap::from([(
                    "content-type".to_string(),
                    "application/json; charset=utf8".to_string(),
                )]),
                body: serde_json::to_vec(&response)?,
            })
        });

    vec![hello_route, top_route]
}
