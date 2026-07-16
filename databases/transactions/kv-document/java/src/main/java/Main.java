// Java-примеры к статье #3 «KV и документные: транзакций почти нет».
// https://khorost.tech/databases/transactions-kv-document-redis-mongo-scylla/
//
//   mvn -q compile exec:java -Dexec.args=redis
//   mvn -q compile exec:java -Dexec.args=mongo
//   mvn -q compile exec:java -Dexec.args=scylla

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.cql.PreparedStatement;
import com.datastax.oss.driver.api.core.cql.ResultSet;
import com.mongodb.client.ClientSession;
import com.mongodb.client.MongoClient;
import com.mongodb.client.MongoClients;
import com.mongodb.client.MongoCollection;
import org.bson.Document;
import redis.clients.jedis.Jedis;
import redis.clients.jedis.Transaction;

import java.net.InetSocketAddress;
import java.util.List;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicInteger;

import static com.mongodb.client.model.Filters.eq;
import static com.mongodb.client.model.Updates.inc;

public class Main {
    static String env(String k, String def) {
        String v = System.getenv(k);
        return v == null || v.isEmpty() ? def : v;
    }

    public static void main(String[] args) throws Exception {
        String cmd = args.length > 0 ? args[0] : "";
        switch (cmd) {
            case "redis" -> redis();
            case "mongo" -> mongo();
            case "scylla" -> scylla();
            default -> System.out.println("usage: exec.args=<redis|mongo|scylla>");
        }
    }

    // Redis: MULTI/EXEC исполняет очередь команд изолированно — не впуская чужие команды, — но
    // rollback'а у него нет и read-modify-write он не закрывает (чтение сделано до транзакции).
    // Это закрывает WATCH: оптимистичный CAS — exec() возвращает null, если ключ изменился после
    // WATCH; повторяем. Альтернатива для серверной логики — Lua/FUNCTION (для голого инкремента
    // достаточно INCR).
    static void redis() throws Exception {
        String[] hp = env("REDIS_ADDR", "localhost:6390").split(":");
        String host = hp[0];
        int port = Integer.parseInt(hp[1]);
        try (Jedis seed = new Jedis(host, port)) {
            seed.set("counter", "0");
        }
        int workers = 16, iters = 100;
        AtomicInteger retries = new AtomicInteger();
        ExecutorService ex = Executors.newFixedThreadPool(workers);
        CountDownLatch latch = new CountDownLatch(workers);
        for (int w = 0; w < workers; w++) {
            ex.submit(() -> {
                try (Jedis j = new Jedis(host, port)) {
                    for (int i = 0; i < iters; i++) {
                        while (true) {
                            j.watch("counter");
                            int v = Integer.parseInt(j.get("counter"));
                            Transaction t = j.multi();
                            t.set("counter", String.valueOf(v + 1));
                            if (t.exec() == null) { // ключ изменился между WATCH и EXEC
                                retries.incrementAndGet();
                                continue;
                            }
                            break;
                        }
                    }
                } finally {
                    latch.countDown();
                }
            });
        }
        latch.await();
        ex.shutdown();
        try (Jedis j = new Jedis(host, port)) {
            System.out.printf("redis (Jedis WATCH-CAS): counter=%s (ожидалось %d, retries=%d)%n",
                    j.get("counter"), workers * iters, retries.get());
        }
    }

    // MongoDB: multi-document транзакция. Атомарность одного документа есть всегда; инвариант
    // между двумя документами требует явной транзакции (withTransaction ретраит транзитные конфликты).
    static void mongo() throws Exception {
        String uri = env("MONGO_URI", "mongodb://localhost:27027/?directConnection=true");
        try (MongoClient client = MongoClients.create(uri)) {
            MongoCollection<Document> coll = client.getDatabase("demo").getCollection("accounts");
            coll.deleteMany(new Document());
            coll.insertMany(List.of(
                    new Document("_id", "A").append("balance", 1000),
                    new Document("_id", "B").append("balance", 0)));
            int workers = 16, iters = 50;
            ExecutorService ex = Executors.newFixedThreadPool(workers);
            CountDownLatch latch = new CountDownLatch(workers);
            for (int w = 0; w < workers; w++) {
                ex.submit(() -> {
                    try (ClientSession s = client.startSession()) {
                        for (int i = 0; i < iters; i++) {
                            s.withTransaction(() -> {
                                coll.updateOne(s, eq("_id", "A"), inc("balance", -1));
                                coll.updateOne(s, eq("_id", "B"), inc("balance", 1));
                                return null;
                            });
                        }
                    } finally {
                        latch.countDown();
                    }
                });
            }
            latch.await();
            ex.shutdown();
            long a = coll.find(eq("_id", "A")).first().getInteger("balance");
            long b = coll.find(eq("_id", "B")).first().getInteger("balance");
            System.out.printf("mongo (multi-doc tx): A=%d B=%d sum=%d (инвариант sum=1000 %s)%n",
                    a, b, a + b, (a + b) == 1000 ? "сохранён" : "НАРУШЕН");
        }
    }

    // ScyllaDB: LWT (IF NOT EXISTS) на Paxos — линеаризуемо, применяется ровно один из воркеров.
    static void scylla() throws Exception {
        String[] hp = env("SCYLLA_HOST", "localhost:9052").split(":");
        try (CqlSession s = CqlSession.builder()
                .addContactPoint(new InetSocketAddress(hp[0], Integer.parseInt(hp[1])))
                .withLocalDatacenter("datacenter1")
                .build()) {
            s.execute("CREATE KEYSPACE IF NOT EXISTS demo WITH replication = {'class':'SimpleStrategy','replication_factor':1}");
            s.execute("CREATE TABLE IF NOT EXISTS demo.seats (seat_id text PRIMARY KEY, booked_by text)");
            s.execute("DELETE FROM demo.seats WHERE seat_id = 'A1'");
            int workers = 16;
            AtomicInteger won = new AtomicInteger();
            PreparedStatement ps = s.prepare("INSERT INTO demo.seats (seat_id, booked_by) VALUES ('A1', ?) IF NOT EXISTS");
            ExecutorService ex = Executors.newFixedThreadPool(workers);
            CountDownLatch latch = new CountDownLatch(workers);
            for (int w = 0; w < workers; w++) {
                final int id = w;
                ex.submit(() -> {
                    try {
                        ResultSet rs = s.execute(ps.bind("worker-" + id));
                        if (rs.wasApplied()) {
                            won.incrementAndGet();
                        }
                    } finally {
                        latch.countDown();
                    }
                });
            }
            latch.await();
            ex.shutdown();
            System.out.printf("scylla (LWT IF NOT EXISTS): применилось %d из %d — эксклюзивно%n",
                    won.get(), workers);
        }
    }
}
