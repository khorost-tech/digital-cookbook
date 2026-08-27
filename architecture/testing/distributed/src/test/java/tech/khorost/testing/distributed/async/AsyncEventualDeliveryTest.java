package tech.khorost.testing.distributed.async;

import org.apache.kafka.clients.admin.Admin;
import org.apache.kafka.clients.admin.AdminClientConfig;
import org.apache.kafka.clients.admin.NewTopic;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.serialization.StringSerializer;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.testcontainers.kafka.KafkaContainer;

import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.concurrent.ExecutionException;

import static org.awaitility.Awaitility.await;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Тест eventual-эффекта БЕЗ sleep. Продюсер шлёт событие в Kafka, фоновый
 * консюмер (OrderEventConsumer) кладёт результат в EventStore. Тест не спит
 * фиксированное время, а ждёт условия через Awaitility: await().untilAsserted.
 *
 * Антипаттерн (см. README): Thread.sleep(2000) — либо флак (медленный CI не
 * успел), либо трата времени (эффект наступил за 50 мс, а тест спит 2 с).
 * Awaitility опрашивает условие часто и завершается сразу по факту.
 */
class AsyncEventualDeliveryTest {

    private static final String TOPIC = "orders";

    // Новый Testcontainers-модуль KafkaContainer работает с нативными
    // apache/kafka-образами (KRaft, без ZooKeeper). Образ есть локально.
    private static final KafkaContainer KAFKA =
            new KafkaContainer("apache/kafka:4.3.1");

    private static EventStore store;
    private static OrderEventConsumer consumer;
    private static Thread consumerThread;

    @BeforeAll
    static void startInfra() throws ExecutionException, InterruptedException {
        KAFKA.start();
        createTopic(KAFKA.getBootstrapServers(), TOPIC);

        store = new EventStore();
        consumer = new OrderEventConsumer(KAFKA.getBootstrapServers(), TOPIC, "orders-consumer", store);
        consumerThread = new Thread(consumer, "order-consumer");
        consumerThread.start();
    }

    @AfterAll
    static void stopInfra() throws InterruptedException {
        if (consumer != null) {
            consumer.close();
        }
        if (consumerThread != null) {
            consumerThread.join(Duration.ofSeconds(10).toMillis());
        }
        KAFKA.stop();
    }

    @Test
    void producedEvent_eventuallyAppearsInStore() {
        try (KafkaProducer<String, String> producer = newProducer(KAFKA.getBootstrapServers())) {
            producer.send(new ProducerRecord<>(TOPIC, "ORD-42", "PLACED"));
            producer.flush();
        }

        // Ждём эффекта, а не спим. Эффект наступит, как только консюмер прочитает
        // событие; тест завершится сразу после этого.
        await().atMost(Duration.ofSeconds(30))
                .pollInterval(Duration.ofMillis(100))
                .untilAsserted(() -> {
                    assertTrue(store.get("ORD-42").isPresent(),
                            "консюмер должен обработать событие");
                    assertEquals("PLACED", store.get("ORD-42").orElseThrow());
                });
    }

    private static void createTopic(String bootstrap, String topic)
            throws ExecutionException, InterruptedException {
        try (Admin admin = Admin.create(
                Map.of(AdminClientConfig.BOOTSTRAP_SERVERS_CONFIG, bootstrap))) {
            admin.createTopics(List.of(new NewTopic(topic, 1, (short) 1))).all().get();
        }
    }

    private static KafkaProducer<String, String> newProducer(String bootstrap) {
        Properties props = new Properties();
        props.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, bootstrap);
        props.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        return new KafkaProducer<>(props);
    }
}
