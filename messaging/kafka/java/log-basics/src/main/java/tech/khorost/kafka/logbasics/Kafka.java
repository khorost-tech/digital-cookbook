package tech.khorost.kafka.logbasics;

import org.apache.kafka.clients.admin.AdminClient;
import org.apache.kafka.clients.admin.AdminClientConfig;
import org.apache.kafka.clients.admin.NewTopic;

import java.util.List;
import java.util.Properties;
import java.util.concurrent.ExecutionException;

/**
 * Общая точка конфигурации стенда. BOOTSTRAP — ВНУТРЕННИЕ (compose-net) листенеры
 * трёх брокеров кластера из ../../compose/compose.yml, не host-порты 190xx — клиент
 * запускается как контейнер на сети kafka-cookbook-net, см. ../../../README.md.
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

    /**
     * Идемпотентно (пере)создаёт топик: если он уже существует от предыдущего
     * прогона — удаляет и создаёт заново, чтобы офсеты в выводе начинались с
     * чистого листа и прогон был воспроизводим.
     */
    public static void recreateTopic(String name, int partitions, short replicationFactor) {
        try (AdminClient admin = AdminClient.create(baseProps())) {
            try {
                admin.deleteTopics(List.of(name)).all().get();
                // контроллеру нужно время, чтобы топик пропал из метаданных
                waitUntilAbsent(admin, name);
            } catch (ExecutionException e) {
                // топика могло не быть на первом прогоне — не критично
            }
            NewTopic topic = new NewTopic(name, partitions, replicationFactor);
            admin.createTopics(List.of(topic)).all().get();
            System.out.printf("[admin] топик %s создан (partitions=%d, rf=%d)%n", name, partitions, replicationFactor);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException(e);
        } catch (ExecutionException e) {
            throw new RuntimeException("Не удалось создать топик " + name, e);
        }
    }

    private static void waitUntilAbsent(AdminClient admin, String name) {
        long deadline = System.currentTimeMillis() + 10_000;
        while (System.currentTimeMillis() < deadline) {
            try {
                var names = admin.listTopics().names().get();
                if (!names.contains(name)) {
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
