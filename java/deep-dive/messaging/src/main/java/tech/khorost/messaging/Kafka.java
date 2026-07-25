package tech.khorost.messaging;

import org.apache.kafka.clients.admin.AdminClient;
import org.apache.kafka.clients.admin.AdminClientConfig;
import org.apache.kafka.clients.admin.NewTopic;
import org.apache.kafka.common.errors.TopicExistsException;

import java.util.List;
import java.util.Properties;
import java.util.concurrent.ExecutionException;

/**
 * Общая точка конфигурации Kafka-стенда. BOOTSTRAP — ВНУТРЕННИЙ листенер брокера
 * (сервис kafka на compose-сети, PLAINTEXT://kafka:9092), не host-порт 9096 —
 * хостовый доступ к localhost:9096 может блокироваться файрволом, см. data-access.
 */
public final class Kafka {
    public static final String BOOTSTRAP =
            System.getenv().getOrDefault("KAFKA_BOOTSTRAP", "kafka:9092");

    private Kafka() {
    }

    public static Properties baseProps() {
        Properties p = new Properties();
        p.put(AdminClientConfig.BOOTSTRAP_SERVERS_CONFIG, BOOTSTRAP);
        return p;
    }

    /** Идемпотентно создаёт топик (игнорирует TopicExistsException — стенд перезапускаем). */
    public static void ensureTopic(String name, int partitions, short replicationFactor) {
        try (AdminClient admin = AdminClient.create(baseProps())) {
            NewTopic topic = new NewTopic(name, partitions, replicationFactor);
            admin.createTopics(List.of(topic)).all().get();
            System.out.printf("  [admin] топик %s создан (partitions=%d)%n", name, partitions);
        } catch (ExecutionException e) {
            if (e.getCause() instanceof TopicExistsException) {
                System.out.printf("  [admin] топик %s уже существует — пересоздаю (delete+create)%n", name);
                try (AdminClient admin = AdminClient.create(baseProps())) {
                    try {
                        admin.deleteTopics(List.of(name)).all().get();
                    } catch (Exception ignore) {
                        // топика могло и не быть — не критично
                    }
                    admin.createTopics(List.of(new NewTopic(name, partitions, replicationFactor))).all().get();
                } catch (Exception e2) {
                    throw new RuntimeException("Не удалось пересоздать топик " + name, e2);
                }
            } else {
                throw new RuntimeException("Не удалось создать топик " + name, e);
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException(e);
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
