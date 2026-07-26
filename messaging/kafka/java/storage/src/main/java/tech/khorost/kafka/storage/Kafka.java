package tech.khorost.kafka.storage;

import org.apache.kafka.clients.admin.AdminClient;
import org.apache.kafka.clients.admin.AdminClientConfig;
import org.apache.kafka.clients.admin.ListOffsetsResult;
import org.apache.kafka.clients.admin.NewTopic;
import org.apache.kafka.clients.admin.OffsetSpec;
import org.apache.kafka.common.TopicPartition;

import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.concurrent.ExecutionException;

/**
 * Общая точка конфигурации + админ-операции стенда #4 (retention/compaction/
 * компрессия). Зеркало ../../go/storage/topic.go — идемпотентное
 * (пере)создание топика с конфигами, ожидание лидера, чтение earliest/latest
 * offset партиции 0 (единственный способ клиента увидеть эффект
 * retention-чистки без доступа к docker socket — см. ../../ops/inspect-segments.sh).
 */
public final class Kafka {
    public static final String BOOTSTRAP =
            System.getenv().getOrDefault("KAFKA_BOOTSTRAP", "kafka1:9092,kafka2:9092,kafka3:9092");

    private Kafka() {
    }

    public static Properties baseProps() {
        Properties p = new Properties();
        p.put(AdminClientConfig.BOOTSTRAP_SERVERS_CONFIG, BOOTSTRAP);
        return p;
    }

    public static void recreateTopic(String name, int partitions, short rf, Map<String, String> configs) {
        try (AdminClient admin = AdminClient.create(baseProps())) {
            try {
                admin.deleteTopics(List.of(name)).all().get();
                waitUntilAbsent(admin, name);
            } catch (ExecutionException e) {
                // топика могло не быть на первом прогоне — не критично
            }
            NewTopic topic = new NewTopic(name, partitions, rf);
            if (configs != null && !configs.isEmpty()) {
                topic = topic.configs(configs);
            }
            admin.createTopics(List.of(topic)).all().get();
            System.out.printf("[admin] топик %s создан (partitions=%d, rf=%d, configs=%s)%n", name, partitions, rf, configs);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException(e);
        } catch (ExecutionException e) {
            throw new RuntimeException("Не удалось создать топик " + name, e);
        }
    }

    public static void waitForLeader(String name, long timeoutMs) {
        long deadline = System.currentTimeMillis() + timeoutMs;
        while (System.currentTimeMillis() < deadline) {
            try (AdminClient admin = AdminClient.create(baseProps())) {
                var desc = admin.describeTopics(List.of(name)).allTopicNames().get();
                var td = desc.get(name);
                if (td != null && !td.partitions().isEmpty() && td.partitions().get(0).leader() != null
                        && td.partitions().get(0).leader().id() >= 0) {
                    System.out.printf("[admin] топик %s: лидер партиции 0 = %d%n", name, td.partitions().get(0).leader().id());
                    return;
                }
            } catch (Exception ignore) {
                // не готов — повторим
            }
            sleep(300);
        }
        throw new IllegalStateException("waitForLeader: топик " + name + " не получил лидера за " + timeoutMs + "мс");
    }

    /** earliest (log start offset) / latest (log end offset) партиции 0. */
    public record Offsets(long earliest, long latest) {
    }

    public static Offsets reportOffsets(String topic, String label) {
        try (AdminClient admin = AdminClient.create(baseProps())) {
            TopicPartition tp = new TopicPartition(topic, 0);
            ListOffsetsResult.ListOffsetsResultInfo start =
                    admin.listOffsets(Map.of(tp, OffsetSpec.earliest())).partitionResult(tp).get();
            ListOffsetsResult.ListOffsetsResultInfo end =
                    admin.listOffsets(Map.of(tp, OffsetSpec.latest())).partitionResult(tp).get();
            long earliest = start.offset();
            long latest = end.offset();
            System.out.printf("[offsets] %s: topic=%s earliest=%d latest=%d (записей сейчас читаемо: %d)%n",
                    label, topic, earliest, latest, latest - earliest);
            return new Offsets(earliest, latest);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException(e);
        } catch (ExecutionException e) {
            throw new RuntimeException("listOffsets " + topic, e);
        }
    }

    private static void waitUntilAbsent(AdminClient admin, String name) {
        long deadline = System.currentTimeMillis() + 10_000;
        while (System.currentTimeMillis() < deadline) {
            try {
                if (!admin.listTopics().names().get().contains(name)) {
                    return;
                }
            } catch (Exception ignore) {
                return;
            }
            sleep(300);
        }
    }

    public static void sleep(long ms) {
        try {
            Thread.sleep(ms);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
