//! Демонстрационный клиент redis-rs: read-through кэш профиля в бесконечном цикле.
//! REDIS_MODE=cluster|sentinel. Клиент не падает на ошибке — логирует и продолжает,
//! чтобы было видно окно недоступности и reconnect при failover.

use std::env;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use redis::AsyncTypedCommands;

const KEY: &str = "user:42";

#[tokio::main]
async fn main() -> redis::RedisResult<()> {
    let mode = env::var("REDIS_MODE").unwrap_or_else(|_| "cluster".into());
    println!("[rust] mode={mode} — read-through цикл, Ctrl+C для выхода");
    if mode == "sentinel" {
        run_sentinel().await
    } else {
        run_cluster().await
    }
}

async fn run_cluster() -> redis::RedisResult<()> {
    let nodes: Vec<String> = csv("REDIS_ADDRS", "127.0.0.1:6379")
        .iter()
        .map(|a| format!("redis://{a}/"))
        .collect();

    let client = redis::cluster::ClusterClient::new(nodes)?;
    // ClusterConnection сам переоткрывает соединения и обновляет карту слотов.
    let mut conn = client.get_async_connection().await?;

    loop {
        step(&mut conn).await;
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
}

async fn run_sentinel() -> redis::RedisResult<()> {
    use redis::sentinel::{SentinelClient, SentinelServerType};

    let nodes: Vec<String> = csv("REDIS_SENTINELS", "127.0.0.1:26379")
        .iter()
        .map(|a| format!("redis://{a}/"))
        .collect();
    let master = env::var("REDIS_MASTER").unwrap_or_else(|_| "mymaster".into());

    // SentinelClient::build(params, service_name, node_info, server_type) — API redis-rs 1.x.
    let mut client = SentinelClient::build(nodes, master, None, SentinelServerType::Master)?;

    loop {
        // Каждую итерацию заново резолвим primary через Sentinel — так виден переезд при failover.
        match client.get_async_connection().await {
            Ok(mut conn) => step(&mut conn).await,
            Err(e) => println!("[rust] ERROR (resolve/connect): {e}"),
        }
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
}

/// Одна итерация read-through: GET, при промахе — SET с TTL.
/// AsyncTypedCommands (redis-rs 1.x): get -> Option<String>, set_ex(key, value, seconds: u64).
async fn step<C: AsyncTypedCommands>(conn: &mut C) {
    match conn.get(KEY).await {
        Ok(Some(v)) => println!("[rust] HIT {v}"),
        Ok(None) => {
            let nv = format!("profile@{}", now());
            match conn.set_ex(KEY, nv.as_str(), 30).await {
                Ok(()) => println!("[rust] MISS -> set {nv}"),
                Err(e) => println!("[rust] MISS, SET error: {e}"),
            }
        }
        Err(e) => println!("[rust] ERROR: {e}"),
    }
}

fn csv(key: &str, def: &str) -> Vec<String> {
    env::var(key)
        .unwrap_or_else(|_| def.into())
        .split(',')
        .map(|s| s.trim().to_string())
        .collect()
}

fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}
