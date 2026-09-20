use picotest::*;
use reqwest::blocking as req;
use serde::{Deserialize, Serialize};

/// Имя сервиса, под которым плагин регистрируется в `lib.rs`. Держать его здесь
/// константой, а не строковым литералом по месту: рассинхронизация с реестром
/// даёт тест, который обращается к несуществующему сервису и падает по причине,
/// не имеющей отношения к проверяемому поведению.
const SERVICE: &str = "near_data_service";

#[derive(Serialize, Deserialize, Debug)]
pub struct User {
    name: String,
}

#[derive(Serialize, Deserialize, Debug)]
pub struct ExampleResponse {
    rpc_hello_response: String,
}

#[derive(Serialize, Deserialize, Debug)]
pub struct TopRequest {
    category: String,
    limit: u32,
}

#[derive(Serialize, Deserialize, Debug)]
pub struct ProductRow {
    id: u64,
    title: String,
    views: u64,
}

#[derive(Serialize, Deserialize, Debug)]
pub struct TopResponse {
    items: Vec<ProductRow>,
    served_by_version: String,
}

#[picotest]
fn test_cluster_handles() {
    let http_port = cluster.main().http_port;

    let resp = req::get(format!("http://127.0.0.1:{http_port}/metrics")).unwrap();
    assert!(resp.status().is_success());

    let resp = req::get(format!("http://127.0.0.1:{http_port}/hello")).unwrap();
    assert!(resp.status().is_success());
}

#[tokio::test]
#[picotest]
async fn test_rpc_handle() {
    let user_to_send = User {
        name: "Dodo".to_string(),
    };

    let response = cluster
        .main()
        .execute_rpc::<User, ExampleResponse>(
            env!("CARGO_PKG_NAME"),
            "/greetings_rpc",
            SERVICE,
            env!("CARGO_PKG_VERSION"),
            &user_to_send,
        )
        .await
        .unwrap();

    assert_eq!(response.rpc_hello_response, "Hello Dodo, long time no see.");
}

/// Проверяет не факт ответа, а КОРРЕКТНОСТЬ результата: отбор по категории,
/// порядок по убыванию просмотров и тай-брейк по убыванию id — ровно те
/// свойства, ради которых обработчик и написан.
#[tokio::test]
#[picotest]
async fn test_top_products_orders_and_limits() {
    // Данные вставляются тестом: миграция создаёт таблицу пустой, а проверять
    // порядок можно только на заранее известном наборе.
    for (id, title, category, views) in [
        (1_u64, "tools a", "tools", 10_u64),
        (2, "tools b", "tools", 30),
        (3, "tools c", "tools", 30),
        (4, "garden d", "garden", 99),
    ] {
        cluster
            .run_query(format!(
                "INSERT INTO products (id, sku, title, price_cents, category, views) \
                 VALUES ({id}, 'sku-{id}', '{title}', 100, '{category}', {views})"
            ))
            .unwrap_or_else(|err| panic!("вставка строки {id}: {err}"));
    }

    let response = cluster
        .main()
        .execute_rpc::<TopRequest, TopResponse>(
            env!("CARGO_PKG_NAME"),
            "/top_products",
            SERVICE,
            env!("CARGO_PKG_VERSION"),
            &TopRequest {
                category: "tools".to_string(),
                limit: 2,
            },
        )
        .await
        .unwrap();

    assert_eq!(response.items.len(), 2, "limit должен ограничивать выдачу");

    // Чужая категория не должна попасть в выдачу.
    assert!(
        response.items.iter().all(|r| r.title.starts_with("tools")),
        "в выдаче оказалась строка чужой категории: {:?}",
        response.items
    );

    // Порядок: views по убыванию, при равенстве — id по убыванию.
    // Здесь у id=2 и id=3 одинаковые views, значит первым обязан идти id=3.
    assert_eq!(response.items[0].id, 3, "тай-брейк по убыванию id нарушен");
    assert_eq!(response.items[1].id, 2);
    assert!(response.items[0].views >= response.items[1].views);

    assert_eq!(response.served_by_version, env!("CARGO_PKG_VERSION"));
}
