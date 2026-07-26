package tech.khorost.kafka.replication;

import org.apache.kafka.clients.admin.AdminClient;
import org.apache.kafka.clients.admin.AdminClientConfig;
import org.apache.kafka.clients.admin.AlterConfigOp;
import org.apache.kafka.clients.admin.ConfigEntry;
import org.apache.kafka.clients.admin.NewTopic;
import org.apache.kafka.clients.admin.TopicDescription;
import org.apache.kafka.common.Node;
import org.apache.kafka.common.TopicPartitionInfo;
import org.apache.kafka.common.config.ConfigResource;

import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.concurrent.ExecutionException;

/**
 * Общая точка конфигурации + админ-операции стенда #3 (репликация/ISR/acks).
 * Зеркало ../../go/replication/topic.go — тот же набор операций: идемпотентное
 * (пере)создание топика с конфигами (min.insync.replicas,
 * unclean.leader.election.enable), describe партиции 0 (leader/ISR/replicas),
 * alter конфига существующего топика.
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

    /** node.id брокера → имя compose-контейнера (см. ../../compose/compose.yml). Информационно для логов. */
    public static String containerFor(int nodeId) {
        return switch (nodeId) {
            case 1 -> "kafka-cookbook-1";
            case 2 -> "kafka-cookbook-2";
            case 3 -> "kafka-cookbook-3";
            default -> "unknown";
        };
    }

    public record PartitionState(String topic, int partition, int leader, List<Integer> replicas, List<Integer> isr) {
    }

    public static PartitionState describePartition(String name) {
        try (AdminClient admin = AdminClient.create(baseProps())) {
            Map<String, TopicDescription> desc = admin.describeTopics(List.of(name)).allTopicNames().get();
            TopicDescription td = desc.get(name);
            if (td == null) {
                throw new IllegalStateException("топик " + name + " не найден");
            }
            TopicPartitionInfo p = td.partitions().get(0);
            Node leader = p.leader();
            return new PartitionState(name, p.partition(),
                    leader == null ? -1 : leader.id(),
                    p.replicas().stream().map(Node::id).toList(),
                    p.isr().stream().map(Node::id).toList());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException(e);
        } catch (ExecutionException e) {
            throw new RuntimeException("describeTopics " + name, e);
        }
    }

    /** Как describePartition, но не бросает — возвращает null при ошибке (для retry-ожидания лидера). */
    public static PartitionState describePartitionSoft(String name) {
        try {
            return describePartition(name);
        } catch (RuntimeException e) {
            return null;
        }
    }

    public static void printPartitionState(String label, PartitionState p) {
        System.out.printf("[describe] %s: topic=%s partition=%d leader=%d(%s) replicas=%s isr=%s%n",
                label, p.topic(), p.partition(), p.leader(), containerFor(p.leader()), p.replicas(), p.isr());
    }

    public static PartitionState waitForLeader(String name, long timeoutMs) {
        long deadline = System.currentTimeMillis() + timeoutMs;
        PartitionState last = null;
        while (System.currentTimeMillis() < deadline) {
            last = describePartitionSoft(name);
            if (last != null && last.leader() >= 0) {
                return last;
            }
            sleep(500);
        }
        throw new IllegalStateException("waitForLeader: топик " + name + " не получил лидера за " + timeoutMs + "мс (последнее: " + last + ")");
    }

    public static void alterTopicConfig(String name, String key, String value) {
        try (AdminClient admin = AdminClient.create(baseProps())) {
            ConfigResource cr = new ConfigResource(ConfigResource.Type.TOPIC, name);
            AlterConfigOp op = new AlterConfigOp(new ConfigEntry(key, value), AlterConfigOp.OpType.SET);
            admin.incrementalAlterConfigs(Map.of(cr, List.of(op))).all().get();
            System.out.printf("[admin] топик %s: %s=%s%n", name, key, value);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException(e);
        } catch (ExecutionException e) {
            throw new RuntimeException("incrementalAlterConfigs " + name, e);
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
