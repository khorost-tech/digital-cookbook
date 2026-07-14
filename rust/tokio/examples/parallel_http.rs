//! Сетевой аналог к демо: параллельные HTTP-запросы с таймаутом на каждый (как в статье).
//! Требует доступа в сеть.  Запуск:  cargo run --example parallel_http
//!
//! tokio::join! опрашивает оба запроса конкурентно в одной задаче; timeout отменяет
//! операцию, если та не уложилась в срок (future дропается).

use std::time::Duration;
use tokio::time::timeout;

async fn fetch(client: &reqwest::Client, url: &str) -> anyhow::Result<usize> {
    // Таймаут на отдельную операцию: future отменяется, если не успел.
    let resp = timeout(Duration::from_secs(2), client.get(url).send()).await??;
    let body = resp.text().await?;
    Ok(body.len())
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let client = reqwest::Client::new();
    let urls = ["https://example.com", "https://rust-lang.org"];

    // join! опрашивает все future конкурентно в одной задаче.
    let (a, b) = tokio::join!(fetch(&client, urls[0]), fetch(&client, urls[1]));
    println!("{:?} {:?}", a, b);
    Ok(())
}
