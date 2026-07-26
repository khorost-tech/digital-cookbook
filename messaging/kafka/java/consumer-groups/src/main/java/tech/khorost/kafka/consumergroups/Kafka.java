package tech.khorost.kafka.consumergroups;

import org.apache.kafka.clients.admin.AdminClient;
import org.apache.kafka.clients.admin.AdminClientConfig;
import org.apache.kafka.clients.admin.NewTopic;

import java.util.List;
import java.util.Properties;
import java.util.concurrent.ExecutionException;

/**
 * Общая точка конфигурации стенда. BOOTSTRAP — ВНУТРЕННИЕ (compose-net)
 * листенеры трёх брокеров кластера, не host-порты, см. ../../../README.md.
 * Топик demo-groups общий для всех четырёх сценариев (у каждого свой
 * group.id, поэтому сценарии друг другу не мешают).
 */
public final class Kafka {
    public static final String BOOTSTRAP =
            System.getenv().getOrDefault("KAFKA_BOOTSTRAP", "kafka1:9092,kafka2:9092,kafka3:9092");
    public static final String TOPIC = "demo-groups";
    public static final int PARTITIONS = 6;
    public static final short REPLICATION = 3;

    private Kafka() {
    }

    public static Properties baseProps() {
        Properties p = new Properties();
        p.put(AdminClientConfig.BOOTSTRAP_SERVERS_CONFIG, BOOTSTRAP);
        return p;
    }

    /** Идемпотентно (пере)создаёт demo-groups (удаляет, если уже есть, создаёт заново). */
    public static void ensureTopic() {
        try (AdminClient admin = AdminClient.create(baseProps())) {
            try {
                admin.deleteTopics(List.of(TOPIC)).all().get();
                waitUntilAbsent(admin);
            } catch (ExecutionException e) {
                // топика могло не быть на первом прогоне — не критично
            }
            NewTopic topic = new NewTopic(TOPIC, PARTITIONS, REPLICATION);
            admin.createTopics(List.of(topic)).all().get();
            System.out.printf("[admin] топик %s создан (partitions=%d, rf=%d)%n", TOPIC, PARTITIONS, REPLICATION);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException(e);
        } catch (ExecutionException e) {
            throw new RuntimeException("Не удалось создать топик " + TOPIC, e);
        }
    }

    private static void waitUntilAbsent(AdminClient admin) {
        long deadline = System.currentTimeMillis() + 10_000;
        while (System.currentTimeMillis() < deadline) {
            try {
                var names = admin.listTopics().names().get();
                if (!names.contains(TOPIC)) {
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
