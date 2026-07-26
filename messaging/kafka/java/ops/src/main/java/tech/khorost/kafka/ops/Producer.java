package tech.khorost.kafka.ops;

import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.serialization.StringSerializer;

import java.util.Properties;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Продюсерские сценарии стенда #6 — зеркало ../../go/ops/producer.go: seed
 * (быстрое наполнение топика для lag/tuning-consumer), seed-continuous
 * (фоновый продюсер для lag-демо), tuning-producer (управляемые
 * batch.size/linger.ms/compression.type), quota-produce (непрерывная отправка
 * под заданным client.id для демонстрации quotas).
 */
public final class Producer {
    private Producer() {
    }

    static String pad(String base, int targetBytes, char filler) {
        if (base.length() >= targetBytes) return base;
        StringBuilder sb = new StringBuilder(base);
        while (sb.length() < targetBytes) sb.append(filler);
        return sb.toString();
    }

    private static Properties baseProducerProps(String clientId) {
        Properties props = new Properties();
        props.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.ACKS_CONFIG, "all");
        if (clientId != null && !clientId.isEmpty()) {
            props.put(ProducerConfig.CLIENT_ID_CONFIG, clientId);
        }
        return props;
    }

    public static void seedFast(String topic, int n, int valueBytes, String prefix) {
        Properties props = baseProducerProps(null);
        AtomicLong acked = new AtomicLong();
        AtomicLong failed = new AtomicLong();
        long start = System.nanoTime();
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(props)) {
            for (int i = 0; i < n; i++) {
                String value = pad(String.format("%s-%08d-", prefix, i), valueBytes, 'v');
                producer.send(new ProducerRecord<>(topic, value), (meta, ex) -> {
                    if (ex == null) acked.incrementAndGet();
                    else failed.incrementAndGet();
                });
            }
            producer.flush();
        }
        double elapsedS = (System.nanoTime() - start) / 1e9;
        System.out.printf("[seed] topic=%s: отправлено %d записей (~%d байт значение) acked=%d failed=%d за %.3fs%n",
                topic, n, valueBytes, acked.get(), failed.get(), elapsedS);
    }

    public static void seedContinuous(String topic, long durationMs, int ratePerSec, int valueBytes) {
        Properties props = baseProducerProps(null);
        long intervalMs = Math.max(1, 1000 / Math.max(1, ratePerSec));
        AtomicLong sent = new AtomicLong();
        long deadline = System.currentTimeMillis() + durationMs;
        long lastLog = System.currentTimeMillis();
        int i = 0;
        System.out.printf("[seed-continuous] СТАРТ topic=%s rate=%d/s duration=%dms (~%d записей ожидается)%n",
                topic, ratePerSec, durationMs, (durationMs / 1000) * ratePerSec);
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(props)) {
            while (System.currentTimeMillis() < deadline) {
                String value = pad(String.format("lag-%08d-", i), valueBytes, 'l');
                i++;
                producer.send(new ProducerRecord<>(topic, value), (meta, ex) -> {
                    if (ex == null) sent.incrementAndGet();
                });
                if (System.currentTimeMillis() - lastLog > 2000) {
                    System.out.printf("[seed-continuous] прогресс: отправлено=%d%n", sent.get());
                    lastLog = System.currentTimeMillis();
                }
                Kafka.sleep(intervalMs);
            }
            producer.flush();
        }
        System.out.printf("[seed-continuous] ЗАВЕРШЕНО: всего отправлено %d записей за %dms (целевой темп %d/s)%n",
                sent.get(), durationMs, ratePerSec);
    }

    public static void runTuningProducer(String topic, int n, int valueBytes, int batchBytes, int lingerMs, String compression, String clientId, String label) {
        Properties props = baseProducerProps(clientId);
        props.put(ProducerConfig.BATCH_SIZE_CONFIG, batchBytes);
        props.put(ProducerConfig.LINGER_MS_CONFIG, lingerMs);
        props.put(ProducerConfig.COMPRESSION_TYPE_CONFIG, compression);

        AtomicLong acked = new AtomicLong();
        AtomicLong failed = new AtomicLong();
        CountDownLatch latch = new CountDownLatch(n);
        long start = System.nanoTime();
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(props)) {
            for (int i = 0; i < n; i++) {
                String value = pad(String.format("tune-%08d-", i), valueBytes, 't');
                producer.send(new ProducerRecord<>(topic, value), (meta, ex) -> {
                    if (ex == null) acked.incrementAndGet();
                    else failed.incrementAndGet();
                    latch.countDown();
                });
            }
            producer.flush();
            await(latch);
        }
        double elapsedS = (System.nanoTime() - start) / 1e9;
        double throughput = n / elapsedS;
        double mbps = throughput * valueBytes / (1024.0 * 1024.0);
        System.out.printf("[tuning-producer] %s n=%d value-bytes=%d -> acked=%d/%d failed=%d elapsed=%.3fs throughput=%.1f msg/s (%.2f MB/s)%n",
                label, n, valueBytes, acked.get(), n, failed.get(), elapsedS, throughput, mbps);
    }

    public static void quotaProduce(String topic, String clientId, long durationMs, int valueBytes) {
        Properties props = baseProducerProps(clientId);
        AtomicLong acked = new AtomicLong();
        AtomicLong failed = new AtomicLong();
        AtomicLong sentCount = new AtomicLong();
        long start = System.currentTimeMillis();
        long deadline = start + durationMs;
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(props)) {
            while (System.currentTimeMillis() < deadline) {
                long i = sentCount.getAndIncrement();
                String value = pad(String.format("quota-%08d-", i), valueBytes, 'q');
                producer.send(new ProducerRecord<>(topic, value), (meta, ex) -> {
                    if (ex == null) acked.incrementAndGet();
                    else failed.incrementAndGet();
                });
            }
            producer.flush();
        }
        double elapsedS = (System.currentTimeMillis() - start) / 1000.0;
        double throughput = acked.get() / elapsedS;
        double mbps = throughput * valueBytes / (1024.0 * 1024.0);
        System.out.printf("[quota-produce] client-id=%s duration=%.3fs -> acked=%d failed=%d throughput=%.1f msg/s (%.2f MB/s)%n",
                clientId, elapsedS, acked.get(), failed.get(), throughput, mbps);
    }

    private static void await(CountDownLatch latch) {
        try {
            latch.await();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
