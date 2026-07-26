package tech.khorost.kafka.ops;

import org.apache.kafka.clients.admin.AdminClient;
import org.apache.kafka.clients.admin.AdminClientConfig;
import org.apache.kafka.clients.admin.NewTopic;

import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.concurrent.ExecutionException;

/**
 * Общая точка конфигурации + админ-операции стенда #6 (эксплуатация). Зеркало
 * ../../go/ops/topic.go: идемпотентное (пере)создание топика с конфигами
 * (min.insync.replicas). Ручная/автоматическая перебалансировка,
 * rack-awareness и KRaft-кворум — операции над БРОКЕРОМ, не над клиентом,
 * поэтому здесь их нет — см. ../../../ops/reassign-demo.sh и
 * ../../../ops/rack-quorum-demo.sh (напрямую kafka-*.sh CLI).
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

    public static void waitTopicReady(String name, int expectPartitions, long timeoutMs) {
        long deadline = System.currentTimeMillis() + timeoutMs;
        try (AdminClient admin = AdminClient.create(baseProps())) {
            while (System.currentTimeMillis() < deadline) {
                try {
                    var desc = admin.describeTopics(List.of(name)).allTopicNames().get();
                    var td = desc.get(name);
                    if (td != null && td.partitions().size() == expectPartitions) {
                        return;
                    }
                } catch (Exception ignore) {
                    // топик ещё не виден в метаданных — повторим
                }
                sleep(300);
            }
        }
        throw new IllegalStateException("waitTopicReady: топик " + name + " не готов за " + timeoutMs + "мс");
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
