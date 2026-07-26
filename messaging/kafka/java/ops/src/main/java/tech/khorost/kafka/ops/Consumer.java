package tech.khorost.kafka.ops;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.consumer.OffsetAndMetadata;
import org.apache.kafka.common.TopicPartition;
import org.apache.kafka.common.serialization.StringDeserializer;

import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Properties;

/**
 * Консьюмерские сценарии стенда #6 — зеркало ../../go/ops/consumer.go:
 * lag-consume ("медленный" консьюмер для демонстрации consumer lag,
 * коммитит СРАЗУ после обработки каждой записи, чтобы committed-offset,
 * видимый kafka-consumer-groups.sh --describe, точно отражал прогресс) и
 * tuning-consumer (измерение throughput при заданных fetch.min.bytes /
 * fetch.max.wait.ms / max.poll.records — в отличие от franz-go, у Java
 * kafka-clients max.poll.records есть НАПРЯМУЮ как ConsumerConfig, без
 * эмуляции через ограничение размера одного poll()).
 */
public final class Consumer {
    private Consumer() {
    }

    private static Properties baseConsumerProps(String group) {
        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, group);
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        props.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, false);
        return props;
    }

    public static void lagConsume(String topic, String group, int slowCount, long slowDelayMs, long runForMs, long idleMs) {
        Properties props = baseConsumerProps(group);
        int processed = 0;
        long start = System.currentTimeMillis();
        long deadline = start + runForMs;
        long lastProgress = System.currentTimeMillis();
        long lastLog = System.currentTimeMillis();

        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(props)) {
            consumer.subscribe(List.of(topic));
            while (System.currentTimeMillis() < deadline) {
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(2000));
                int n = 0;
                for (ConsumerRecord<String, String> r : records) {
                    n++;
                    boolean slow = processed < slowCount;
                    if (slow) {
                        Kafka.sleep(slowDelayMs);
                    }
                    processed++;
                    TopicPartition tp = new TopicPartition(r.topic(), r.partition());
                    consumer.commitSync(Map.of(tp, new OffsetAndMetadata(r.offset() + 1)));
                }
                if (n > 0) {
                    lastProgress = System.currentTimeMillis();
                } else if (System.currentTimeMillis() - lastProgress > idleMs) {
                    System.out.printf("[lag-consume] idle %dms без новых записей — завершаю раньше run-for%n", idleMs);
                    break;
                }
                if (System.currentTimeMillis() - lastLog > 2000) {
                    String mode = processed >= slowCount ? "быстрый (drain)" : "МЕДЛЕННЫЙ";
                    System.out.printf("[lag-consume] прогресс: обработано=%d режим=%s%n", processed, mode);
                    lastLog = System.currentTimeMillis();
                }
            }
        }
        System.out.printf("[lag-consume] ЗАВЕРШЕНО: обработано=%d за %dms (slow-count=%d slow-delay=%dms)%n",
                processed, System.currentTimeMillis() - start, slowCount, slowDelayMs);
    }

    public static void tuningConsume(String topic, String group, int fetchMinBytes, int fetchMaxWaitMs, int maxPollRecords, long idleMs, String label) {
        Properties props = baseConsumerProps(group);
        props.put(ConsumerConfig.FETCH_MIN_BYTES_CONFIG, fetchMinBytes);
        props.put(ConsumerConfig.FETCH_MAX_WAIT_MS_CONFIG, fetchMaxWaitMs);
        props.put(ConsumerConfig.MAX_POLL_RECORDS_CONFIG, maxPollRecords);

        long total = 0;
        int polls = 0, nonEmptyPolls = 0;
        long lastProgress = System.currentTimeMillis();
        long firstRecordAt = 0, lastRecordAt = 0;

        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(props)) {
            consumer.subscribe(List.of(topic));
            while (System.currentTimeMillis() - lastProgress <= idleMs) {
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(2000));
                polls++;
                int n = records.count();
                if (n > 0) {
                    long now = System.currentTimeMillis();
                    if (firstRecordAt == 0) firstRecordAt = now;
                    lastRecordAt = now;
                    total += n;
                    nonEmptyPolls++;
                    lastProgress = now;
                }
            }
        }
        // elapsed — окно РЕАЛЬНОЙ передачи данных (первая..последняя непустая
        // запись), НЕ полный цикл (который включает idle-хвост ожидания перед
        // выходом и завышал бы "elapsed", занижая throughput — тот же баг,
        // пойманный живьём в ../../go/ops/consumer.go, исправлен здесь синхронно).
        long elapsedMs = Math.max(1, lastRecordAt - firstRecordAt);
        double elapsedS = elapsedMs / 1000.0;
        double avgPerPoll = nonEmptyPolls > 0 ? (double) total / nonEmptyPolls : 0.0;
        double throughput = total / elapsedS;
        System.out.printf("[tuning-consumer] %s -> total=%d непустых-poll=%d avg-records/poll=%.1f elapsed(данные)=%.3fs throughput=%.1f msg/s%n",
                label, total, nonEmptyPolls, avgPerPoll, elapsedS, throughput);
    }
}
