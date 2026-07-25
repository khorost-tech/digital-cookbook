package tech.khorost.messaging;

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
import java.util.List;
import java.util.Properties;
import java.util.concurrent.ExecutionException;

/**
 * (1) Базовая доставка сообщений end-to-end: producer шлёт N сообщений в топик
 * с 3 партициями, consumer читает их обратно и мы сверяем количество.
 */
public final class ProducerConsumerDemo {
    public static final String TOPIC = "demo-basic";
    public static final int PARTITIONS = 3;

    private ProducerConsumerDemo() {
    }

    public static int run(int messageCount) throws ExecutionException, InterruptedException {
        Kafka.ensureTopic(TOPIC, PARTITIONS, (short) 1);

        Properties producerProps = new Properties();
        producerProps.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        producerProps.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        producerProps.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        producerProps.put(ProducerConfig.ACKS_CONFIG, "all");

        int sent = 0;
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(producerProps)) {
            for (int i = 0; i < messageCount; i++) {
                String key = "key-" + (i % PARTITIONS);
                String value = "msg-" + i;
                RecordMetadata meta = producer.send(new ProducerRecord<>(TOPIC, key, value)).get();
                if (i < 3 || i == messageCount - 1) {
                    System.out.printf("  [producer] отправлено %s -> partition=%d offset=%d%n",
                            value, meta.partition(), meta.offset());
                }
                sent++;
            }
        }
        System.out.printf("  [producer] всего отправлено: %d%n", sent);

        Properties consumerProps = new Properties();
        consumerProps.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        consumerProps.put(ConsumerConfig.GROUP_ID_CONFIG, "basic-group");
        consumerProps.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        consumerProps.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        consumerProps.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        consumerProps.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "true");

        int received = 0;
        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(consumerProps)) {
            consumer.subscribe(List.of(TOPIC));
            long deadline = System.currentTimeMillis() + 30_000;
            while (received < messageCount && System.currentTimeMillis() < deadline) {
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(500));
                for (ConsumerRecord<String, String> r : records) {
                    received++;
                }
            }
        }
        System.out.printf("  [consumer] всего получено: %d%n", received);
        return received;
    }
}
