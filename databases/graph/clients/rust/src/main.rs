//! Демо Rust-клиента: гоняет пять графовых вопросов на AGE и baseline (оба через
//! PostgreSQL) и печатает результаты. Показывает, что graph-in-PG работает через
//! обычный `tokio-postgres`, а ответы совпадают с Go/Java.
//!
//! Переменные окружения: GRAPH_BACKEND (age|baseline|all), PG_CONN.

use graph_client_rust::queries::{Backend, PgQueries};

#[tokio::main]
async fn main() {
    let backend = std::env::var("GRAPH_BACKEND").unwrap_or_else(|_| "all".into());
    let conn = std::env::var("PG_CONN").unwrap_or_else(|_| {
        "host=localhost port=5432 user=graph password=graph dbname=graphlab".into()
    });

    let backends: Vec<Backend> = match backend.as_str() {
        "age" => vec![Backend::Age],
        "baseline" => vec![Backend::Baseline],
        _ => vec![Backend::Age, Backend::Baseline],
    };

    for b in backends {
        match PgQueries::connect(&conn, b).await {
            Ok(q) => run_demo(&q).await,
            Err(e) => eprintln!("[{}] подключение: {}", name(b), e),
        }
    }
}

fn name(b: Backend) -> &'static str {
    match b {
        Backend::Age => "age",
        Backend::Baseline => "baseline",
    }
}

async fn run_demo(q: &PgQueries) {
    let b = q.backend_name();
    println!("\n=== backend: {} ===", b);

    match q.accessible_resources(1).await {
        Ok(ids) => report(b, "accessible_resources(user=1)", &ids),
        Err(e) => eprintln!("[{}] accessible_resources: {}", b, e),
    }

    let cyc = q.cyclic_services(15).await.unwrap_or_default();
    report(b, "cyclic_services(maxLen=15)", &cyc);

    if let Some(&s) = cyc.first() {
        match q.impact_of(s, 10).await {
            Ok(ids) => report(b, &format!("impact_of(service={}, depth=10)", s), &ids),
            Err(e) => eprintln!("[{}] impact_of: {}", b, e),
        }
    }

    match q.distance(1, 2, 6).await {
        Ok(d) => println!("[{}] distance(1,2, maxLen=6) = {}", b, d),
        Err(e) => eprintln!("[{}] distance: {}", b, e),
    }

    match q.ring_members(4).await {
        Ok(ids) => report(b, "ring_members(length=4)", &ids),
        Err(e) => eprintln!("[{}] ring_members: {}", b, e),
    }
}

fn report(backend: &str, name: &str, ids: &[i64]) {
    let sample: Vec<i64> = ids.iter().take(5).copied().collect();
    println!(
        "[{}] {:<34} → {} шт., пример {:?}",
        backend,
        name,
        ids.len(),
        sample
    );
}
