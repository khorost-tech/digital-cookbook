//! ПРИЁМ 6: Testcontainers — настоящий Postgres вместо фейка.
//!
//! Тот же образ postgres:18.1-alpine, что в стенде across-languages, и та же
//! проверка: доезжает ли заказ до диска через SQL и отклоняет ли база дубль ID.
//! Фейк из юнит-тестов про первичный ключ не знает вовсе — он с радостью примет
//! второй заказ с тем же id.
//!
//!     cargo test --test integration_pg -- --ignored     # нужен Docker
//!
//! Помечено #[ignore], потому что требует Docker и стоит секунды: `cargo test`
//! по умолчанию должен оставаться быстрым.

use rust_testing::money::Money;
use rust_testing::service::Order;
use testcontainers::core::{IntoContainerPort, WaitFor};
use testcontainers::runners::AsyncRunner;
use testcontainers::{GenericImage, ImageExt};
use tokio_postgres::NoTls;

const SCHEMA: &str = "
CREATE TABLE IF NOT EXISTS orders (
    id         TEXT PRIMARY KEY,
    user_id    TEXT   NOT NULL,
    total_cent BIGINT NOT NULL,
    price_cent BIGINT NOT NULL
)";

async fn save(client: &tokio_postgres::Client, o: &Order) -> Result<(), tokio_postgres::Error> {
    client
        .execute(
            "INSERT INTO orders (id, user_id, total_cent, price_cent) VALUES ($1, $2, $3, $4)",
            &[&o.id, &o.user_id, &o.total.cents(), &o.price.cents()],
        )
        .await?;
    Ok(())
}

#[tokio::test]
#[ignore = "нужен Docker: cargo test --test integration_pg -- --ignored"]
async fn заказ_доезжает_до_postgres_и_дубль_отклоняется() {
    let container = GenericImage::new("postgres", "18.1-alpine")
        .with_wait_for(WaitFor::message_on_stderr(
            "database system is ready to accept connections",
        ))
        .with_env_var("POSTGRES_USER", "test")
        .with_env_var("POSTGRES_PASSWORD", "test")
        .with_env_var("POSTGRES_DB", "orders")
        .start()
        .await
        .expect("старт контейнера Postgres");

    let port = container
        .get_host_port_ipv4(5432.tcp())
        .await
        .expect("проброшенный порт");
    let dsn = format!("host=127.0.0.1 port={port} user=test password=test dbname=orders");

    let (client, conn) = tokio_postgres::connect(&dsn, NoTls)
        .await
        .expect("подключение");
    tokio::spawn(async move {
        let _ = conn.await;
    });
    client.batch_execute(SCHEMA).await.expect("схема");

    let order = Order {
        id: "o-1".into(),
        user_id: "u-1".into(),
        total: Money::try_new(25_000).unwrap(),
        price: Money::try_new(22_500).unwrap(),
    };

    // 1. Заказ доезжает до диска и читается обратно.
    save(&client, &order).await.expect("первый save");
    let rows = client
        .query(
            "SELECT price_cent FROM orders WHERE user_id = $1",
            &[&"u-1"],
        )
        .await
        .expect("выборка");
    assert_eq!(rows.len(), 1, "ожидали один заказ");
    assert_eq!(rows[0].get::<_, i64>(0), 22_500, "250.00 минус 10%");

    // 2. А это фейк не проверяет вовсе: первичный ключ — знание базы, не наше.
    let dup = save(&client, &order).await;
    assert!(
        dup.is_err(),
        "повторный save с тем же ID должен отклоняться первичным ключом"
    );
}
