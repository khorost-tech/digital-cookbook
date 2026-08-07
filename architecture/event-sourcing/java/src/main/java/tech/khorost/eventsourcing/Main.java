package tech.khorost.eventsourcing;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * Стенд «Event Sourcing на практике» (Java, чистый JDBC) — урезанный сценарий к статье
 * https://khorost.tech/architecture/event-sourcing-in-practice/ и серии «event-sourcing-deep-dive».
 * <p>
 * Домен: учёт кислорода в резервуарах лунной базы. Демонстрирует:
 * <ol>
 *   <li>append с ожидаемой версией и оптимистичную блокировку через PRIMARY KEY(stream_id, version);</li>
 *   <li>реальный конфликт версий: два потока пишут version=N — один падает на нарушении PK (SQLSTATE 23505);</li>
 *   <li>async-проектор баланса: читает по global_pos, идемпотентный UPSERT, чекпоинт в той же транзакции;</li>
 *   <li>ассерт «свёртка событий == проекция».</li>
 * </ol>
 * Полный сценарий (снапшоты, blue-green rebuild) — в Go-стенде event-sourcing/go.
 * <p>
 * Запуск: docker compose -f event-sourcing/compose/compose.yml up -d; затем в event-sourcing/java:
 * mvn -q compile exec:java. Переопределить строку подключения — переменная окружения DATABASE_URL
 * (JDBC-URL), по умолчанию jdbc:postgresql://localhost:5452/esdemo.
 */
public final class Main {

    static final String EVT_REGISTERED = "TankRegistered";
    static final String EVT_ADDED = "OxygenAdded";
    static final String EVT_CONSUMED = "OxygenConsumed";

    static final String SCHEMA = """
        DROP TABLE IF EXISTS events, balances, projection_checkpoint CASCADE;
        CREATE TABLE events (
            stream_id  uuid        NOT NULL,
            version    bigint      NOT NULL,
            event_type text        NOT NULL,
            schema_ver int         NOT NULL DEFAULT 1,
            data       jsonb       NOT NULL,
            metadata   jsonb       NOT NULL DEFAULT '{}',
            created_at timestamptz NOT NULL DEFAULT now(),
            global_pos bigserial   NOT NULL,
            PRIMARY KEY (stream_id, version)
        );
        CREATE INDEX events_global_pos_idx ON events (global_pos);
        CREATE TABLE balances (
            stream_id uuid   PRIMARY KEY,
            level     double precision NOT NULL,
            capacity  double precision NOT NULL,
            version   bigint NOT NULL
        );
        CREATE TABLE projection_checkpoint (
            name     text   PRIMARY KEY,
            last_pos bigint NOT NULL
        );
        """;

    public static void main(String[] args) throws Exception {
        String url = System.getenv().getOrDefault("DATABASE_URL", "jdbc:postgresql://localhost:5452/esdemo");
        String user = System.getenv().getOrDefault("PGUSER", "esdemo");
        String pass = System.getenv().getOrDefault("PGPASSWORD", "esdemo");

        try (Connection db = DriverManager.getConnection(url, user, pass)) {
            db.setAutoCommit(true);
            try (Statement st = db.createStatement()) {
                st.execute(SCHEMA);
            }

            // 1. Наполняем event store.
            System.out.println("== 1. Наполнение event store ==");
            List<UUID> streams = new ArrayList<>();
            java.util.Random rng = new java.util.Random(42);
            for (int i = 0; i < 5; i++) {
                UUID sid = UUID.randomUUID();
                streams.add(sid);
                long v = append(db, sid, 0, EVT_REGISTERED, "{\"capacity\":" + (1000 + i * 250) + "}");
                int ops = 10 + rng.nextInt(5);
                double level = 0;
                for (int j = 0; j < ops; j++) {
                    double amt = Math.round(rng.nextDouble() * 3000) / 10.0;
                    if (level < 200 || rng.nextInt(3) != 0) {
                        level += amt;
                        v = append(db, sid, v, EVT_ADDED, "{\"amount\":" + amt + "}");
                    } else {
                        amt = Math.round(amt / 2);
                        level -= amt;
                        v = append(db, sid, v, EVT_CONSUMED, "{\"amount\":" + amt + "}");
                    }
                }
                System.out.printf("  резервуар %s: версия %d%n", sid.toString().substring(0, 8), v);
            }

            // 2. Конфликт версий двумя потоками.
            System.out.println("== 2. Конфликт версий (два потока пишут version=N) ==");
            UUID racer = streams.get(0);
            long cur = currentVersion(db, url, user, pass, racer);
            int conflicts = raceAppend(url, user, pass, racer, cur);
            System.out.printf("  оба потока писали version=%d: успех=1, конфликт=%d%n", cur + 1, conflicts);
            assertTrue(conflicts == 1, "ожидался ровно 1 конфликт версий, получено " + conflicts);

            // 3. Async-проектор с чекпоинтом в той же транзакции + рестарт.
            System.out.println("== 3. Async-проектор баланса (падение и рестарт) ==");
            int first = runProjectorBatch(db, 7);
            System.out.printf("  экземпляр #1 обработал %d событий, чекпоинт=%d, затем «упал»%n", first, checkpoint(db));
            int more;
            int total = 0;
            while ((more = runProjectorBatch(db, 16)) > 0) {
                total += more;
            }
            System.out.printf("  экземпляр #2 догнал хвост: +%d событий, чекпоинт=%d%n", total, checkpoint(db));

            // 4. Ассерт: свёртка событий == проекция.
            assertProjectionMatchesFold(db, streams);
            System.out.println("  ASSERT ok: свёртка событий == проекция balances");

            System.out.println("\nВсе проверки пройдены (Java): append+optimistic, конфликт версий, async-проектор.");
        }
    }

    /** Append пачки из одного события с ожидаемой версией; конфликт по PK => VersionConflict. */
    static long append(Connection db, UUID sid, long expectedVersion, String type, String dataJson) throws SQLException {
        long v = expectedVersion + 1;
        String sql = "INSERT INTO events(stream_id, version, event_type, data, metadata) VALUES (?,?,?,?::jsonb,'{}'::jsonb)";
        try (PreparedStatement ps = db.prepareStatement(sql)) {
            ps.setObject(1, sid);
            ps.setLong(2, v);
            ps.setString(3, type);
            ps.setString(4, dataJson);
            ps.executeUpdate();
        } catch (SQLException e) {
            if ("23505".equals(e.getSQLState())) {
                throw new VersionConflict();
            }
            throw e;
        }
        return v;
    }

    /** Оптимистичная блокировка: два потока пишут version=cur+1, ровно один падает на PK. */
    static int raceAppend(String url, String user, String pass, UUID sid, long cur) throws InterruptedException {
        AtomicInteger conflicts = new AtomicInteger();
        Runnable task = () -> {
            try (Connection c = DriverManager.getConnection(url, user, pass)) {
                append(c, sid, cur, EVT_ADDED, "{\"amount\":10}");
            } catch (VersionConflict vc) {
                conflicts.incrementAndGet();
            } catch (SQLException e) {
                throw new RuntimeException(e);
            }
        };
        Thread t1 = new Thread(task), t2 = new Thread(task);
        t1.start();
        t2.start();
        t1.join();
        t2.join();
        return conflicts.get();
    }

    /**
     * Одна пачка проектора: читает события после чекпоинта по global_pos, идемпотентно
     * применяет к balances (UPSERT с guard version=?-1) и продвигает чекпоинт В ТОЙ ЖЕ
     * транзакции. Возвращает число обработанных событий (0 — хвост догнан).
     */
    static int runProjectorBatch(Connection db, int limit) throws SQLException {
        db.setAutoCommit(false);
        try {
            long last;
            try (PreparedStatement ps = db.prepareStatement(
                    "INSERT INTO projection_checkpoint(name,last_pos) VALUES('balances',0) " +
                    "ON CONFLICT (name) DO UPDATE SET last_pos=projection_checkpoint.last_pos RETURNING last_pos")) {
                ResultSet rs = ps.executeQuery();
                rs.next();
                last = rs.getLong(1);
            }
            int processed = 0;
            long newPos = last;
            String q = "SELECT stream_id, version, event_type, " +
                    "coalesce((data->>'amount')::float8,0), coalesce((data->>'capacity')::float8,0), global_pos " +
                    "FROM events WHERE global_pos>? ORDER BY global_pos LIMIT ?";
            List<Object[]> batch = new ArrayList<>();
            try (PreparedStatement ps = db.prepareStatement(q)) {
                ps.setLong(1, last);
                ps.setInt(2, limit);
                ResultSet rs = ps.executeQuery();
                while (rs.next()) {
                    batch.add(new Object[]{
                            (UUID) rs.getObject(1), rs.getLong(2), rs.getString(3),
                            rs.getDouble(4), rs.getDouble(5), rs.getLong(6)});
                }
            }
            for (Object[] e : batch) {
                applyToProjection(db, (UUID) e[0], (Long) e[1], (String) e[2], (Double) e[3], (Double) e[4]);
                newPos = (Long) e[5];
                processed++;
            }
            if (processed > 0) {
                try (PreparedStatement ps = db.prepareStatement(
                        "UPDATE projection_checkpoint SET last_pos=? WHERE name='balances'")) {
                    ps.setLong(1, newPos);
                    ps.executeUpdate();
                }
            }
            db.commit();
            return processed;
        } catch (SQLException e) {
            db.rollback();
            throw e;
        } finally {
            db.setAutoCommit(true);
        }
    }

    /** Идемпотентный UPSERT: guard `version=?-1` не даёт применить событие дважды. */
    static void applyToProjection(Connection db, UUID sid, long version, String type, double amount, double capacity)
            throws SQLException {
        if (EVT_REGISTERED.equals(type)) {
            try (PreparedStatement ps = db.prepareStatement(
                    "INSERT INTO balances(stream_id,level,capacity,version) VALUES (?,0,?,?) " +
                    "ON CONFLICT (stream_id) DO UPDATE SET capacity=?, version=? WHERE balances.version=?-1")) {
                ps.setObject(1, sid);
                ps.setDouble(2, capacity);
                ps.setLong(3, version);
                ps.setDouble(4, capacity);
                ps.setLong(5, version);
                ps.setLong(6, version);
                ps.executeUpdate();
            }
        } else {
            double delta = EVT_CONSUMED.equals(type) ? -amount : amount;
            try (PreparedStatement ps = db.prepareStatement(
                    "INSERT INTO balances(stream_id,level,capacity,version) VALUES (?,?,0,?) " +
                    "ON CONFLICT (stream_id) DO UPDATE SET level=balances.level+?, version=? WHERE balances.version=?-1")) {
                ps.setObject(1, sid);
                ps.setDouble(2, delta);
                ps.setLong(3, version);
                ps.setDouble(4, delta);
                ps.setLong(5, version);
                ps.setLong(6, version);
                ps.executeUpdate();
            }
        }
    }

    /** Свёртка событий каждого стрима и сверка с проекцией. */
    static void assertProjectionMatchesFold(Connection db, List<UUID> streams) throws SQLException {
        for (UUID sid : streams) {
            double level = 0;
            long ver = 0;
            String q = "SELECT event_type, coalesce((data->>'amount')::float8,0), version " +
                    "FROM events WHERE stream_id=? ORDER BY version";
            try (PreparedStatement ps = db.prepareStatement(q)) {
                ps.setObject(1, sid);
                ResultSet rs = ps.executeQuery();
                while (rs.next()) {
                    String t = rs.getString(1);
                    double amt = rs.getDouble(2);
                    if (EVT_ADDED.equals(t)) level += amt;
                    else if (EVT_CONSUMED.equals(t)) level -= amt;
                    ver = rs.getLong(3);
                }
            }
            double projLevel = 0;
            long projVer = -1;
            try (PreparedStatement ps = db.prepareStatement("SELECT level, version FROM balances WHERE stream_id=?")) {
                ps.setObject(1, sid);
                ResultSet rs = ps.executeQuery();
                if (rs.next()) {
                    projLevel = rs.getDouble(1);
                    projVer = rs.getLong(2);
                }
            }
            assertTrue(Math.abs(projLevel - level) < 1e-9 && projVer == ver,
                    "стрим " + sid.toString().substring(0, 8) + ": проекция(" + projLevel + "," + projVer
                            + ") != свёртка(" + level + "," + ver + ")");
        }
    }

    static long currentVersion(Connection db, String url, String user, String pass, UUID sid) throws SQLException {
        try (PreparedStatement ps = db.prepareStatement("SELECT coalesce(max(version),0) FROM events WHERE stream_id=?")) {
            ps.setObject(1, sid);
            ResultSet rs = ps.executeQuery();
            rs.next();
            return rs.getLong(1);
        }
    }

    static long checkpoint(Connection db) throws SQLException {
        try (Statement st = db.createStatement()) {
            ResultSet rs = st.executeQuery("SELECT coalesce(last_pos,0) FROM projection_checkpoint WHERE name='balances'");
            return rs.next() ? rs.getLong(1) : 0;
        }
    }

    static void assertTrue(boolean cond, String msg) {
        if (!cond) {
            System.err.println("ASSERT провален: " + msg);
            System.exit(1);
        }
    }

    /** Оптимистичная блокировка сработала: версия стрима уже занята (PK 23505). */
    static final class VersionConflict extends RuntimeException {
    }

    private Main() {
    }
}
