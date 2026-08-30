import io.debezium.engine.ChangeEvent;
import io.debezium.engine.DebeziumEngine;
import io.debezium.engine.format.Json;

import java.io.IOException;
import java.util.Properties;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;

/**
 * Минимальный Debezium Embedded Engine (без Kafka).
 * Читает PG logical replication (wal_level=logical, plugin.name=pgoutput)
 * и печатает декодированные change-события (JSON) в stdout.
 *
 * Debezium 3.6.0.Final, API: DebeziumEngine.create(Json.class) + .notifying(Consumer<ChangeEvent<String,String>>).
 * Этот паттерн (Json.class + record.value()) не менялся между Debezium 2.x и 3.x —
 * адаптаций API относительно канонического образца не потребовалось.
 */
public class Main {
    public static void main(String[] args) throws Exception {
        Properties props = new Properties();
        props.setProperty("name", "wal-debezium-embedded");
        props.setProperty("connector.class", "io.debezium.connector.postgresql.PostgresConnector");

        // offset/schema-history — локальные файлы (без Kafka)
        props.setProperty("offset.storage", "org.apache.kafka.connect.storage.FileOffsetBackingStore");
        props.setProperty("offset.storage.file.filename", "/tmp/dbz-offsets.dat");
        props.setProperty("offset.flush.interval.ms", "1000");
        props.setProperty("schema.history.internal", "io.debezium.storage.file.history.FileSchemaHistory");
        props.setProperty("schema.history.internal.file.filename", "/tmp/dbz-schema-history.dat");

        // подключение к PG-стенду
        props.setProperty("database.hostname", System.getProperty("database.hostname", "postgres"));
        props.setProperty("database.port", System.getProperty("database.port", "5432"));
        props.setProperty("database.user", "postgres");
        props.setProperty("database.password", "waldemo");
        props.setProperty("database.dbname", "waldemo");
        props.setProperty("topic.prefix", "wal");

        // logical decoding через pgoutput; отдельный слот от стенда ../logical/ (wal_slot)
        props.setProperty("plugin.name", "pgoutput");
        props.setProperty("slot.name", "debezium_slot");
        props.setProperty("publication.autocreate.mode", "filtered");
        props.setProperty("table.include.list", "public.orders");

        DebeziumEngine<ChangeEvent<String, String>> engine = DebeziumEngine.create(Json.class)
                .using(props)
                .notifying(record -> {
                    System.out.println("CHANGE-EVENT: " + record.value());
                })
                .build();

        ExecutorService executor = Executors.newSingleThreadExecutor();
        CountDownLatch latch = new CountDownLatch(1);

        // graceful shutdown: SIGTERM (в т.ч. timeout-kill контейнера) должен
        // корректно закрыть движок — иначе PG-слот репликации останется висеть
        Runtime.getRuntime().addShutdownHook(new Thread(() -> {
            try {
                engine.close();
                System.out.println("Debezium embedded engine stopped gracefully.");
            } catch (IOException e) {
                e.printStackTrace();
            } finally {
                latch.countDown();
            }
        }));

        executor.execute(engine);
        System.out.println("Debezium embedded engine started, streaming changes from orders...");

        try {
            // держим процесс живым, пока shutdown hook не отработает close() и не откроет защёлку
            latch.await();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        } finally {
            executor.shutdown();
            try {
                if (!executor.awaitTermination(10, TimeUnit.SECONDS)) {
                    executor.shutdownNow();
                }
            } catch (InterruptedException e) {
                executor.shutdownNow();
                Thread.currentThread().interrupt();
            }
        }
    }
}
