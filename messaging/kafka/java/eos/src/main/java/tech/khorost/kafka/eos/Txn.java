package tech.khorost.kafka.eos;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;

import java.time.Duration;
import java.util.List;
import java.util.Properties;

/**
 * (1)/(2) ядро транзакций: транзакционный producer, батч A коммитится
 * штатно, батч B на первой попытке абортится (симулированный сбой в
 * середине), пересылается повторно и коммитится. Тот же сценарий, что
 * ../../../java-deep-dive/messaging/.../EosDemo.java (не переиспользуется
 * напрямую — этот стенд разбит на CLI-фазы, как replication/storage), и то
 * же поведение, что ../../go/eos/txn.go — franz-go/kafka-clients сходятся
 * побайтово в физическом/логическом счёте.
 */
public final class Txn {
    private Txn() {
    }

    private static KafkaProducer<String, String> newTxnProducer(String txnId) {
        Properties props = new Properties();
        props.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.TRANSACTIONAL_ID_CONFIG, txnId);
        // С transactional.id заданным kafka-clients берёт idempotence=true по
        // дефолту; а явная попытка enable.idempotence=false + transactional.id
        // ПАДАЕТ с ConfigException("Cannot set a transactional.id without also
        // enabling idempotence.") — симметрично franz-go, который так же
        // запрещает DisableIdempotentWrite() при заданном TransactionalID.
        props.put(ProducerConfig.ACKS_CONFIG, "all");
        KafkaProducer<String, String> producer = new KafkaProducer<>(props);
        // ⚠️ Явный initTransactions() — ОБЯЗАТЕЛЕН в Java API (в отличие от
        // franz-go, где BeginTransaction() сам восстанавливает producer id
        // внутри себя). initTransactions() тоже фенсит/абортит зависшую
        // транзакцию ПРЕДЫДУЩЕГО процесса с тем же transactional.id (KIP-98) —
        // именно на этот вызов и рассчитывает попытка B в cpp-сценарии.
        producer.initTransactions();
        return producer;
    }

    public static final class BatchResult {
        int sentPhysically;
        int committedLogical;
    }

    private static int sendBatch(KafkaProducer<String, String> producer, String topic, String prefix, int batchSize, boolean failMidway) {
        producer.beginTransaction();
        int produced = 0;
        RuntimeException midwayErr = null;
        for (int i = 0; i < batchSize; i++) {
            // ⚠️ Синхронный .get() (ждём ack брокера) НАРОЧНО, а не обычный
            // асинхронный send() — если оставить асинхронно, найденный живьём
            // факт: abortTransaction() аварийно отменяет ЕЩЁ НЕ
            // ПОДТВЕРЖДЁННЫЕ брокером записи клиентски (они физически на
            // диск вообще не попадают, Javadoc KafkaProducer.abortTransaction:
            // "Any unflushed produce messages will be aborted..."), и
            // read_uncommitted-счёт физического мусора от абортнутого батча
            // тогда оказывается 0, а не 3 — воспроизведено живьём при первой
            // версии этого стенда. Franz-go в
            // ../../go/eos/txn.go использует ProduceSync (тоже синхронно) —
            // чтобы сравнение "физически записано" было яблоки-к-яблокам
            // между клиентами, а не артефактом разной буферизации, здесь
            // синхронный .get() тоже.
            try {
                producer.send(new ProducerRecord<>(topic, prefix + "-key-" + i, prefix + "-" + i)).get();
            } catch (Exception e) {
                throw new RuntimeException("send " + prefix + " i=" + i, e);
            }
            produced++;
            if (failMidway && i == 2) {
                midwayErr = new RuntimeException("симулированный сбой обработки в середине батча " + prefix);
                break;
            }
        }
        if (midwayErr != null) {
            producer.abortTransaction();
            System.out.printf("  [txn] батч %s: сбой (%s) -> abortTransaction() — %d записей физически ушли, логически ничего не подтверждено%n",
                    prefix, midwayErr.getMessage(), produced);
            return -produced; // отрицательное значение = абортнутый батч
        }
        producer.commitTransaction();
        System.out.printf("  [txn] батч %s: commitTransaction() — %d сообщений подтверждено%n", prefix, produced);
        return produced;
    }

    public static BatchResult runTxnBatches(String topic, int batchSize) {
        BatchResult result = new BatchResult();
        try (KafkaProducer<String, String> producer = newTxnProducer("cookbook-eos-txn-producer-java")) {
            int a = sendBatch(producer, topic, "batchA", batchSize, false);
            result.sentPhysically += Math.abs(a);
            if (a > 0) result.committedLogical += a;

            int b1 = sendBatch(producer, topic, "batchB", batchSize, true);
            result.sentPhysically += Math.abs(b1);
            if (b1 > 0) result.committedLogical += b1;

            int b2 = sendBatch(producer, topic, "batchB", batchSize, false);
            result.sentPhysically += Math.abs(b2);
            if (b2 > 0) result.committedLogical += b2;
        }
        System.out.printf("[txn] физически отправлено записей: %d, логически подтверждено: %d%n",
                result.sentPhysically, result.committedLogical);
        return result;
    }

    // package-private (не private) — переиспользуется из Cpp.cppVerify для
    // счёта output read_committed/read_uncommitted тем же кодом.
    static int consumeIsolation(String topic, String isolationLevel, long idleTimeoutMs) {
        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, "eos-txn-verify-" + isolationLevel + "-" + System.nanoTime());
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        props.put(ConsumerConfig.ISOLATION_LEVEL_CONFIG, isolationLevel);
        props.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "false");

        int count = 0;
        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(props)) {
            consumer.subscribe(List.of(topic));
            long lastProgress = System.currentTimeMillis();
            while (true) {
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(500));
                count += records.count();
                if (!records.isEmpty()) {
                    lastProgress = System.currentTimeMillis();
                } else if (System.currentTimeMillis() - lastProgress > idleTimeoutMs) {
                    break;
                }
            }
        }
        return count;
    }

    public static void runTxnVerify(String topic, int expectCommitted, int expectPhysical) {
        int committedCount = consumeIsolation(topic, "read_committed", 5000);
        int uncommittedCount = consumeIsolation(topic, "read_uncommitted", 5000);
        System.out.printf("[txn-verify] read_committed=%d read_uncommitted=%d (ожидалось: committed=%d physical=%d)%n",
                committedCount, uncommittedCount, expectCommitted, expectPhysical);

        if (committedCount != expectCommitted) {
            throw new IllegalStateException("[txn-verify] РАСХОЖДЕНИЕ: read_committed=" + committedCount + ", ожидалось " + expectCommitted);
        }
        if (uncommittedCount != expectPhysical) {
            throw new IllegalStateException("[txn-verify] РАСХОЖДЕНИЕ: read_uncommitted=" + uncommittedCount + ", ожидалось " + expectPhysical);
        }
        if (!(uncommittedCount > committedCount)) {
            throw new IllegalStateException("[txn-verify] РАСХОЖДЕНИЕ: read_uncommitted должен быть строго больше read_committed");
        }
        System.out.printf("[txn-verify] OK: read_committed == закоммиченное (%d), read_uncommitted (%d) строго больше — абортнутый батч физически записан, но невидим read_committed-консьюмеру%n",
                committedCount, uncommittedCount);
    }
}
