// Kafka-пример к статье #4 «Брокеры и стриминг». Транзакционный producer + read_committed
// consumer — exactly-once semantics на JVM (каноничный транзакционный API Kafka).
// https://khorost.tech/databases/transactions-brokers-rabbitmq-kafka/
//
//   mvn -q compile exec:java

import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;

import java.time.Duration;
import java.util.List;
import java.util.Properties;

public class Main {
    static String env(String k, String def) {
        String v = System.getenv(k);
        return v == null || v.isEmpty() ? def : v;
    }

    public static void main(String[] args) {
        String broker = env("KAFKA", "localhost:9095");
        String topic = "tx-topic-java";

        // Транзакционный producer: transactional.id включает транзакции и idempotent producer.
        Properties pp = new Properties();
        pp.put("bootstrap.servers", broker);
        pp.put("transactional.id", "tx-demo-java");
        pp.put("enable.idempotence", true);
        pp.put("key.serializer", StringSerializer.class.getName());
        pp.put("value.serializer", StringSerializer.class.getName());

        try (KafkaProducer<String, String> producer = new KafkaProducer<>(pp)) {
            producer.initTransactions();
            produceTx(producer, topic, "aborted", false);  // прерываем — записи не должны быть видны
            produceTx(producer, topic, "committed", true);  // коммитим — видны
        }

        // Consumer с isolation.level=read_committed — не видит прерванную транзакцию.
        Properties cp = new Properties();
        cp.put("bootstrap.servers", broker);
        cp.put("group.id", "tx-demo-java-consumer");
        cp.put("auto.offset.reset", "earliest");
        cp.put("isolation.level", "read_committed");
        cp.put("key.deserializer", StringDeserializer.class.getName());
        cp.put("value.deserializer", StringDeserializer.class.getName());

        int committed = 0, aborted = 0;
        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(cp)) {
            consumer.subscribe(List.of(topic));
            long deadline = System.currentTimeMillis() + 10_000;
            while (committed < 10 && System.currentTimeMillis() < deadline) {
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(500));
                for (ConsumerRecord<String, String> r : records) {
                    if (r.value().startsWith("aborted")) {
                        aborted++;
                    } else {
                        committed++;
                    }
                }
            }
        }
        System.out.printf("kafka (EOS, Java): 10 aborted + 10 committed; read_committed consumer увидел committed=%d aborted=%d — прерванная транзакция невидима%n",
                committed, aborted);
    }

    static void produceTx(KafkaProducer<String, String> producer, String topic, String prefix, boolean commit) {
        producer.beginTransaction();
        for (int i = 0; i < 10; i++) {
            producer.send(new ProducerRecord<>(topic, prefix + "-" + i));
        }
        if (commit) {
            producer.commitTransaction();
        } else {
            producer.abortTransaction();
        }
    }
}
