//! Демонстрационный клиент sqlx: пишет через pgbouncer (transaction pool mode),
//! читает напрямую с реплики. Клиент НИКОГДА не падает на ошибке: при обрыве/failover
//! он логирует ошибку и продолжает, чтобы было видно окно недоступности и reconnect.
//!
//! ВАЖНОЕ ОТЛИЧИЕ от Go/Java клиентов этого стенда: sqlx-postgres — это НЕ обёртка над
//! libpq, а собственная реализация протокола Postgres на чистом Rust. libpq умеет
//! multi-host DSN вида `host1:5432,host2:5432/db?target_session_attrs=read-write` и сама
//! перебирает хосты в поисках read-write сервера (primary) — на этом построен
//! failover-aware MULTIHOST_DSN у pgx (Go) и JDBC (Java) в этом стенде. sqlx такого
//! разбора DSN не делает: PgConnectOptions/URL-парсер ожидает ровно один host, и
//! target_session_attrs как параметр не распознаётся (открытый feature-request —
//! "Multiple Hosts, Failover", https://github.com/launchbadge/sqlx/issues/3333, всё ещё
//! не реализован в 0.9). Поэтому MULTIHOST_DSN здесь НЕ используется для реального
//! подключения: мы не имитируем то, чего в клиенте нет. Ниже — только парсинг DSN и
//! чтение host/port из результата (что не требует сетевого похода к серверу), чтобы
//! показать, какие хосты вообще перечислены в переменной окружения.

use std::env;
use std::str::FromStr;
use std::time::Duration;

use sqlx::postgres::{PgConnectOptions, PgPoolOptions};
use sqlx::{Pool, Postgres, Row};

const OP_TIMEOUT: Duration = Duration::from_secs(1);
const MAX_RETRIES: u32 = 2;

#[tokio::main]
async fn main() {
    let writer = build_writer_pool().await;
    let reader = build_reader_pool().await;

    // Один раз при старте: разбираем MULTIHOST_DSN и показываем, что реально видит клиент.
    // См. комментарий в шапке файла — реального failover-aware подключения тут нет,
    // sqlx не поддерживает libpq multi-host + target_session_attrs.
    log_multihost_limitation();

    println!("[rust] цикл WRITE(bouncer)/READ(replica) раз в секунду, Ctrl+C для выхода");
    let mut tick = tokio::time::interval(Duration::from_secs(1));

    loop {
        tick.tick().await;

        match write_row(&writer).await {
            Ok(id) => println!("[rust] WROTE id={id}"),
            Err(e) => println!("[rust] ERROR write: {e}"),
        }

        match read_rows(&reader).await {
            Ok(rows) => println!("[rust] READ {rows:?}"),
            Err(e) => println!("[rust] ERROR read: {e}"),
        }
    }
}

/// build_writer_pool — пул для записи через pgbouncer в TRANSACTION pool mode.
///
/// КРИТИЧНО: pgbouncer в transaction pool mode отдаёт клиенту разное серверное соединение
/// на каждую транзакцию (а то и на каждый запрос вне явной транзакции). sqlx по умолчанию
/// готовит запросы через extended protocol с server-side prepared statements и кеширует их
/// на уровне соединения (statement cache) — драйвер запоминает, что statement с таким-то
/// текстом уже подготовлен на этом физическом соединении, и на следующий раз пытается
/// сразу его исполнить (Bind/Execute без повторного Parse). Через transaction-mode бенчер
/// это ломается: соединение, которое видит sqlx-пул, каждый раз может быть новым
/// физическим соединением к Postgres (bouncer подменяет его между транзакциями), и сервер
/// ответит "prepared statement does not exist" — непредсказуемо и трудноотлаживаемо.
/// Решение: отключить statement cache на уровне PgConnectOptions
/// (`statement_cache_capacity(0)`) — тогда sqlx на каждый запрос заново готовит и сразу
/// закрывает statement (unnamed prepared statement), не полагаясь на то, что кеш соединения
/// переживёт следующий запрос. Это безопасно для transaction-mode бенчера.
async fn build_writer_pool() -> Pool<Postgres> {
    let dsn = must_getenv("PGBOUNCER_DSN");
    let opts = PgConnectOptions::from_str(&dsn)
        .unwrap_or_else(|e| fatal(&format!("не удалось разобрать PGBOUNCER_DSN: {e}")))
        .statement_cache_capacity(0);

    PgPoolOptions::new()
        .max_connections(10)
        .max_lifetime(Duration::from_secs(5 * 60))
        .acquire_timeout(OP_TIMEOUT)
        .connect_with(opts)
        .await
        .unwrap_or_else(|e| fatal(&format!("не удалось создать writer pool: {e}")))
}

/// build_reader_pool — пул для чтения напрямую с реплики (без bouncer). Соединение с
/// сервером закреплено за пулом sqlx, поэтому дефолтный statement cache тут безопасен.
async fn build_reader_pool() -> Pool<Postgres> {
    let dsn = must_getenv("REPLICA_DSN");

    PgPoolOptions::new()
        .max_connections(10)
        .max_lifetime(Duration::from_secs(5 * 60))
        .acquire_timeout(OP_TIMEOUT)
        .connect(&dsn)
        .await
        .unwrap_or_else(|e| fatal(&format!("не удалось создать reader pool: {e}")))
}

/// log_multihost_limitation разбирает MULTIHOST_DSN как обычный (одно-хостовый) DSN,
/// чтобы показать в логе, что видит sqlx-парсер. Реального подключения с перебором
/// хостов и выбором read-write сервера НЕ делает — sqlx этого не умеет (см. шапку файла).
fn log_multihost_limitation() {
    let dsn = must_getenv("MULTIHOST_DSN");
    match PgConnectOptions::from_str(&dsn) {
        Ok(opts) => {
            println!(
                "[rust] MULTIHOST: sqlx не поддерживает libpq multi-host/target_session_attrs \
                 (issue launchbadge/sqlx#3333) — DSN содержит несколько хостов, но парсер \
                 sqlx учитывает только первый: host={:?} port={}",
                opts.get_host(),
                opts.get_port(),
            );
        }
        Err(e) => {
            println!(
                "[rust] MULTIHOST: не удалось разобрать даже первый хост MULTIHOST_DSN: {e} \
                 (ожидаемо: sqlx не поддерживает multi-host DSN, см. launchbadge/sqlx#3333)"
            );
        }
    }
}

/// write_row вставляет строку в users через writer pool (pgbouncer) с ретраями
/// транзиентных ошибок.
async fn write_row(pool: &Pool<Postgres>) -> Result<i64, sqlx::Error> {
    let name = format!("client-rust@{}", now_hms_millis());

    with_retry(|| {
        let name = name.clone();
        async move {
            let fut = sqlx::query_scalar::<_, i64>("INSERT INTO users (name) VALUES ($1) RETURNING id")
                .bind(name)
                .fetch_one(pool);

            match tokio::time::timeout(OP_TIMEOUT, fut).await {
                Ok(res) => res,
                Err(_) => Err(sqlx::Error::Io(std::io::Error::new(
                    std::io::ErrorKind::TimedOut,
                    "operation timed out",
                ))),
            }
        }
    })
    .await
}

/// read_rows читает несколько последних строк из users через reader pool (реплика).
async fn read_rows(pool: &Pool<Postgres>) -> Result<Vec<String>, sqlx::Error> {
    with_retry(|| async move {
        let fut = sqlx::query("SELECT id, name FROM users ORDER BY id DESC LIMIT 3").fetch_all(pool);

        let rows = match tokio::time::timeout(OP_TIMEOUT, fut).await {
            Ok(res) => res?,
            Err(_) => {
                return Err(sqlx::Error::Io(std::io::Error::new(
                    std::io::ErrorKind::TimedOut,
                    "operation timed out",
                )))
            }
        };

        let mut out = Vec::with_capacity(rows.len());
        for row in &rows {
            let id: i64 = row.try_get("id")?;
            let name: String = row.try_get("name")?;
            out.push(format!("{id}:{name}"));
        }
        Ok(out)
    })
    .await
}

/// with_retry повторяет op при транзиентных ошибках Postgres (serialization_failure,
/// deadlock_detected, admin shutdown) с небольшим backoff. Остальные ошибки не ретраятся —
/// они возвращаются вызывающему коду как есть (и залогируются, не уронив цикл).
async fn with_retry<F, Fut, T>(mut op: F) -> Result<T, sqlx::Error>
where
    F: FnMut() -> Fut,
    Fut: std::future::Future<Output = Result<T, sqlx::Error>>,
{
    let mut attempt = 0;
    loop {
        match op().await {
            Ok(v) => return Ok(v),
            Err(e) if attempt < MAX_RETRIES && is_retryable(&e) => {
                attempt += 1;
                tokio::time::sleep(Duration::from_millis(50 * attempt as u64)).await;
            }
            Err(e) => return Err(e),
        }
    }
}

/// is_retryable распознаёт транзиентные коды ошибок Postgres по SQLSTATE.
fn is_retryable(err: &sqlx::Error) -> bool {
    let sqlx::Error::Database(db_err) = err else {
        return false;
    };
    match db_err.code() {
        Some(code) => matches!(
            code.as_ref(),
            "40001" // serialization_failure
                | "40P01" // deadlock_detected
                | "57P01" // admin_shutdown
        ),
        None => false,
    }
}

fn must_getenv(key: &str) -> String {
    env::var(key).unwrap_or_else(|_| fatal(&format!("не задана переменная окружения {key}")))
}

fn fatal(msg: &str) -> ! {
    println!("[rust] FATAL: {msg}");
    std::process::exit(1);
}

fn now_hms_millis() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    let millis_total = now.as_millis();
    let ms = millis_total % 1000;
    let secs_total = (millis_total / 1000) as u64;
    let s = secs_total % 60;
    let m = (secs_total / 60) % 60;
    let h = (secs_total / 3600) % 24;
    format!("{h:02}:{m:02}:{s:02}.{ms:03}")
}
