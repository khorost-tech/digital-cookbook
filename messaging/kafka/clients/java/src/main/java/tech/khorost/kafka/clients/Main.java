package tech.khorost.kafka.clients;

import org.apache.kafka.clients.admin.Admin;
import org.apache.kafka.clients.admin.AdminClientConfig;
import org.apache.kafka.clients.admin.NewTopic;
import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.clients.producer.RecordMetadata;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;

import java.time.Duration;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Properties;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;

/**
 * Стенд #8 (клиенты в разных языках), драйвер org.apache.kafka:kafka-clients.
 *
 * <p>Родословная: РЕФЕРЕНСНЫЙ JVM-клиент — эталон, новые KIP-фичи появляются
 * здесь первыми. Полный набор фич "из коробки": идемпотентный продюсер,
 * транзакции/EOS, cooperative-sticky ребаланс.
 *
 * <p>Сценарий (одинаковый на всех языках стенда): producer с ключом и
 * idempotence=true отправляет 12 сообщений (4 ключа x 3 раунда) -&gt;
 * consumer в group с ручным commitSync читает их обратно, печатает
 * (partition, offset, key).
 *
 * <p>Запуск:
 * <pre>
 * docker run --rm --network kafka-cookbook-net -v "$(pwd)/clients/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
 *   sh -c "mvn -q package -DskipTests &amp;&amp; java -jar target/clients-java.jar kafka1:9092,kafka2:9092,kafka3:9092"
 * </pre>
 */
public final class Main {

    private static final String TOPIC = "demo-clients-java";
    private static final int PARTITIONS = 3;
    private static final short REPLICATION = 3;
    private static final String GROUP_ID = "demo-clients-java-group";
    private static final String[] KEYS = {"order-1", "order-2", "order-3", "order-4"};

    public static void main(String[] args) throws Exception {
        String brokers = args.length > 0 ? args[0] : "kafka1:9092,kafka2:9092,kafka3:9092";

        ensureTopic(brokers);
        int sent = produce(brokers);
        System.out.printf("[producer] отправлено (acks=all, enable.idempotence=true): %d%n", sent);
        List<String> recv = consume(brokers, sent);
        System.out.printf("[consumer] получено (group=%s, manual commitSync): %d%n", GROUP_ID, recv.size());

        if (sent != recv.size()) {
            throw new IllegalStateException("[assert] FAIL: отправлено " + sent + " != получено " + recv.size());
        }
        System.out.println("[assert] OK: отправлено == получено");
    }

    private static void ensureTopic(String brokers) throws ExecutionException, InterruptedException {
        Properties props = new Properties();
        props.put(AdminClientConfig.BOOTSTRAP_SERVERS_CONFIG, brokers);
        try (Admin admin = Admin.create(props)) {
            try {
                admin.deleteTopics(List.of(TOPIC)).all().get(20, TimeUnit.SECONDS);
            } catch (Exception e) {
                // UnknownTopicOrPartitionException при первом запуске — игнорируем
            }
            long deadline = System.currentTimeMillis() + 10_000;
            while (System.currentTimeMillis() < deadline) {
                boolean stillExists = admin.listTopics().names().get().contains(TOPIC);
                if (!stillExists) break;
                Thread.sleep(300);
            }
            NewTopic newTopic = new NewTopic(TOPIC, PARTITIONS, REPLICATION);
            admin.createTopics(List.of(newTopic)).all().get(20, TimeUnit.SECONDS);
            System.out.printf("[admin] топик %s создан (partitions=%d, rf=%d)%n", TOPIC, PARTITIONS, REPLICATION);
        } catch (java.util.concurrent.TimeoutException e) {
            throw new RuntimeException(e);
        }
    }

    private static int produce(String brokers) {
        Properties props = new Properties();
        props.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, brokers);
        props.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.ACKS_CONFIG, "all");
        props.put(ProducerConfig.ENABLE_IDEMPOTENCE_CONFIG, true);

        int count = 0;
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(props)) {
            for (int round = 0; round < 3; round++) {
                for (String key : KEYS) {
                    String value = key + "-evt-" + round;
                    ProducerRecord<String, String> record = new ProducerRecord<>(TOPIC, key, value);
                    try {
                        RecordMetadata md = producer.send(record).get();
                        System.out.printf("  sent  key=%s partition=%d offset=%d%n", key, md.partition(), md.offset());
                        count++;
                    } catch (Exception e) {
                        throw new RuntimeException("produce key=" + key, e);
                    }
                }
            }
        }
        return count;
    }

    private record Rec(int partition, long offset, String key) {}

    private static List<String> consume(String brokers, int expected) {
        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, brokers);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, GROUP_ID);
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        props.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, false);

        List<Rec> recs = new ArrayList<>();
        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(props)) {
            consumer.subscribe(List.of(TOPIC));
            long deadline = System.currentTimeMillis() + 30_000;
            while (recs.size() < expected && System.currentTimeMillis() < deadline) {
                ConsumerRecords<String, String> batch = consumer.poll(Duration.ofMillis(1000));
                for (ConsumerRecord<String, String> r : batch) {
                    recs.add(new Rec(r.partition(), r.offset(), r.key()));
                }
                if (!batch.isEmpty()) {
                    consumer.commitSync(); // ручной синхронный коммит offset ПОСЛЕ обработки батча
                }
            }
        }
        if (recs.size() < expected) {
            throw new IllegalStateException("consume: таймаут, получено " + recs.size() + " из " + expected);
        }

        recs.sort(Comparator.<Rec>comparingInt(Rec::partition).thenComparingLong(Rec::offset));
        List<String> out = new ArrayList<>();
        for (Rec r : recs) {
            String line = "(partition=" + r.partition() + ", offset=" + r.offset() + ", key=" + r.key() + ")";
            System.out.println("  recv  " + line);
            out.add(line);
        }
        return out;
    }
}
