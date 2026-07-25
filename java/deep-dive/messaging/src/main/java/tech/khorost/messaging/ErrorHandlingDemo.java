package tech.khorost.messaging;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.header.Header;
import org.apache.kafka.common.header.internals.RecordHeader;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.Properties;

/**
 * (4) Обработка ошибок: retry с бэкоффом + dead-letter topic. Часть сообщений —
 * "ядовитые" (маркер POISON в значении), их обработка всегда падает. После
 * исчерпания ретраев сообщение уходит в DLT (с заголовками об исходном
 * топике/партиции/оффсете/причине), оффсет коммитится — партиция не блокируется
 * навсегда одним плохим сообщением.
 */
public final class ErrorHandlingDemo {
    public static final String TOPIC = "demo-errors";
    public static final String DLT_TOPIC = "demo-errors-dlt";
    public static final int PARTITIONS = 3;
    public static final int MAX_RETRIES = 3;
    public static final String POISON_MARKER = "POISON";

    private ErrorHandlingDemo() {
    }

    public static final class Result {
        int totalSent;
        int poisonSent;
        int goodProcessed;
        int sentToDlt;
        int dltConsumed;
    }

    /** Имитация обработки: "ядовитое" сообщение всегда падает. */
    private static void process(String value) {
        if (value.contains(POISON_MARKER)) {
            throw new RuntimeException("не удалось обработать сообщение: " + value);
        }
    }

    public static Result run(int messageCount, int poisonEvery) {
        Kafka.ensureTopic(TOPIC, PARTITIONS, (short) 1);
        Kafka.ensureTopic(DLT_TOPIC, 1, (short) 1);
        Result result = new Result();

        Properties producerProps = new Properties();
        producerProps.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        producerProps.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        producerProps.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        producerProps.put(ProducerConfig.ACKS_CONFIG, "all");

        try (KafkaProducer<String, String> producer = new KafkaProducer<>(producerProps)) {
            for (int i = 0; i < messageCount; i++) {
                boolean poison = (i % poisonEvery) == (poisonEvery - 1);
                String value = poison ? ("payload-" + i + "-" + POISON_MARKER) : ("payload-" + i);
                producer.send(new ProducerRecord<>(TOPIC, "key-" + i, value));
                result.totalSent++;
                if (poison) {
                    result.poisonSent++;
                }
            }
        }
        System.out.printf("  [errors] отправлено %d сообщений, из них ядовитых=%d%n",
                result.totalSent, result.poisonSent);

        Properties consumerProps = new Properties();
        consumerProps.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        consumerProps.put(ConsumerConfig.GROUP_ID_CONFIG, "errors-group");
        consumerProps.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        consumerProps.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        consumerProps.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        consumerProps.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "false");

        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(consumerProps);
             KafkaProducer<String, String> dltProducer = new KafkaProducer<>(producerProps)) {
            consumer.subscribe(List.of(TOPIC));
            int processedTotal = 0;
            long deadline = System.currentTimeMillis() + 30_000;
            while (processedTotal < result.totalSent && System.currentTimeMillis() < deadline) {
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(500));
                for (ConsumerRecord<String, String> r : records) {
                    boolean ok = false;
                    Exception lastError = null;
                    for (int attempt = 1; attempt <= MAX_RETRIES && !ok; attempt++) {
                        try {
                            process(r.value());
                            ok = true;
                        } catch (RuntimeException e) {
                            lastError = e;
                            System.out.printf("  [errors] попытка %d/%d провалилась для offset=%d value=%s: %s%n",
                                    attempt, MAX_RETRIES, r.offset(), r.value(), e.getMessage());
                            if (attempt < MAX_RETRIES) {
                                Kafka.sleep(150L * attempt); // экспоненциальный бэкофф
                            }
                        }
                    }
                    if (ok) {
                        result.goodProcessed++;
                    } else {
                        List<Header> headers = List.of(
                                new RecordHeader("x-original-topic", TOPIC.getBytes(StandardCharsets.UTF_8)),
                                new RecordHeader("x-original-partition",
                                        String.valueOf(r.partition()).getBytes(StandardCharsets.UTF_8)),
                                new RecordHeader("x-original-offset",
                                        String.valueOf(r.offset()).getBytes(StandardCharsets.UTF_8)),
                                new RecordHeader("x-error",
                                        String.valueOf(lastError).getBytes(StandardCharsets.UTF_8))
                        );
                        dltProducer.send(new ProducerRecord<>(DLT_TOPIC, null, r.key(), r.value(), headers));
                        dltProducer.flush();
                        result.sentToDlt++;
                        System.out.printf("  [errors] offset=%d value=%s -> DLT после %d неудачных попыток%n",
                                r.offset(), r.value(), MAX_RETRIES);
                    }
                    // Оффсет коммитится в любом случае (успех или отправка в DLT) —
                    // "ядовитое" сообщение не блокирует партицию навсегда.
                    consumer.commitSync();
                    processedTotal++;
                }
            }
        }

        result.dltConsumed = consumeDlt(result.sentToDlt);
        System.out.printf("  [errors] итог: обработано успешно=%d, в DLT=%d (перепроверено чтением DLT=%d)%n",
                result.goodProcessed, result.sentToDlt, result.dltConsumed);
        return result;
    }

    private static int consumeDlt(int expected) {
        Properties consumerProps = new Properties();
        consumerProps.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        consumerProps.put(ConsumerConfig.GROUP_ID_CONFIG, "errors-dlt-verify-group");
        consumerProps.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        consumerProps.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        consumerProps.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        consumerProps.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "true");

        int consumed = 0;
        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(consumerProps)) {
            consumer.subscribe(List.of(DLT_TOPIC));
            long deadline = System.currentTimeMillis() + 15_000;
            while (consumed < expected && System.currentTimeMillis() < deadline) {
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(500));
                for (ConsumerRecord<String, String> r : records) {
                    consumed++;
                    Header origOffset = r.headers().lastHeader("x-original-offset");
                    String origOffsetStr = origOffset == null ? "?" : new String(origOffset.value(), StandardCharsets.UTF_8);
                    System.out.printf("  [dlt-consumer] %s = %s (original-offset=%s)%n", r.key(), r.value(), origOffsetStr);
                }
            }
        }
        return consumed;
    }
}
