package tech.khorost.messaging;

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
 * (3) Exactly-once: транзакционный продюсер + read_committed консьюмер.
 * Батч A коммитится штатно. Батч B на первой попытке "падает" в середине
 * (симуляция ошибки обработки) и абортится — read_committed-консьюмер НЕ должен
 * увидеть эти сообщения. Батч B пересылается повторно (тот же контент) и
 * коммитится — виден один раз, без дублей от первой (абортнутой) попытки.
 */
public final class EosDemo {
    public static final String TOPIC = "demo-eos";
    public static final int PARTITIONS = 3;
    public static final int BATCH_SIZE = 5;

    private EosDemo() {
    }

    public static final class Result {
        int sentPhysically;   // сколько записей реально ушло в лог партиций (включая абортнутые)
        int committedLogical; // сколько сообщений логически подтверждено (A + B-retry)
        int consumedVisible;  // сколько увидел read_committed консьюмер
    }

    private static void sendBatch(KafkaProducer<String, String> producer, String prefix, boolean failMidway) {
        producer.beginTransaction();
        try {
            for (int i = 0; i < BATCH_SIZE; i++) {
                producer.send(new ProducerRecord<>(TOPIC, prefix + "-key-" + i, prefix + "-" + i));
                if (failMidway && i == 2) {
                    throw new RuntimeException("симулированный сбой обработки в середине батча " + prefix);
                }
            }
            producer.commitTransaction();
            System.out.printf("  [eos] батч %s: commitTransaction() — %d сообщений подтверждено%n", prefix, BATCH_SIZE);
        } catch (RuntimeException e) {
            System.out.printf("  [eos] батч %s: сбой (%s) -> abortTransaction()%n", prefix, e.getMessage());
            producer.abortTransaction();
        }
    }

    public static Result run() {
        Kafka.ensureTopic(TOPIC, PARTITIONS, (short) 1);
        Result result = new Result();

        Properties producerProps = new Properties();
        producerProps.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        producerProps.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        producerProps.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        producerProps.put(ProducerConfig.TRANSACTIONAL_ID_CONFIG, "messaging-eos-producer");
        producerProps.put(ProducerConfig.ENABLE_IDEMPOTENCE_CONFIG, true);
        producerProps.put(ProducerConfig.ACKS_CONFIG, "all");

        try (KafkaProducer<String, String> producer = new KafkaProducer<>(producerProps)) {
            producer.initTransactions();

            // Батч A: штатный коммит.
            sendBatch(producer, "batchA", false);

            // Батч B, попытка 1: падает в середине -> abort. Эти 5 записей физически
            // попадают в лог партиций (не в отдельный WAL), но помечены control-record'ом
            // ABORT — read_committed-консьюмер их отфильтрует.
            sendBatch(producer, "batchB", true);

            // Батч B, попытка 2 (повтор после "устранения сбоя"): штатный коммит.
            sendBatch(producer, "batchB", false);
        }

        result.sentPhysically = BATCH_SIZE * 3;
        result.committedLogical = BATCH_SIZE * 2; // A + B-retry

        Properties consumerProps = new Properties();
        consumerProps.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        consumerProps.put(ConsumerConfig.GROUP_ID_CONFIG, "eos-read-group");
        consumerProps.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        consumerProps.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        consumerProps.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        consumerProps.put(ConsumerConfig.ISOLATION_LEVEL_CONFIG, "read_committed");
        consumerProps.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "true");

        int consumed = 0;
        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(consumerProps)) {
            consumer.subscribe(List.of(TOPIC));
            long deadline = System.currentTimeMillis() + 20_000;
            long lastRecordAt = System.currentTimeMillis();
            while (System.currentTimeMillis() < deadline) {
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(500));
                for (ConsumerRecord<String, String> r : records) {
                    consumed++;
                    lastRecordAt = System.currentTimeMillis();
                    System.out.printf("  [eos-consumer, read_committed] %s = %s (partition=%d offset=%d)%n",
                            r.key(), r.value(), r.partition(), r.offset());
                }
                // выходим пораньше, если 3с подряд ничего нового не пришло и мы уже видели ожидаемое кол-во
                if (consumed >= result.committedLogical && System.currentTimeMillis() - lastRecordAt > 3000) {
                    break;
                }
            }
        }
        result.consumedVisible = consumed;

        System.out.printf("  [eos] физически отправлено записей: %d (включая абортнутый батч), "
                        + "логически подтверждено: %d, увидел read_committed-консьюмер: %d%n",
                result.sentPhysically, result.committedLogical, result.consumedVisible);
        return result;
    }
}
