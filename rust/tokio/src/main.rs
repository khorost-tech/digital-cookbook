//! Демо к статье «Rust async runtime: Tokio изнутри».
//! https://khorost.tech/rust/rust-async-tokio/
//!
//! Три сюжета из статьи, воспроизводимые ОФФЛАЙН (без сети), детерминированно:
//!   1) concurrent_io  — конкурентные I/O-ожидания через join! + таймаут на операцию;
//!   2) cpu_offloaded  — spawn_blocking: тяжёлую синхронную работу мимо async-планировщика;
//!   3) backpressure   — ограниченный mpsc-канал как естественный тормоз producer'а.
//!
//! Запуск:  cargo run
//! Сетевой аналог (reqwest, параллельные HTTP-запросы) — examples/parallel_http.rs.

use std::time::{Duration, Instant};
use tokio::sync::mpsc;
use tokio::time::{sleep, timeout};

/// Симуляция I/O-операции: «ждём сеть» delay_ms миллисекунд и возвращаем размер «ответа».
/// sleep — асинхронный, поэтому во время ожидания поток executor'а свободен для других задач.
async fn fake_fetch(name: &str, delay_ms: u64) -> usize {
    sleep(Duration::from_millis(delay_ms)).await;
    name.len()
}

/// Выигрыш async — в ОЖИДАНИИ: две операции по 50 мс конкурентно дают ~50 мс, а не 100.
async fn concurrent_io() {
    let t0 = Instant::now();
    // join! опрашивает оба future конкурентно в одной задаче (без создания потоков).
    let (a, b) = tokio::join!(fake_fetch("alpha", 50), fake_fetch("beta", 50));
    println!(
        "[join] a={a} b={b} за {:?} — конкурентно, а не последовательно (иначе было бы ~100 мс)",
        t0.elapsed()
    );

    // Таймаут на операцию: если не успела — future отменяется (дропается).
    match timeout(Duration::from_millis(20), fake_fetch("slow", 100)).await {
        Ok(n) => println!("[timeout] успело: {n}"),
        Err(_) => println!("[timeout] операция отменена по таймауту — медленный future дропнут"),
    }
}

/// Тяжёлую синхронную CPU-работу — в spawn_blocking, чтобы не занимать поток async-планировщика.
async fn cpu_offloaded() {
    let sum = tokio::task::spawn_blocking(|| {
        // Чистое вычисление: в async его держать нельзя — заблокировал бы executor.
        (0u64..50_000_000).fold(0u64, |acc, x| acc.wrapping_add(x))
    })
    .await
    .expect("blocking-задача паникнула");
    println!("[spawn_blocking] CPU-результат={sum} — посчитан вне async-планировщика");
}

/// Ограниченный канал: send().await притормаживает producer'а, когда буфер полон, — backpressure.
async fn backpressure() {
    let (tx, mut rx) = mpsc::channel::<u32>(4); // ёмкость 4
    let producer = tokio::spawn(async move {
        for i in 0..10 {
            // Когда в буфере уже 4 непрочитанных сообщения, эта строка ждёт на .await,
            // пока consumer не освободит место, — producer естественным образом тормозится.
            tx.send(i).await.expect("receiver закрыт");
        }
        // tx дропается здесь → rx.recv() ниже вернёт None и завершит цикл.
    });

    let mut received = 0u32;
    while let Some(_v) = rx.recv().await {
        sleep(Duration::from_millis(5)).await; // consumer намеренно медленнее producer'а
        received += 1;
    }
    producer.await.expect("producer паникнул");
    println!("[backpressure] consumer обработал {received} сообщений через канал ёмкостью 4");
}

#[tokio::main]
async fn main() {
    concurrent_io().await;
    cpu_offloaded().await;
    backpressure().await;
}
