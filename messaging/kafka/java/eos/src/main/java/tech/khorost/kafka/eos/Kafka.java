package tech.khorost.kafka.eos;

import org.apache.kafka.clients.admin.AdminClient;
import org.apache.kafka.clients.admin.AdminClientConfig;
import org.apache.kafka.clients.admin.NewTopic;
import org.apache.kafka.clients.consumer.OffsetAndMetadata;
import org.apache.kafka.common.TopicPartition;

import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.concurrent.ExecutionException;

/**
 * Общая точка конфигурации + админ-операции стенда #5 (EOS). Зеркало
 * ../../go/eos/topic.go: идемпотентное (пере)создание топика, удаление
 * consumer group (чистое состояние офсетов на каждый прогон), сумма
 * committed-офсетов группы по топику (для проверки атомарности
 * consume-process-produce — либо не продвинулась вовсе, либо продвинулась
 * РОВНО на n, никогда не оказывается посередине).
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

    public static void recreateTopic(String name, int partitions, short rf) {
        try (AdminClient admin = AdminClient.create(baseProps())) {
            try {
                admin.deleteTopics(List.of(name)).all().get();
                waitUntilAbsent(admin, name);
            } catch (ExecutionException e) {
                // топика могло не быть на первом прогоне — не критично
            }
            admin.createTopics(List.of(new NewTopic(name, partitions, rf))).all().get();
            System.out.printf("[admin] топик %s создан (partitions=%d, rf=%d)%n", name, partitions, rf);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException(e);
        } catch (ExecutionException e) {
            throw new RuntimeException("Не удалось создать топик " + name, e);
        }
    }

    /** Удаляет consumer group, если существует — чистое состояние на каждый прогон cpp-сценария. */
    public static void deleteGroup(String group) {
        try (AdminClient admin = AdminClient.create(baseProps())) {
            admin.deleteConsumerGroups(List.of(group)).all().get();
            System.out.printf("[admin] группа %s удалена%n", group);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        } catch (ExecutionException e) {
            // группа могла не существовать на первом прогоне — не критично
            System.out.printf("[admin] deleteConsumerGroups %s: %s (ок, если группа ещё не создавалась)%n", group, e.getMessage());
        }
    }

    /** Сумма committed-офсетов группы по всем партициям топика. */
    public static long groupCommittedTotal(String group, String topic) {
        try (AdminClient admin = AdminClient.create(baseProps())) {
            Map<TopicPartition, OffsetAndMetadata> offsets =
                    admin.listConsumerGroupOffsets(group).partitionsToOffsetAndMetadata().get();
            long total = 0;
            for (Map.Entry<TopicPartition, OffsetAndMetadata> e : offsets.entrySet()) {
                if (e.getKey().topic().equals(topic) && e.getValue() != null) {
                    total += e.getValue().offset();
                }
            }
            return total;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException(e);
        } catch (ExecutionException e) {
            // группа без единого committed-офсета (ещё не коммитила) — трактуем как 0
            return 0;
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
