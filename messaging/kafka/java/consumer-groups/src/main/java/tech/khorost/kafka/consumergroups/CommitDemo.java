package tech.khorost.kafka.consumergroups;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.consumer.OffsetAndMetadata;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.TopicPartition;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;

import java.time.Duration;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Разница между auto-commit (риск at-most-once: офсет коммитится по
 * таймеру/позиции чтения независимо от того, доделана ли фактическая
 * обработка) и ручным commitSync ПОСЛЕ обработки (at-least-once: при сбое
 * до коммита запись передоставляется повторно, а не теряется).
 */
public final class CommitDemo {
    static final int MESSAGES = 20;
    static final long PROCESS_DELAY_MS = 400;

    private CommitDemo() {
    }

    public static void run() throws Exception {
        autoCommitAtMostOnce();
        manualCommitAtLeastOnce();
    }

    // ---------------- auto-commit: риск at-most-once ----------------

    /**
     * Моделирует реалистичный баг auto-commit: consumer раздаёт полученные
     * записи в фоновые "воркеры" (имитация асинхронной обработки — очередь,
     * вызов другого сервиса) и СРАЗУ возвращается за следующей пачкой, не
     * дожидаясь их завершения. Позиция чтения (а с ней auto-commit) уходит
     * вперёд быстрее, чем реально завершается обработка. Если процесс
     * "падает" после того как офсет уже автокоммитнут, но воркер ещё не
     * доделал — запись потеряна безвозвратно.
     */
    private static void autoCommitAtMostOnce() throws Exception {
        String groupId = "cg-commit-auto";
        System.out.println("\n--- commit: auto-commit (риск at-most-once) ---");

        AtomicLong dispatched = new AtomicLong();
        AtomicLong trulyProcessed = new AtomicLong();
        AtomicBoolean crashed = new AtomicBoolean(false);

        Member.Processor process = (m, records) -> {
            for (ConsumerRecord<String, String> r : records) {
                dispatched.incrementAndGet();
                long off = r.offset();
                String val = r.value();
                Thread worker = new Thread(() -> {
                    Kafka.sleep(PROCESS_DELAY_MS); // "бизнес-логика" в фоне, poll-цикл дальше НЕ ждёт
                    if (crashed.get()) {
                        Log.f("[auto] воркер доделал offset=%d value=%s ПОСЛЕ 'краша' — результат потерян (в реальности процесс уже мёртв)", off, val);
                        return;
                    }
                    long n = trulyProcessed.incrementAndGet();
                    Log.f("[auto] воркер доделал offset=%d value=%s (итого доделано=%d)", off, val, n);
                });
                worker.setDaemon(true);
                worker.start();
            }
        };

        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, groupId);
        props.put(ConsumerConfig.CLIENT_ID_CONFIG, "auto-1");
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "latest");
        props.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "true");
        props.put(ConsumerConfig.AUTO_COMMIT_INTERVAL_MS_CONFIG, 100);

        Member c1 = new Member("auto-1", props, process);
        c1.waitFirstAssign(Duration.ofSeconds(15));
        Kafka.sleep(300);

        int sent = produceSync("auto-msg-", MESSAGES);
        Log.f("[auto] отправлено %d сообщений", sent);

        long deadline = System.currentTimeMillis() + 5000;
        while (dispatched.get() < sent && System.currentTimeMillis() < deadline) {
            Kafka.sleep(50);
        }
        if (dispatched.get() < sent) {
            throw new IllegalStateException(String.format(
                    "[assert] FAIL (auto-commit): консьюмер не забрал все %d сообщений (забрал %d)", sent, dispatched.get()));
        }
        // автокоммиту (интервал 100мс) хватит времени тикнуть и закоммитить
        // позицию чтения; воркерам (спят 400мс) — НЕ хватит.
        Kafka.sleep(250);

        crashed.set(true);
        c1.close(); // "краш": просто останавливаем поллинг, руками ничего не коммитим

        Log.f("[auto] 'краш': раздиспатчено=%d, реально доделано ДО краша=%d", dispatched.get(), trulyProcessed.get());

        AtomicLong resumed = new AtomicLong();
        Member.Processor resumeProcess = (m, records) -> {
            for (ConsumerRecord<String, String> r : records) {
                resumed.incrementAndGet();
                Log.f("[auto] (новый консьюмер, resume) offset=%d value=%s", r.offset(), r.value());
            }
        };
        Properties props2 = (Properties) props.clone();
        props2.put(ConsumerConfig.CLIENT_ID_CONFIG, "auto-2");
        Member c2 = new Member("auto-2", props2, resumeProcess);
        c2.waitFirstAssign(Duration.ofSeconds(15));
        Kafka.sleep(1500);
        c2.close();

        // дать зависшим воркерам добежать — только чтобы честно залогировать
        // их (уже помеченный потерянным) результат.
        Kafka.sleep(PROCESS_DELAY_MS + 200);

        long seenTotal = trulyProcessed.get() + resumed.get();
        Log.f("[auto] итог: отправлено=%d, реально доделано до краша=%d, дочитано новым консьюмером=%d, суммарно=%d",
                sent, trulyProcessed.get(), resumed.get(), seenTotal);

        if (seenTotal >= sent) {
            throw new IllegalStateException(String.format(
                    "[assert] FAIL (auto-commit): ожидалась потеря сообщений (auto-commit обогнал фоновую обработку), но seenTotal=%d >= sent=%d — гонка не проявилась в этом прогоне (тайминги host-зависимы)",
                    seenTotal, sent));
        }
        Log.f("[assert] OK (auto-commit): потеря продемонстрирована — отправлено=%d, суммарно доделано+дочитано=%d (< %d) => at-most-once",
                sent, seenTotal, sent);
    }

    // ---------------- manual commitSync: at-least-once ----------------

    private static void manualCommitAtLeastOnce() throws Exception {
        System.out.println("\n--- commit: ручной commitSync (at-least-once) ---");
        measureCommitLatency();
        crashBeforeCommitDemo();
    }

    /** Content-note: per-record commitSync (N round-trip'ов) медленнее одного батч-коммита. */
    private static void measureCommitLatency() throws Exception {
        final int n = 10;
        String groupId = "cg-commit-latency";

        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, groupId);
        props.put(ConsumerConfig.CLIENT_ID_CONFIG, "latency-consumer");
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "latest");
        props.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "false");

        // Свой ручной consumer (НЕ Member) — нужен единоличный контроль над
        // poll()/commitSync() без конкурирующего фонового потока обработки.
        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(props)) {
            consumer.subscribe(List.of(Kafka.TOPIC));
            consumer.poll(Duration.ofSeconds(5)); // установить членство/назначение партиций
            Kafka.sleep(500);

            produceSync("latency-", n);

            List<ConsumerRecord<String, String>> recs = new ArrayList<>();
            long deadline = System.currentTimeMillis() + 15_000;
            while (recs.size() < n && System.currentTimeMillis() < deadline) {
                ConsumerRecords<String, String> polled = consumer.poll(Duration.ofMillis(500));
                for (ConsumerRecord<String, String> r : polled) {
                    recs.add(r);
                }
            }
            if (recs.size() < n) {
                throw new IllegalStateException(String.format("[commit-latency] не дождались %d сообщений (получено %d)", n, recs.size()));
            }

            long start = System.nanoTime();
            for (ConsumerRecord<String, String> r : recs) {
                consumer.commitSync(Map.of(new TopicPartition(r.topic(), r.partition()), new OffsetAndMetadata(r.offset() + 1)));
            }
            long perRecordNs = System.nanoTime() - start;

            Map<TopicPartition, OffsetAndMetadata> batchOffsets = batchOffsets(recs);
            start = System.nanoTime();
            consumer.commitSync(batchOffsets);
            long batchNs = System.nanoTime() - start;

            Log.f("[commit-latency] %d коммитов по одной записи: %.1fms (%.2fms/коммит) vs 1 батч-коммит на %d записей: %.1fms",
                    n, perRecordNs / 1_000_000.0, perRecordNs / 1_000_000.0 / n, n, batchNs / 1_000_000.0);
            Log.f("[commit-latency] абсолютные мс — host-зависимая величина (сеть/диск брокера); важен ФАКТ (N round-trip'ов вместо одного), не точные цифры");
        }
    }

    /**
     * При отключённом autocommit и коммите ПОСЛЕ обработки, крах ДО коммита
     * ведёт к повторной доставке уже обработанных записей (дубли), но НЕ к
     * потере — идентифицируем сообщения по VALUE, а не по Kafka-offset:
     * топик demo-groups общий для всех сценариев, и у 6 партиций
     * независимые последовательности offset'ов.
     */
    private static void crashBeforeCommitDemo() throws Exception {
        String groupId = "cg-commit-manual";

        Set<String> processedValues = ConcurrentHashMap.newKeySet();
        AtomicLong processedRun1 = new AtomicLong();
        AtomicBoolean crashed = new AtomicBoolean(false);
        int crashAfterRun1 = 12;

        Member.Processor process = (m, records) -> {
            List<ConsumerRecord<String, String>> batchRecs = new ArrayList<>();
            for (ConsumerRecord<String, String> r : records) {
                if (crashed.get()) {
                    continue;
                }
                Kafka.sleep(PROCESS_DELAY_MS);
                long n = processedRun1.incrementAndGet();
                processedValues.add(r.value());
                batchRecs.add(r);
                Log.f("[manual] обработано offset=%d value=%s (итого в run1=%d)", r.offset(), r.value(), n);
                if (n == crashAfterRun1) {
                    crashed.set(true);
                }
            }
            if (batchRecs.isEmpty()) {
                return;
            }
            if (!crashed.get()) {
                // commitSync-аналог: коммитим ПОСЛЕ обработки всей пачки, не до.
                m.rawConsumer().commitSync(batchOffsets(batchRecs));
            } else {
                Log.f("[manual] 'краш' — пачка из %d записей обработана, но НЕ закоммичена (коммит не успел)", batchRecs.size());
            }
        };

        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, groupId);
        props.put(ConsumerConfig.CLIENT_ID_CONFIG, "manual-1");
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "latest");
        props.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "false");

        Member c1 = new Member("manual-1", props, process);
        c1.waitFirstAssign(Duration.ofSeconds(15));
        Kafka.sleep(300);

        int sent = produceSync("manual-msg-", MESSAGES);
        Log.f("[manual] отправлено %d сообщений", sent);

        long deadline = System.currentTimeMillis() + (long) (crashAfterRun1 + 2) * PROCESS_DELAY_MS * 2;
        while (!crashed.get() && System.currentTimeMillis() < deadline) {
            Kafka.sleep(50);
        }
        if (!crashed.get()) {
            throw new IllegalStateException(String.format("[assert] FAIL (manual-commit): не дождались обработки %d записей до 'краша'", crashAfterRun1));
        }
        Kafka.sleep(200);
        c1.close();

        Log.f("[manual] 'краш' после %d обработанных записей (закоммичено раньше, батчами, без последней незакоммиченной пачки)", processedRun1.get());

        Set<String> resumedValues = ConcurrentHashMap.newKeySet();
        Member.Processor resumeProcess = (m, records) -> {
            for (ConsumerRecord<String, String> r : records) {
                resumedValues.add(r.value());
                String dup = processedValues.contains(r.value())
                        ? " (ДУБЛЬ — уже обрабатывался в run1 до краша, но не был закоммичен)" : "";
                Log.f("[manual] (новый консьюмер, resume) offset=%d value=%s%s", r.offset(), r.value(), dup);
            }
        };
        Properties props2 = (Properties) props.clone();
        props2.put(ConsumerConfig.CLIENT_ID_CONFIG, "manual-2");
        Member c2 = new Member("manual-2", props2, resumeProcess);
        c2.waitFirstAssign(Duration.ofSeconds(15));
        Kafka.sleep(2000);
        c2.close();

        List<String> missing = new ArrayList<>();
        int dupCount = 0;
        for (int i = 0; i < sent; i++) {
            String val = "manual-msg-" + i;
            boolean inRun1 = processedValues.contains(val);
            boolean inRun2 = resumedValues.contains(val);
            if (!inRun1 && !inRun2) {
                missing.add(val);
            }
            if (inRun1 && inRun2) {
                dupCount++;
            }
        }
        if (!missing.isEmpty()) {
            throw new IllegalStateException("[assert] FAIL (manual-commit): потеряны сообщения " + missing + " — at-least-once нарушен");
        }
        Log.f("[assert] OK (manual-commit): все %d сообщений покрыты (run1=%d, run2=%d, дублей=%d) — потерь нет, at-least-once подтверждён",
                sent, processedValues.size(), resumedValues.size(), dupCount);
    }

    // ---------------- helpers ----------------

    private static Map<TopicPartition, OffsetAndMetadata> batchOffsets(List<ConsumerRecord<String, String>> recs) {
        Map<TopicPartition, OffsetAndMetadata> offsets = new HashMap<>();
        for (ConsumerRecord<String, String> r : recs) {
            TopicPartition tp = new TopicPartition(r.topic(), r.partition());
            OffsetAndMetadata cur = offsets.get(tp);
            if (cur == null || r.offset() + 1 > cur.offset()) {
                offsets.put(tp, new OffsetAndMetadata(r.offset() + 1));
            }
        }
        return offsets;
    }

    private static int produceSync(String valuePrefix, int n) throws Exception {
        Properties producerProps = new Properties();
        producerProps.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        producerProps.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        producerProps.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        int sent = 0;
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(producerProps)) {
            for (int i = 0; i < n; i++) {
                String key = "part-" + (i % Kafka.PARTITIONS);
                producer.send(new ProducerRecord<>(Kafka.TOPIC, key, valuePrefix + i)).get();
                sent++;
            }
        }
        return sent;
    }
}
