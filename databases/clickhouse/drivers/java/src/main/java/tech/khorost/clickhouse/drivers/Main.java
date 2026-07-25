package tech.khorost.clickhouse.drivers;

import com.clickhouse.client.api.Client;
import com.clickhouse.client.api.insert.InsertResponse;
import com.clickhouse.client.api.insert.InsertSettings;
import com.clickhouse.client.api.query.GenericRecord;
import com.clickhouse.data.ClickHouseFormat;

import java.io.BufferedReader;
import java.io.ByteArrayInputStream;
import java.io.FileReader;
import java.io.IOException;
import java.io.InputStream;
import java.math.BigDecimal;
import java.nio.charset.StandardCharsets;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.sql.Timestamp;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.zip.CRC32;

/**
 * Стенд #3 серии "ClickHouse: глубокое погружение" — бенчмарк драйверов,
 * Java-половина (2 из 6 драйверов, см. ../go за ch-go/clickhouse-go
 * native/database-sql/raw HTTP): одинаковый сценарий (батч-INSERT M строк
 * общего датасета + один аналитический SELECT) через:
 *
 * <ul>
 *   <li>clickhouse-jdbc: {@link PreparedStatement#addBatch()}/{@code executeBatch()},
 *       чанками по batchSize строк (память не держит все M строк сразу)
 *   <li>client-v2 ("native"): {@link Client#insert} CSV-потоком, чанками по
 *       batchSize строк
 * </ul>
 *
 * Обе таблицы читаются потоково (BufferedReader) — не грузят весь CSV (до
 * 1М+ строк) в память разом, в отличие от ../../java/mergetree (100k строк,
 * там это было приемлемо).
 *
 * <p>Аналитический SELECT — тот же SQL, что в ../go/aggregate.go
 * (aggregateSQLTemplate), с той же чексуммой CRC32 над канонической строкой
 * "country|c|cents\n" по группам в порядке ORDER BY country — Go
 * hash/crc32.ChecksumIEEE и java.util.zip.CRC32 реализуют один и тот же
 * полином (CRC-32/ISO-HDLC), поэтому чексуммы Go- и Java-таблиц сравнимы
 * побайтово. Авторитетная межъязыковая сверка всех 6 таблиц — отдельная фаза
 * ../go -phase=verify (main.go, runVerify), выполняемая ПОСЛЕ этого стенда
 * (см. ../../ops/drivers-bench.sh).
 *
 * <p>Запуск (из контейнера maven:3.9-eclipse-temurin-25 на сети
 * clickhouse-cookbook-net, см. ../../ops/drivers-bench.sh):
 *
 * <pre>
 * mvn -q -f ../../java/pom.xml -pl ../drivers/java -am package -DskipTests &amp;&amp;
 * java -cp target/drivers.jar tech.khorost.clickhouse.drivers.Main \
 *   /data/events-drivers.csv jdbc:ch://clickhouse:8123/demo http://clickhouse:8123 1000000 100000
 * </pre>
 */
public final class Main {

    private static final String DDL = """
            CREATE TABLE demo.%s
            (
                event_time  DateTime,
                user_id     UInt64,
                event_type  LowCardinality(String),
                url         String,
                duration_ms UInt32,
                country     LowCardinality(String),
                revenue     Decimal(10,2)
            )
            ENGINE = MergeTree
            ORDER BY (event_time, user_id)
            PARTITION BY toYYYYMM(event_time)
            SETTINGS index_granularity = 8192
            """;

    // Тот же SQL, что ../go/aggregate.go (aggregateSQLTemplate) — явные
    // касты фиксируют типы результата (String/UInt64/Int64) одинаково для
    // всех 6 драйверов стенда, см. комментарий там.
    private static final String AGGREGATE_SQL = """
            SELECT country, count() AS c, toInt64(sum(revenue) * 100) AS cents
            FROM demo.%s
            GROUP BY country
            ORDER BY country
            """;

    private Main() {
    }

    public static void main(String[] args) throws Exception {
        String csvPath = args.length > 0 ? args[0] : "/data/events-drivers.csv";
        String jdbcUrl = args.length > 1 ? args[1] : "jdbc:ch://clickhouse:8123/demo";
        String httpUrl = args.length > 2 ? args[2] : "http://clickhouse:8123";
        long expectRows = args.length > 3 ? Long.parseLong(args[3]) : 1_000_000L;
        int batchSize = args.length > 4 ? Integer.parseInt(args[4]) : 100_000;

        System.out.printf(Locale.ROOT, "=== drivers (Java): M=%d, batchSize=%d, csv=%s ===%n", expectRows, batchSize, csvPath);

        AggResult jdbcRef;
        AggResult clientRef;

        try (Connection conn = DriverManager.getConnection(jdbcUrl, "default", "")) {

            System.out.println("\n=== JDBC BATCH (PreparedStatement.addBatch/executeBatch, чанками) ===");
            final String jdbcTable = "drivers_java_jdbc";
            createTable(conn, jdbcTable);
            long jdbcStart = System.nanoTime();
            long jdbcRows = insertJdbcBatch(conn, jdbcTable, csvPath, batchSize);
            double jdbcElapsedSec = (System.nanoTime() - jdbcStart) / 1e9;
            long jdbcPartsRows = countRows(conn, jdbcTable);
            System.out.printf(Locale.ROOT, "[jdbc] %s: %d rows in %.3fs (%.0f rows/s)%n",
                    jdbcTable, jdbcRows, jdbcElapsedSec, jdbcRows / jdbcElapsedSec);
            assertFailFast(jdbcRows == expectRows, "jdbc inserted rows (%d) == M (%d)", jdbcRows, expectRows);
            assertFailFast(jdbcPartsRows == expectRows, "jdbc system.parts rows (%d) == M (%d)", jdbcPartsRows, expectRows);

            long jdbcSelStart = System.nanoTime();
            jdbcRef = runAggregate(conn, jdbcTable);
            double jdbcSelSec = (System.nanoTime() - jdbcSelStart) / 1e9;
            System.out.printf(Locale.ROOT, "[jdbc] select: %.3fs, groups=%d, totalRows=%d, totalCents=%d, checksum=%08x%n",
                    jdbcSelSec, jdbcRef.groups, jdbcRef.totalRows, jdbcRef.totalCents, jdbcRef.checksum);
            assertFailFast(jdbcRef.totalRows == expectRows, "jdbc aggregate totalRows (%d) == M (%d)", jdbcRef.totalRows, expectRows);

            System.out.println("\n=== client-v2 BATCH (Client.insert, CSV-поток, чанками) ===");
            final String clientTable = "drivers_java_client";
            createTable(conn, clientTable);

            double clientElapsedSec;
            long clientRows;
            try (Client client = new Client.Builder()
                    .addEndpoint(httpUrl)
                    .setUsername("default")
                    .setPassword("")
                    .setDefaultDatabase("demo")
                    .build()) {
                long clientStart = System.nanoTime();
                clientRows = insertClientV2Batch(client, clientTable, csvPath, batchSize);
                clientElapsedSec = (System.nanoTime() - clientStart) / 1e9;

                long clientPartsRows = countRows(conn, clientTable);
                System.out.printf(Locale.ROOT, "[client-v2] %s: %d rows in %.3fs (%.0f rows/s)%n",
                        clientTable, clientRows, clientElapsedSec, clientRows / clientElapsedSec);
                assertFailFast(clientRows == expectRows, "client-v2 inserted rows (%d) == M (%d)", clientRows, expectRows);
                assertFailFast(clientPartsRows == expectRows, "client-v2 system.parts rows (%d) == M (%d)", clientPartsRows, expectRows);

                long clientSelStart = System.nanoTime();
                clientRef = runAggregateClientV2(client, clientTable);
                double clientSelSec = (System.nanoTime() - clientSelStart) / 1e9;
                System.out.printf(Locale.ROOT, "[client-v2] select: %.3fs, groups=%d, totalRows=%d, totalCents=%d, checksum=%08x%n",
                        clientSelSec, clientRef.groups, clientRef.totalRows, clientRef.totalCents, clientRef.checksum);
                assertFailFast(clientRef.totalRows == expectRows, "client-v2 aggregate totalRows (%d) == M (%d)", clientRef.totalRows, expectRows);
            }

            System.out.printf(Locale.ROOT, "%n[summary] JDBC batch: %.0f rows/s; client-v2 batch: %.0f rows/s%n",
                    jdbcRows / jdbcElapsedSec, clientRows / clientElapsedSec);

            System.out.println("\n=== СВЕРКА JDBC vs client-v2 (в рамках Java-стенда; полная сверка всех 6 драйверов — ../go -phase=verify) ===");
            assertFailFast(clientRef.groups == jdbcRef.groups, "client-v2 groups (%d) == jdbc groups (%d)", clientRef.groups, jdbcRef.groups);
            assertFailFast(clientRef.totalCents == jdbcRef.totalCents, "client-v2 totalCents (%d) == jdbc totalCents (%d)", clientRef.totalCents, jdbcRef.totalCents);
            assertFailFast(clientRef.checksum == jdbcRef.checksum, "client-v2 checksum (%08x) == jdbc checksum (%08x) — SELECT дал одинаковый результат", clientRef.checksum, jdbcRef.checksum);
        }

        System.out.println("\n[drivers-java] все фазы завершены, все ассерты прошли");
    }

    // ------------------------------------------------------------------
    // JDBC batch insert (чанками, не грузит все M строк в память)
    // ------------------------------------------------------------------

    private static long insertJdbcBatch(Connection conn, String table, String csvPath, int batchSize) throws SQLException, IOException {
        String sql = "INSERT INTO demo." + table
                + " (event_time, user_id, event_type, url, duration_ms, country, revenue) VALUES (?, ?, ?, ?, ?, ?, ?)";
        long total = 0;
        try (BufferedReader br = new BufferedReader(new FileReader(csvPath), 1 << 20)) {
            String header = br.readLine();
            if (header == null) {
                throw new IOException("empty csv: " + csvPath);
            }
            // Живьём обнаружено: clickhouse-jdbc 0.9.0 PreparedStatement.clearBatch()
            // НЕ очищает внутренний буфер между executeBatch()-вызовами на одном и
            // том же Statement — второй executeBatch() на "очищенном" батче заново
            // отправляет уже отправленные строки (наблюдалось: 10 чанков по 100k
            // дали 5 500 000 строк в system.parts вместо 1 000 000 — ровно
            // 100k*(1+2+...+10), т.е. каждый следующий executeBatch() пересылал ВСЕ
            // накопленные с начала строки). Обход: НОВЫЙ PreparedStatement на
            // каждый чанк (тот же приём, что database/sql-драйвер Go — свежий
            // Prepare на транзакцию, см. ../go/dbsql.go) — устраняет проблему
            // полностью, ассерт system.parts rows == M ниже подтверждает.
            int inBatch = 0;
            PreparedStatement ps = conn.prepareStatement(sql);
            String line;
            while ((line = br.readLine()) != null) {
                CsvRow r = CsvRow.parse(line);
                ps.setTimestamp(1, Timestamp.valueOf(r.eventTime()));
                ps.setLong(2, r.userId());
                ps.setString(3, r.eventType());
                ps.setString(4, r.url());
                ps.setInt(5, r.durationMs());
                ps.setString(6, r.country());
                ps.setBigDecimal(7, new BigDecimal(r.revenue()));
                ps.addBatch();
                inBatch++;
                total++;
                if (inBatch >= batchSize) {
                    ps.executeBatch();
                    ps.close();
                    ps = conn.prepareStatement(sql);
                    inBatch = 0;
                }
            }
            if (inBatch > 0) {
                ps.executeBatch();
            }
            ps.close();
        }
        return total;
    }

    // ------------------------------------------------------------------
    // client-v2 batch insert (CSV-поток чанками — тот же файл, чанки по
    // batchSize строк текста CSV, каждый чанк = отдельный Client.insert)
    // ------------------------------------------------------------------

    private static long insertClientV2Batch(Client client, String table, String csvPath, int batchSize) throws IOException, java.util.concurrent.ExecutionException, InterruptedException {
        long total = 0;
        try (BufferedReader br = new BufferedReader(new FileReader(csvPath), 1 << 20)) {
            String header = br.readLine();
            if (header == null) {
                throw new IOException("empty csv: " + csvPath);
            }
            List<String> chunk = new ArrayList<>(batchSize);
            String line;
            while ((line = br.readLine()) != null) {
                chunk.add(line);
                if (chunk.size() >= batchSize) {
                    total += sendClientV2Chunk(client, table, header, chunk);
                    chunk.clear();
                }
            }
            if (!chunk.isEmpty()) {
                total += sendClientV2Chunk(client, table, header, chunk);
            }
        }
        return total;
    }

    private static long sendClientV2Chunk(Client client, String table, String header, List<String> lines) throws java.util.concurrent.ExecutionException, InterruptedException, IOException {
        StringBuilder sb = new StringBuilder(header.length() + lines.size() * 64);
        sb.append(header).append('\n');
        for (String l : lines) {
            sb.append(l).append('\n');
        }
        byte[] csvBytes = sb.toString().getBytes(StandardCharsets.UTF_8);
        InsertSettings settings = new InsertSettings();
        try (InputStream in = new ByteArrayInputStream(csvBytes);
             InsertResponse resp = client.insert(table, in, ClickHouseFormat.CSVWithNames, settings).get()) {
            return resp.getWrittenRows();
        }
    }

    // ------------------------------------------------------------------
    // Аналитический SELECT + чексумма (JDBC / client-v2)
    // ------------------------------------------------------------------

    private record AggResult(int groups, long totalRows, long totalCents, long checksum) {
    }

    private static AggResult runAggregate(Connection conn, String table) throws SQLException {
        CRC32 crc = new CRC32();
        int groups = 0;
        long totalRows = 0;
        long totalCents = 0;
        try (Statement st = conn.createStatement();
             ResultSet rs = st.executeQuery(AGGREGATE_SQL.formatted(table))) {
            while (rs.next()) {
                String country = rs.getString(1);
                long c = rs.getLong(2);
                long cents = rs.getLong(3);
                groups++;
                totalRows += c;
                totalCents += cents;
                updateChecksum(crc, country, c, cents);
            }
        }
        return new AggResult(groups, totalRows, totalCents, crc.getValue());
    }

    private static AggResult runAggregateClientV2(Client client, String table) throws Exception {
        List<GenericRecord> records = client.queryAll(AGGREGATE_SQL.formatted(table));
        CRC32 crc = new CRC32();
        int groups = 0;
        long totalRows = 0;
        long totalCents = 0;
        for (GenericRecord rec : records) {
            String country = rec.getString("country");
            long c = rec.getLong("c");
            long cents = rec.getLong("cents");
            groups++;
            totalRows += c;
            totalCents += cents;
            updateChecksum(crc, country, c, cents);
        }
        return new AggResult(groups, totalRows, totalCents, crc.getValue());
    }

    /**
     * Обновляет CRC32 канонической строкой "country|c|cents\n" — ТОТ ЖЕ
     * формат, что ../go/aggregate.go computeAgg. java.util.zip.CRC32 и Go
     * hash/crc32.ChecksumIEEE — один полином (CRC-32/ISO-HDLC), поэтому
     * итоговые чексуммы сравнимы между Go- и Java-таблицами (см.
     * ../go/main.go runVerify).
     */
    private static void updateChecksum(CRC32 crc, String country, long c, long cents) {
        String line = country + "|" + c + "|" + cents + "\n";
        crc.update(line.getBytes(StandardCharsets.UTF_8));
    }

    // ------------------------------------------------------------------
    // Утилиты
    // ------------------------------------------------------------------

    private static void createTable(Connection conn, String table) throws SQLException {
        try (Statement st = conn.createStatement()) {
            st.execute("DROP TABLE IF EXISTS demo." + table);
            st.execute(DDL.formatted(table));
        }
    }

    private static long countRows(Connection conn, String table) throws SQLException {
        try (Statement st = conn.createStatement();
             ResultSet rs = st.executeQuery(
                     "SELECT coalesce(sum(rows), 0) FROM system.parts WHERE database = 'demo' AND table = '" + table + "' AND active")) {
            rs.next();
            return rs.getLong(1);
        }
    }

    private static void assertFailFast(boolean cond, String format, Object... args) {
        String msg = String.format(Locale.ROOT, format, args);
        if (!cond) {
            System.out.println("[ASSERT FAILED] " + msg);
            throw new IllegalStateException("assertion failed: " + msg);
        }
        System.out.println("[assert OK] " + msg);
    }
}
