package tech.khorost.kafka.storage;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.serialization.ByteArrayDeserializer;
import org.apache.kafka.common.serialization.ByteArraySerializer;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.TreeMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * Сценарии стенда #4 — зеркало ../../go/storage/{producer,consumer,report}.go.
 * Использует byte[] key/value напрямую (не String), чтобы tombstone
 * (value=null) был однозначен независимо от сериализатора — тот же выбор,
 * что в franz-go версии (kgo.Record.Value == nil ЕСТЬ маркер удаления).
 */
public final class Storage {
    private Storage() {
    }

    // --- producer helpers ---

    /**
     * pad — строка длиной ровно targetBytes: база + заполнитель из filler.
     * ⚠️ compression.type у Java KafkaProducer по умолчанию "none" (в отличие
     * от franz-go, который без явного ProducerBatchCompression(...) выбирает
     * Snappy — реальная живая находка при отладке Go-версии этого стенда, см.
     * ../../go/storage/producer.go). Здесь ничего специально выключать не
     * нужно — producerProps() ниже не задаёт compression.type вовсе, и это
     * уже "none" по дефолту клиента.
     */
    static String pad(String base, int targetBytes, char filler) {
        if (base.length() >= targetBytes) {
            return base;
        }
        StringBuilder sb = new StringBuilder(base);
        while (sb.length() < targetBytes) {
            sb.append(filler);
        }
        return sb.toString();
    }

    static Properties producerProps(String compressionType) {
        Properties p = new Properties();
        p.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        p.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, ByteArraySerializer.class.getName());
        p.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, ByteArraySerializer.class.getName());
        p.put(ProducerConfig.ACKS_CONFIG, "all");
        if (compressionType != null && !compressionType.equals("none")) {
            p.put(ProducerConfig.COMPRESSION_TYPE_CONFIG, compressionType);
        }
        return p;
    }

    public static void produceUnkeyedSequential(String topic, int n, int padBytes) {
        try (KafkaProducer<byte[], byte[]> producer = new KafkaProducer<>(producerProps("none"))) {
            for (int i = 0; i < n; i++) {
                String value = pad(String.format("retention-%06d-", i), padBytes, 'x');
                try {
                    producer.send(new ProducerRecord<>(topic, value.getBytes(StandardCharsets.UTF_8))).get();
                } catch (Exception e) {
                    throw new RuntimeException("produce i=" + i, e);
                }
            }
        }
        System.out.printf("[produce] topic=%s: отправлено %d записей без ключа, ~%d байт значения каждая%n", topic, n, padBytes);
    }

    public static int produceKeyedUpdates(String topic, List<String> keys, int rounds, int padBytes) {
        int sent = 0;
        try (KafkaProducer<byte[], byte[]> producer = new KafkaProducer<>(producerProps("none"))) {
            for (int round = 0; round < rounds; round++) {
                for (String k : keys) {
                    String value = pad(k + "-round" + round + "-", padBytes, 'y');
                    try {
                        producer.send(new ProducerRecord<>(topic, k.getBytes(StandardCharsets.UTF_8), value.getBytes(StandardCharsets.UTF_8))).get();
                    } catch (Exception e) {
                        throw new RuntimeException("produce key=" + k + " round=" + round, e);
                    }
                    sent++;
                }
            }
        }
        System.out.printf("[produce] topic=%s: %d ключей x %d раундов = %d update-записей отправлено%n", topic, keys.size(), rounds, sent);
        return sent;
    }

    public static void produceTombstones(String topic, List<String> keys) {
        try (KafkaProducer<byte[], byte[]> producer = new KafkaProducer<>(producerProps("none"))) {
            for (String k : keys) {
                try {
                    // value=null — ЯВНО null, это и есть tombstone-маркер.
                    producer.send(new ProducerRecord<>(topic, k.getBytes(StandardCharsets.UTF_8), null)).get();
                } catch (Exception e) {
                    throw new RuntimeException("produce tombstone key=" + k, e);
                }
            }
        }
        System.out.printf("[produce] topic=%s: %d tombstone-записей отправлено (ключи: %s)%n", topic, keys.size(), keys);
    }

    /**
     * n записей с уникальными ключами roll-filler-NNNN (нумерация с startAt),
     * форсируют roll активного сегмента (segment.bytes). См. подробный
     * комментарий в ../../go/storage/producer.go про то, ПОЧЕМУ нумерация
     * между раундами filler'а должна продолжаться, а не начинаться с 0 —
     * иначе второй раунд перезапишет те же ключи первого, и cleaner
     * дедуплицирует filler как обычные обновления (не то, что нужно —
     * filler задуман как балласт, форсирующий roll, а не бизнес-данные).
     */
    public static void produceFiller(String topic, int n, int padBytes, int startAt) {
        try (KafkaProducer<byte[], byte[]> producer = new KafkaProducer<>(producerProps("none"))) {
            for (int i = 0; i < n; i++) {
                String key = String.format("roll-filler-%04d", startAt + i);
                String value = pad(key + "-", padBytes, 'z');
                try {
                    producer.send(new ProducerRecord<>(topic, key.getBytes(StandardCharsets.UTF_8), value.getBytes(StandardCharsets.UTF_8))).get();
                } catch (Exception e) {
                    throw new RuntimeException("produce filler i=" + i, e);
                }
            }
        }
        System.out.printf("[produce] topic=%s: %d filler-записей отправлено (форсируют roll сегмента, не участвуют в бизнес-ассертах)%n", topic, n);
    }

    /** Асинхронная батч-отправка (см. Go produceBatchedAsync) — даёт клиенту накопить реальные батчи, на которых компрессия работает. */
    public static void produceBatchedAsync(String topic, int n, int padBytes, String compressionType) {
        AtomicInteger acked = new AtomicInteger();
        AtomicInteger failed = new AtomicInteger();
        long start = System.nanoTime();
        try (KafkaProducer<byte[], byte[]> producer = new KafkaProducer<>(producerProps(compressionType))) {
            CountDownLatch latch = new CountDownLatch(n);
            for (int i = 0; i < n; i++) {
                String value = pad(String.format("compress-%08d-order_id=%d-user=user-%d-status=OK-region=eu-west-1-", i, i, i % 50), padBytes, 'c');
                producer.send(new ProducerRecord<>(topic, value.getBytes(StandardCharsets.UTF_8)), (meta, ex) -> {
                    if (ex == null) acked.incrementAndGet(); else failed.incrementAndGet();
                    latch.countDown();
                });
            }
            try {
                latch.await();
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
        }
        double elapsedS = (System.nanoTime() - start) / 1e9;
        System.out.printf("[produce] topic=%s: батч-отправка %d записей (acked=%d failed=%d) за %.3fs%n", topic, n, acked.get(), failed.get(), elapsedS);
    }

    // --- consumer / report ---

    public record Recv(int partition, long offset, String key, byte[] value) {
        boolean isTombstone() {
            return value == null;
        }
    }

    public static List<Recv> consumeAllFromStart(String topic, Duration idleTimeout) {
        Properties p = new Properties();
        p.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        p.put(ConsumerConfig.GROUP_ID_CONFIG, "storage-consume-" + System.nanoTime());
        p.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, ByteArrayDeserializer.class.getName());
        p.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, ByteArrayDeserializer.class.getName());
        p.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");

        List<Recv> out = new ArrayList<>();
        long overallDeadline = System.currentTimeMillis() + 60_000;
        long lastProgress = System.currentTimeMillis();
        try (KafkaConsumer<byte[], byte[]> consumer = new KafkaConsumer<>(p)) {
            consumer.subscribe(List.of(topic));
            while (true) {
                if (System.currentTimeMillis() > overallDeadline) break;
                ConsumerRecords<byte[], byte[]> records = consumer.poll(Duration.ofMillis(1000));
                int n = 0;
                for (ConsumerRecord<byte[], byte[]> r : records) {
                    String key = r.key() == null ? "" : new String(r.key(), StandardCharsets.UTF_8);
                    out.add(new Recv(r.partition(), r.offset(), key, r.value()));
                    n++;
                }
                if (n > 0) {
                    lastProgress = System.currentTimeMillis();
                } else if (System.currentTimeMillis() - lastProgress > idleTimeout.toMillis()) {
                    break;
                }
            }
        }
        out.sort((a, b) -> Long.compare(a.offset(), b.offset()));
        return out;
    }

    static String valueLabel(byte[] v) {
        return v == null ? "<tombstone/null>" : new String(v, StandardCharsets.UTF_8);
    }

    /** Сокращает длинные значения для читаемого вывода (см. Go report.go:truncateValue). Ассерты работают с ПОЛНЫМ значением. */
    static String truncateValue(String v) {
        final int maxLen = 48;
        if (v.equals("<tombstone/null>") || v.length() <= maxLen) {
            return v;
        }
        return v.substring(0, maxLen) + "...(len=" + v.length() + ")";
    }

    public static void printCompactState(String label, List<Recv> recv) {
        System.out.printf("%n[compact-consume] %s: всего читаемо %d записей%n", label, recv.size());
        Map<String, List<Recv>> byKey = new LinkedHashMap<>();
        for (Recv r : recv) {
            byKey.computeIfAbsent(r.key(), k -> new ArrayList<>()).add(r);
        }
        var sortedKeys = new TreeMap<String, List<Recv>>(byKey);
        int fillerCount = 0;
        for (var e : sortedKeys.entrySet()) {
            if (e.getKey().startsWith("roll-filler-")) {
                fillerCount += e.getValue().size();
                continue;
            }
            List<String> parts = new ArrayList<>();
            for (Recv r : e.getValue()) {
                parts.add("offset=" + r.offset() + ":" + truncateValue(valueLabel(r.value())));
            }
            System.out.printf("  key=%-16s (%d записей): %s%n", e.getKey(), e.getValue().size(), String.join(" | ", parts));
        }
        if (fillerCount > 0) {
            System.out.printf("  (+ %d filler-записей roll-filler-*, по одной уникальной на ключ, не показаны построчно)%n", fillerCount);
        }
    }

    /**
     * Fail-loud проверка финального состояния после компакции (см. Go
     * report.go:assertCompacted): каждый живой ключ встречается РОВНО один
     * раз со значением последнего раунда; каждый tombstone-ключ отсутствует
     * ПОЛНОСТЬЮ. filler-ключи в проверке не участвуют (уникальны по
     * определению, компакция их не трогает).
     */
    public static void assertCompacted(List<Recv> recv, List<String> keys, List<String> tombstoneKeys, int lastRound) {
        Map<String, List<Recv>> byKey = new LinkedHashMap<>();
        for (Recv r : recv) {
            byKey.computeIfAbsent(r.key(), k -> new ArrayList<>()).add(r);
        }
        var tomb = new java.util.HashSet<>(tombstoneKeys);
        String marker = "-round" + lastRound + "-";

        for (String k : keys) {
            if (tomb.contains(k)) {
                List<Recv> recs = byKey.get(k);
                if (recs != null) {
                    throw new AssertionError("[assert] FAIL: tombstone-ключ " + k + " всё ещё присутствует после компакции (" + recs.size() + " записей)");
                }
                continue;
            }
            List<Recv> recs = byKey.get(k);
            if (recs == null) {
                throw new AssertionError("[assert] FAIL: живой ключ " + k + " ПОЛНОСТЬЮ отсутствует после компакции (должен остаться с последним значением)");
            }
            if (recs.size() != 1) {
                throw new AssertionError("[assert] FAIL: живой ключ " + k + " встречен " + recs.size() + " раз(а) после компакции (ожидалась ровно 1)");
            }
            byte[] v = recs.get(0).value();
            if (v == null) {
                throw new AssertionError("[assert] FAIL: живой ключ " + k + " имеет tombstone-значение после компакции — не должен был получать tombstone");
            }
            String s = valueLabel(v);
            if (!s.contains(marker)) {
                throw new AssertionError("[assert] FAIL: живой ключ " + k + " — значение не содержит маркер последнего раунда " + marker + " (осталась СТАРАЯ версия)");
            }
        }
        System.out.printf("[assert] OK: %d живых ключей — ровно по 1 записи (последняя версия, маркер %s); %d tombstone-ключей — отсутствуют полностью%n",
                keys.size() - tombstoneKeys.size(), marker, tombstoneKeys.size());
    }
}
