package tech.khorost.testing.distributed.async;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.concurrent.atomic.AtomicBoolean;

/**
 * Фоновый консюмер Kafka: читает события из топика и кладёт эффект в EventStore.
 * Работает в собственном потоке (реализует Runnable) — тест запускает его,
 * шлёт событие продюсером и ждёт эффекта через Awaitility.
 *
 * Смысл для статьи: обработка асинхронна и eventual — между отправкой и
 * появлением эффекта проходит НЕизвестное время. Правильный тест ждёт условия,
 * а не фиксированную паузу.
 */
public final class OrderEventConsumer implements Runnable, AutoCloseable {

    private static final Logger log = LoggerFactory.getLogger(OrderEventConsumer.class);

    private final KafkaConsumer<String, String> consumer;
    private final String topic;
    private final EventStore store;
    private final AtomicBoolean running = new AtomicBoolean(true);

    public OrderEventConsumer(String bootstrapServers, String topic, String groupId, EventStore store) {
        this.topic = topic;
        this.store = store;

        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, bootstrapServers);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, groupId);
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        props.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "true");
        this.consumer = new KafkaConsumer<>(props);
    }

    @Override
    public void run() {
        consumer.subscribe(List.of(topic));
        try {
            while (running.get()) {
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(200));
                for (ConsumerRecord<String, String> record : records) {
                    // "Обработка" события: сохраняем эффект в хранилище.
                    log.info("consumed key={} value={}", record.key(), record.value());
                    store.put(record.key(), record.value());
                }
            }
        } catch (org.apache.kafka.common.errors.WakeupException e) {
            // Ожидаемо при остановке через close() — не ошибка.
        } finally {
            consumer.close();
        }
    }

    @Override
    public void close() {
        running.set(false);
        consumer.wakeup();
    }
}
