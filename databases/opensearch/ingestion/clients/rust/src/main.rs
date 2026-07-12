// Демо opensearch-rs 2.4.0: index + bulk (serde/async). Проверено на OpenSearch 3.5.0.
// demo-креды — только для локального стенда.
use opensearch::auth::Credentials;
use opensearch::cert::CertificateValidation;
use opensearch::http::request::JsonBody;
use opensearch::http::transport::{SingleNodeConnectionPool, TransportBuilder};
use opensearch::{BulkParts, IndexParts, OpenSearch};
use serde::Serialize;
use serde_json::{json, Value};
use url::Url;

#[derive(Serialize)]
struct LogDoc {
    ts: &'static str,
    level: &'static str,
    service: &'static str,
    message: &'static str,
    status: u16,
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // По умолчанию — localhost (клиент запускается на хосте рядом со стендом). Для запуска
    // ИЗ контейнера, которому нужен хостовый стенд, задайте OS_HOST=host.docker.internal.
    let os_host = std::env::var("OS_HOST").unwrap_or_else(|_| "localhost".to_string());
    let url = Url::parse(&format!("https://{os_host}:9214"))?;
    let pool = SingleNodeConnectionPool::new(url);
    let transport = TransportBuilder::new(pool)
        .auth(Credentials::Basic("admin".into(), "IngDemo#2026".into()))
        .cert_validation(CertificateValidation::None) // demo only
        .build()?;
    let client = OpenSearch::new(transport);

    // single index
    let doc = LogDoc { ts: "2026-07-03T10:00:00Z", level: "error", service: "service-a", message: "disk full", status: 500 };
    let resp = client
        .index(IndexParts::Index("app-logs-rust"))
        .body(doc)
        .send()
        .await?;
    println!("single index -> status={}", resp.status_code());

    // bulk (action-строка + документ-строка)
    let docs = [
        LogDoc { ts: "2026-07-03T10:01:00Z", level: "info", service: "service-a", message: "ok", status: 200 },
        LogDoc { ts: "2026-07-03T10:02:00Z", level: "warn", service: "service-b", message: "retry", status: 429 },
    ];
    let mut body: Vec<JsonBody<Value>> = Vec::new();
    for d in &docs {
        body.push(json!({"index": {}}).into());
        body.push(serde_json::to_value(d)?.into());
    }
    let resp = client.bulk(BulkParts::Index("app-logs-rust")).body(body).send().await?;
    let out: Value = resp.json().await?;
    // HTTP-успех != все записаны: проверяем errors и каждый item.
    println!("bulk -> errors={}", out["errors"]);
    for (i, item) in out["items"].as_array().unwrap().iter().enumerate() {
        println!("  item {} status={}", i, item["index"]["status"]);
    }
    Ok(())
}
