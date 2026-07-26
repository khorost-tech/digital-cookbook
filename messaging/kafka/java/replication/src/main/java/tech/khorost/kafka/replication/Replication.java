package tech.khorost.kafka.replication;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.clients.producer.RecordMetadata;
import org.apache.kafka.common.errors.NotEnoughReplicasException;
import org.apache.kafka.common.errors.TimeoutException;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Properties;
import java.util.TreeMap;
import java.util.TreeSet;
import java.util.concurrent.ExecutionException;

/**
 * Сценарии стенда #3 — зеркало ../../go/replication/main.go. Часть сценариев
 * (broker-kill) требует, чтобы docker kill/start между вызовами фаз выполнял
 * ХОСТ (см. ../../ops/broker-kill.sh), т.к. у клиента внутри контейнера нет
 * доступа к docker socket.
 */
public final class Replication {
    private Replication() {
    }

    public static void runAcksBench(String topic) {
        Kafka.recreateTopic(topic, 1, (short) 3, Map1("min.insync.replicas", "2"));
        Kafka.waitForLeader(topic, 30_000);

        String[] levels = {"0", "1", "all"};
        int perLevel = 200;
        for (String lvl : levels) {
            boolean idempotent = lvl.equals("all");
            Properties props = producerProps(lvl, idempotent, -1, 10_000);
            long start = System.nanoTime();
            int acked = 0, errs = 0;
            long minNs = Long.MAX_VALUE, maxNs = 0;
            try (KafkaProducer<String, String> producer = new KafkaProducer<>(props)) {
                for (int i = 0; i < perLevel; i++) {
                    long t0 = System.nanoTime();
                    try {
                        producer.send(new ProducerRecord<>(topic, "acks" + lvl + "-" + i)).get();
                        acked++;
                    } catch (Exception e) {
                        errs++;
                    }
                    long dt = System.nanoTime() - t0;
                    minNs = Math.min(minNs, dt);
                    maxNs = Math.max(maxNs, dt);
                }
            } catch (Exception e) {
                throw new RuntimeException(e);
            }
            double elapsedS = (System.nanoTime() - start) / 1e9;
            System.out.printf("[acks-bench] acks=%-3s acked=%d/%d errs=%d elapsed=%.3fs throughput=%.1f msg/s avg_latency=%.3fms min=%.3fms max=%.3fms%n",
                    lvl, acked, perLevel, errs, elapsedS, perLevel / elapsedS,
                    elapsedS * 1000 / perLevel, minNs / 1e6, maxNs / 1e6);
        }
        System.out.println("[acks-bench] характерный прогон (host-зависимые абсолютные числа); ожидаемый ПОРЯДОК: acks=0 быстрее acks=1 быстрее acks=all");
    }

    public static void runProduce(String topic, int n, String acks, boolean idempotent, int retries, int reqTimeoutMs, String prefix, int delayMs) {
        if (idempotent && !acks.equals("all")) {
            System.out.printf("[produce] idempotent=true несовместимо с acks=%s — выключаю idempotent для этого прогона%n", acks);
            idempotent = false;
        }
        Properties props = producerProps(acks, idempotent, retries, reqTimeoutMs);
        int acked = 0, failed = 0;
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(props)) {
            for (int i = 0; i < n; i++) {
                if (delayMs > 0 && i > 0) {
                    Kafka.sleep(delayMs);
                }
                String value = prefix + "-" + i;
                long t0 = System.nanoTime();
                try {
                    RecordMetadata meta = producer.send(new ProducerRecord<>(topic, value)).get();
                    double ms = (System.nanoTime() - t0) / 1e6;
                    System.out.printf("[produce] i=%d value=%s -> partition=%d offset=%d (%.3fms)%n", i, value, meta.partition(), meta.offset(), ms);
                    acked++;
                } catch (Exception e) {
                    double ms = (System.nanoTime() - t0) / 1e6;
                    System.out.printf("[produce] i=%d value=%s -> ОШИБКА: %s (%.3fms)%n", i, value, e.getCause() != null ? e.getCause() : e, ms);
                    failed++;
                }
            }
        }
        System.out.printf("[produce] итого: acked=%d failed=%d из %d (acks=%s idempotent=%s)%n", acked, failed, n, acks, idempotent);
    }

    public static void runVerify(String topic, int expect, int idleMs, boolean soft, boolean checkdup, boolean printIndices) {
        List<Recv> out = consumeFromStart(topic, expect, idleMs);
        System.out.printf("[verify] топик %s: прочитано %d записей%n", topic, out.size());
        List<Recv> sorted = new ArrayList<>(out);
        sorted.sort((a, b) -> a.partition() != b.partition() ? Integer.compare(a.partition(), b.partition()) : Long.compare(a.offset(), b.offset()));
        for (Recv r : sorted) {
            System.out.printf("  partition=%d offset=%d value=%s%n", r.partition(), r.offset(), r.value());
        }
        if (expect > 0) {
            if (out.size() != expect) {
                String msg = "[verify] РАСХОЖДЕНИЕ: прочитано " + out.size() + ", ожидалось " + expect;
                if (soft) {
                    System.out.println(msg);
                } else {
                    throw new AssertionError(msg);
                }
            } else {
                System.out.printf("[verify] OK: прочитано == ожидалось == %d%n", expect);
            }
        }
        if (checkdup) {
            var seen = new TreeMap<Integer, Integer>();
            for (Recv r : out) {
                seen.merge(extractIndex(r.value()), 1, Integer::sum);
            }
            int dupCount = 0;
            List<Integer> samples = new ArrayList<>();
            for (var e : seen.entrySet()) {
                if (e.getValue() > 1) {
                    dupCount += e.getValue() - 1;
                    if (samples.size() < 10) samples.add(e.getKey());
                }
            }
            if (dupCount > 0) {
                throw new AssertionError(String.format(
                        "[verify] ДУБЛИ: %d повторных вхождений среди %d уникальных индексов, примеры: %s",
                        dupCount, seen.size(), samples));
            } else {
                System.out.printf("[verify] OK: дублей индексов нет (%d уникальных)%n", seen.size());
            }
        }
        if (printIndices) {
            var idxs = new TreeSet<Integer>();
            for (Recv r : out) idxs.add(extractIndex(r.value()));
            System.out.printf("[verify] индексы в топике (%d шт.): %s%n", idxs.size(), idxs);
        }
    }

    public static void runMinISRProduce(String topic, String acks) {
        Properties props = producerProps(acks, false, 0, 6_000);
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(props)) {
            long start = System.nanoTime();
            String cls;
            Throwable errOut = null;
            try {
                producer.send(new ProducerRecord<>(topic, "minisr-probe")).get();
                cls = "OK";
            } catch (ExecutionException e) {
                Throwable cause = e.getCause();
                errOut = cause;
                if (cause instanceof NotEnoughReplicasException) {
                    cls = "NOT_ENOUGH_REPLICAS";
                } else if (cause instanceof TimeoutException) {
                    cls = "CLIENT_TIMEOUT";
                } else {
                    cls = "ДРУГАЯ ОШИБКА: " + cause;
                }
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                cls = "ПРЕРВАНО";
            }
            double ms = (System.nanoTime() - start) / 1e6;
            System.out.printf("[minisr-produce] topic=%s acks=%s -> %s (elapsed=%.1fms, err=%s)%n", topic, acks, cls, ms, errOut);
        }
    }

    // --- вспомогательное ---

    record Recv(int partition, long offset, String value) {
    }

    private static List<Recv> consumeFromStart(String topic, int expect, int idleMs) {
        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, "replication-verify-" + System.nanoTime());
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");

        List<Recv> out = new ArrayList<>();
        long overallDeadline = System.currentTimeMillis() + 60_000;
        long lastProgress = System.currentTimeMillis();
        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(props)) {
            consumer.subscribe(List.of(topic));
            while (true) {
                if (expect > 0 && out.size() >= expect) break;
                if (System.currentTimeMillis() > overallDeadline) {
                    if (expect > 0) {
                        throw new IllegalStateException("consume: общий таймаут, получено " + out.size() + " из " + expect);
                    }
                    break;
                }
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(2000));
                int n = 0;
                for (ConsumerRecord<String, String> r : records) {
                    out.add(new Recv(r.partition(), r.offset(), r.value()));
                    n++;
                }
                if (n > 0) {
                    lastProgress = System.currentTimeMillis();
                } else if (expect == 0 && System.currentTimeMillis() - lastProgress > idleMs) {
                    break;
                }
            }
        }
        return out;
    }

    private static int extractIndex(String value) {
        String[] parts = value.split("-");
        return Integer.parseInt(parts[parts.length - 1]);
    }

    private static Properties producerProps(String acks, boolean idempotent, int retries, int reqTimeoutMs) {
        Properties props = new Properties();
        props.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.ACKS_CONFIG, acks);
        props.put(ProducerConfig.ENABLE_IDEMPOTENCE_CONFIG, idempotent);
        props.put(ProducerConfig.REQUEST_TIMEOUT_MS_CONFIG, reqTimeoutMs);
        // delivery.timeout.ms должен быть >= request.timeout.ms + linger.ms; даём небольшой запас.
        props.put(ProducerConfig.DELIVERY_TIMEOUT_MS_CONFIG, reqTimeoutMs + 2000);
        if (retries >= 0) {
            props.put(ProducerConfig.RETRIES_CONFIG, retries);
        }
        return props;
    }

    private static java.util.Map<String, String> Map1(String k, String v) {
        return java.util.Map.of(k, v);
    }
}
