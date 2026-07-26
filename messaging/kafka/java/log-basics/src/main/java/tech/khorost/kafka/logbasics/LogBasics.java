package tech.khorost.kafka.logbasics;

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
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.TreeMap;
import java.util.TreeSet;
import java.util.concurrent.ExecutionException;

/**
 * Стенд #1 серии "Kafka: глубокое погружение": ментальная модель лога —
 * топик, партиции, ключ → партиция, порядок в пределах партиции. Сценарий
 * идентичен Go-версии (../../go/log-basics/main.go): тот же топик demo-log,
 * те же ключи, то же число сообщений на ключ.
 */
public final class LogBasics {
    public static final String TOPIC = "demo-log";
    public static final int PARTITIONS = 3;
    public static final short REPLICATION = 3;
    public static final int MESSAGES_PER_KEY = 5;

    // Ключи намеренно избыточны относительно числа партиций (6 ключей на 3
    // партиции) — почти наверняка увидим коллизию murmur2 % 3 (два разных
    // ключа в одной партиции), и явно зафиксируем это как факт, а не
    // предположение.
    public static final List<String> KEYS = List.of(
            "user-1", "user-2", "user-3", "user-4", "user-5", "user-6");

    private LogBasics() {
    }

    record Sent(String key, String value, int partition, long offset) {
    }

    record Recv(int partition, long offset, String key, String value) {
    }

    public static void run() throws ExecutionException, InterruptedException {
        Kafka.recreateTopic(TOPIC, PARTITIONS, REPLICATION);

        List<Sent> sent = produce();
        System.out.printf("[producer] всего отправлено: %d%n", sent.size());

        List<Recv> recv = consume(sent.size());
        System.out.printf("[consumer] всего получено: %d%n", recv.size());

        printDistribution(sent);
        printRecords(recv);
        runAsserts(sent, recv);

        System.out.println("[assert] все проверки пройдены (key→partition, монотонность offset, sent==received)");
    }

    /**
     * Отправляет MESSAGES_PER_KEY сообщений на каждый ключ из KEYS, чередуя
     * ключи по раундам (round-0: все ключи по разу, round-1: все ключи ещё
     * по разу, ...) — синхронно (get() на каждый send), чтобы порядок
     * отправки был полностью детерминирован.
     */
    private static List<Sent> produce() throws ExecutionException, InterruptedException {
        Properties props = new Properties();
        props.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.ACKS_CONFIG, "all");

        List<Sent> results = new ArrayList<>();
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(props)) {
            for (int round = 0; round < MESSAGES_PER_KEY; round++) {
                for (String key : KEYS) {
                    String value = key + "-msg-" + round;
                    RecordMetadata meta = producer.send(new ProducerRecord<>(TOPIC, key, value)).get();
                    results.add(new Sent(key, value, meta.partition(), meta.offset()));
                }
            }
        }
        return results;
    }

    /** Читает demo-log с начала, пока не получит ровно expected записей (иначе — таймаут-фейл). */
    private static List<Recv> consume(int expected) {
        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, "log-basics-group");
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        props.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "true");

        List<Recv> out = new ArrayList<>();
        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(props)) {
            consumer.subscribe(List.of(TOPIC));
            long deadline = System.currentTimeMillis() + 30_000;
            while (out.size() < expected && System.currentTimeMillis() < deadline) {
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(500));
                for (ConsumerRecord<String, String> r : records) {
                    out.add(new Recv(r.partition(), r.offset(), r.key(), r.value()));
                }
            }
        }
        if (out.size() < expected) {
            throw new IllegalStateException(
                    "consume: таймаут, получено " + out.size() + " из " + expected + " ожидаемых");
        }
        return out;
    }

    private static void printDistribution(List<Sent> sent) {
        Map<Integer, TreeSet<String>> byPartition = new TreeMap<>();
        for (Sent s : sent) {
            byPartition.computeIfAbsent(s.partition(), p -> new TreeSet<>()).add(s.key());
        }
        System.out.println();
        System.out.println("[распределение] ключ → партиция (murmur2(key) % partitions, дефолтный партиционер kafka-clients):");
        byPartition.forEach((p, ks) -> System.out.printf("  partition %d: %s%n", p, String.join(", ", ks)));
    }

    private static void printRecords(List<Recv> recv) {
        List<Recv> sorted = new ArrayList<>(recv);
        sorted.sort((a, b) -> a.partition() != b.partition()
                ? Integer.compare(a.partition(), b.partition())
                : Long.compare(a.offset(), b.offset()));
        System.out.println();
        System.out.println("[consumer] записи по (partition, offset):");
        for (Recv r : sorted) {
            System.out.printf("  partition=%d offset=%d key=%s value=%s%n", r.partition(), r.offset(), r.key(), r.value());
        }
    }

    /** Падает (AssertionError/IllegalStateException) при малейшем расхождении — условие честности стенда. */
    private static void runAsserts(List<Sent> sent, List<Recv> recv) {
        if (sent.size() != recv.size()) {
            throw new AssertionError("[assert] FAIL: отправлено " + sent.size() + " != получено " + recv.size());
        }

        Map<String, Integer> sentKeyPart = new LinkedHashMap<>();
        for (Sent s : sent) {
            Integer prev = sentKeyPart.putIfAbsent(s.key(), s.partition());
            if (prev != null && prev != s.partition()) {
                throw new AssertionError("[assert] FAIL (producer): ключ " + s.key()
                        + " встречен в разных партициях: " + prev + " и " + s.partition());
            }
        }
        Map<String, Integer> recvKeyPart = new LinkedHashMap<>();
        for (Recv r : recv) {
            Integer prev = recvKeyPart.putIfAbsent(r.key(), r.partition());
            if (prev != null && prev != r.partition()) {
                throw new AssertionError("[assert] FAIL (consumer): ключ " + r.key()
                        + " встречен в разных партициях: " + prev + " и " + r.partition());
            }
        }
        for (var e : sentKeyPart.entrySet()) {
            Integer recvPart = recvKeyPart.get(e.getKey());
            if (!e.getValue().equals(recvPart)) {
                throw new AssertionError("[assert] FAIL: ключ " + e.getKey()
                        + " — партиция у producer=" + e.getValue() + ", у consumer=" + recvPart);
            }
        }
        System.out.println("[assert] OK: каждый ключ — в ровно одной партиции (producer и consumer согласны)");

        Map<Integer, List<Recv>> byPartition = new TreeMap<>();
        for (Recv r : recv) {
            byPartition.computeIfAbsent(r.partition(), p -> new ArrayList<>()).add(r);
        }
        for (var entry : byPartition.entrySet()) {
            List<Recv> rs = entry.getValue();
            rs.sort((a, b) -> Long.compare(a.offset(), b.offset()));
            for (int i = 1; i < rs.size(); i++) {
                long prevOffset = rs.get(i - 1).offset();
                long curOffset = rs.get(i).offset();
                if (curOffset != prevOffset + 1) {
                    throw new AssertionError("[assert] FAIL: партиция " + entry.getKey()
                            + " — offset не монотонен подряд: " + prevOffset + " затем " + curOffset);
                }
            }
        }
        System.out.println("[assert] OK: offset монотонно растёт (шаг 1) в пределах каждой партиции");

        System.out.printf("[assert] OK: отправлено == получено == %d%n", sent.size());
    }
}
