// Стенд #8 (клиенты в разных языках), Rust: rdkafka (обёртка над librdkafka
// на C, через FFI) -- producer с ключом+идемпотентностью -> consumer в
// group с ручным коммитом.
//
// Родословная: обёртка над librdkafka -- тот же движок, что у Python
// confluent-kafka, C# Confluent.Kafka, Go confluent-kafka-go, C++
// modern-cpp-kafka.
//
// ⚠️ Живая находка про сборку: apt librdkafka-dev в этом образе (Debian
// trixie) даёт librdkafka 2.8.0, а rdkafka-sys v4.10.0 (тянется крейтом
// rdkafka 0.36) с feature "dynamic-linking" требует системную librdkafka
// >= 2.12.1 -- pkg-config падает с несовпадением версий. Рабочий вариант --
// БЕЗ dynamic-linking: rdkafka-sys тогда собирает librdkafka САМ из
// bundled C-исходников (нужны только cmake+perl+gcc в образе, apt
// librdkafka-dev вообще не требуется) -- дольше собирается (компилирует
// C-библиотеку с нуля), зато не зависит от версии в дистрибутиве.
//
// Запуск:
//   docker run --rm --network kafka-cookbook-net -v "$(pwd)/clients/rust:/app" -w /app rust:1 \
//     sh -c "apt-get update -qq && apt-get install -y -qq cmake perl >/dev/null && \
//            cargo run --release -- kafka1:9092,kafka2:9092,kafka3:9092"

use std::collections::BTreeMap;
use std::time::Duration;

use rdkafka::admin::{AdminClient, AdminOptions, NewTopic, TopicReplication};
use rdkafka::client::DefaultClientContext;
use rdkafka::config::ClientConfig;
use rdkafka::consumer::{CommitMode, Consumer, StreamConsumer};
use rdkafka::message::Message;
use rdkafka::producer::{FutureProducer, FutureRecord};
use rdkafka::util::Timeout;

const TOPIC: &str = "demo-clients-rust";
const PARTITIONS: i32 = 3;
const REPLICATION: i32 = 3;
const GROUP_ID: &str = "demo-clients-rust-group";
const KEYS: [&str; 4] = ["order-1", "order-2", "order-3", "order-4"];

#[tokio::main]
async fn main() {
    let args: Vec<String> = std::env::args().collect();
    let brokers = args
        .get(1)
        .cloned()
        .unwrap_or_else(|| "kafka1:9092,kafka2:9092,kafka3:9092".to_string());

    ensure_topic(&brokers).await;
    let sent = produce(&brokers).await;
    println!("[producer] отправлено (acks=all, enable.idempotence=true): {sent}");
    let recv = consume(&brokers, sent).await;
    println!("[consumer] получено (group={GROUP_ID}, manual commit): {}", recv.len());

    if sent != recv.len() {
        panic!("[assert] FAIL: отправлено {sent} != получено {}", recv.len());
    }
    println!("[assert] OK: отправлено == получено");
}

async fn ensure_topic(brokers: &str) {
    let admin: AdminClient<DefaultClientContext> = ClientConfig::new()
        .set("bootstrap.servers", brokers)
        .create()
        .expect("AdminClient::create");

    let _ = admin
        .delete_topics(&[TOPIC], &AdminOptions::new().operation_timeout(Some(Timeout::After(Duration::from_secs(20)))))
        .await; // игнорируем "unknown topic" при первом запуске

    tokio::time::sleep(Duration::from_secs(1)).await;

    let deadline = std::time::Instant::now() + Duration::from_secs(15);
    loop {
        let new_topic = NewTopic::new(TOPIC, PARTITIONS, TopicReplication::Fixed(REPLICATION));
        let res = admin
            .create_topics(&[new_topic], &AdminOptions::new().operation_timeout(Some(Timeout::After(Duration::from_secs(10)))))
            .await
            .expect("create_topics request");
        let ok = res.iter().all(|r| r.is_ok());
        if ok {
            break;
        }
        if std::time::Instant::now() > deadline {
            panic!("CreateTopics: не удалось после ретраев: {res:?}");
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
    }
    println!("[admin] топик {TOPIC} создан (partitions={PARTITIONS}, rf={REPLICATION})");
}

async fn produce(brokers: &str) -> usize {
    let producer: FutureProducer = ClientConfig::new()
        .set("bootstrap.servers", brokers)
        .set("acks", "all")
        .set("enable.idempotence", "true")
        .create()
        .expect("FutureProducer::create");

    let mut count = 0usize;
    for round in 0..3 {
        for key in KEYS.iter() {
            let value = format!("{key}-evt-{round}");
            let record = FutureRecord::to(TOPIC).key(*key).payload(&value);
            match producer.send(record, Timeout::After(Duration::from_secs(10))).await {
                Ok((partition, offset)) => {
                    println!("  sent  key={key} partition={partition} offset={offset}");
                    count += 1;
                }
                Err((err, _)) => panic!("produce key={key}: {err}"),
            }
        }
    }
    count
}

async fn consume(brokers: &str, expected: usize) -> Vec<String> {
    let consumer: StreamConsumer = ClientConfig::new()
        .set("bootstrap.servers", brokers)
        .set("group.id", GROUP_ID)
        .set("auto.offset.reset", "earliest")
        .set("enable.auto.commit", "false")
        .set("partition.assignment.strategy", "cooperative-sticky")
        .create()
        .expect("StreamConsumer::create");

    consumer.subscribe(&[TOPIC]).expect("subscribe");

    // (partition, offset) -> key, отсортируем в конце для стабильного вывода
    let mut recs: BTreeMap<(i32, i64), String> = BTreeMap::new();

    let deadline = tokio::time::Instant::now() + Duration::from_secs(30);
    while recs.len() < expected {
        let remaining = deadline.saturating_duration_since(tokio::time::Instant::now());
        if remaining.is_zero() {
            panic!("consume: таймаут, получено {} из {expected}", recs.len());
        }
        match tokio::time::timeout(remaining, consumer.recv()).await {
            Ok(Ok(msg)) => {
                let key = msg
                    .key()
                    .map(|k| String::from_utf8_lossy(k).to_string())
                    .unwrap_or_default();
                recs.insert((msg.partition(), msg.offset()), key);
                // ручной синхронный коммит offset ПОСЛЕ обработки каждой записи
                consumer
                    .commit_message(&msg, CommitMode::Sync)
                    .expect("commit_message");
            }
            Ok(Err(e)) => panic!("consumer error: {e}"),
            Err(_) => panic!("consume: таймаут, получено {} из {expected}", recs.len()),
        }
    }

    let mut out = Vec::new();
    for ((partition, offset), key) in recs {
        let line = format!("(partition={partition}, offset={offset}, key={key})");
        println!("  recv  {line}");
        out.push(line);
    }
    out
}
