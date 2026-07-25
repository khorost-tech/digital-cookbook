package tech.khorost.clickhouse.mergetree;

import com.clickhouse.client.api.Client;
import com.clickhouse.client.api.insert.InsertResponse;
import com.clickhouse.client.api.insert.InsertSettings;
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

/**
 * Стенд #2 серии "ClickHouse: глубокое погружение" — MergeTree из Java:
 * вставки и запросы. Зеркало ../../../go/mergetree (та же DDL, тот же
 * общий датасет), два эквивалентных батч-пути (Step 3 брифа):
 *
 * <ul>
 *   <li>clickhouse-jdbc: {@link PreparedStatement#addBatch()}/{@code executeBatch()}
 *   <li>client-v2 ("native" в терминологии брифа — низкоуровневый клиент без
 *       JDBC-обёртки): {@link Client#insert} батч-CSV-потоком одним вызовом
 * </ul>
 *
 * <p>Плюс: EXPLAIN indexes=1 + read_rows из system.query_log для запроса с
 * фильтром по префиксу ORDER BY (event_time) — тот же гранульный
 * pruning-сценарий, что Step 1 брифа демонстрирует в Go-стенде.
 *
 * <p>Запуск (из контейнера maven:3.9-eclipse-temurin-25 на сети
 * clickhouse-cookbook-net, см. ../../../../ops/mergetree-demo.sh):
 *
 * <pre>
 * mvn -q -f ../pom.xml -pl mergetree -am package -DskipTests &amp;&amp;
 * java -cp target/mergetree.jar tech.khorost.clickhouse.mergetree.Main \
 *   /data/events-mergetree.csv jdbc:ch://clickhouse:8123/demo http://clickhouse:8123 100000
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

    private Main() {
    }

    public static void main(String[] args) throws Exception {
        String csvPath = args.length > 0 ? args[0] : "/data/events-mergetree.csv";
        String jdbcUrl = args.length > 1 ? args[1] : "jdbc:ch://clickhouse:8123/demo";
        String httpUrl = args.length > 2 ? args[2] : "http://clickhouse:8123";
        int rows = args.length > 3 ? Integer.parseInt(args[3]) : 100_000;

        System.out.println("=== ЧТЕНИЕ CSV ===");
        List<String> rawLines = readLines(csvPath, rows); // [0]=header, [1..rows]=данные
        List<CsvRow> parsed = new ArrayList<>(rows);
        for (int i = 1; i < rawLines.size(); i++) {
            parsed.add(CsvRow.parse(rawLines.get(i)));
        }
        System.out.printf("прочитано %d строк из %s%n", parsed.size(), csvPath);

        try (Connection conn = DriverManager.getConnection(jdbcUrl, "default", "")) {

            System.out.println("\n=== JDBC BATCH (PreparedStatement.addBatch/executeBatch) ===");
            final String jdbcTable = "mergetree_events_java_jdbc";
            createTable(conn, jdbcTable);
            long jdbcStart = System.nanoTime();
            insertJdbcBatch(conn, jdbcTable, parsed);
            double jdbcElapsedSec = (System.nanoTime() - jdbcStart) / 1e9;
            long jdbcRows = countRows(conn, jdbcTable);
            long jdbcParts = countParts(conn, jdbcTable);
            System.out.printf("[jdbc] %s: %d rows in %.3fs (%.0f rows/s), %d active parts%n",
                    jdbcTable, jdbcRows, jdbcElapsedSec, jdbcRows / jdbcElapsedSec, jdbcParts);
            assertFailFast(jdbcRows == rows, "jdbc loaded rows (%d) == expected (%d)", jdbcRows, rows);
            // <=6, не <=1: один executeBatch создаёт один part НА КАЖДУЮ
            // затронутую партицию toYYYYMM(event_time) — тот же эффект, что
            // в Go-стенде (../../../go/mergetree/main.go), не баг.
            assertFailFast(jdbcParts <= 6, "jdbc single executeBatch produces few parts — one per touched partition: %d <= 6", jdbcParts);

            System.out.println("\n=== client-v2 BATCH (Client.insert, CSV-поток, один вызов) ===");
            final String clientTable = "mergetree_events_java_client";
            createTable(conn, clientTable);
            String csvText = String.join("\n", rawLines) + "\n";
            byte[] csvBytes = csvText.getBytes(StandardCharsets.UTF_8);

            double clientElapsedSec;
            try (Client client = new Client.Builder()
                    .addEndpoint(httpUrl)
                    .setUsername("default")
                    .setPassword("")
                    .setDefaultDatabase("demo")
                    .build()) {
                long clientStart = System.nanoTime();
                InsertSettings settings = new InsertSettings();
                try (InputStream in = new ByteArrayInputStream(csvBytes);
                     InsertResponse resp = client.insert(clientTable, in, ClickHouseFormat.CSVWithNames, settings).get()) {
                    clientElapsedSec = (System.nanoTime() - clientStart) / 1e9;
                    System.out.printf("[client-v2] insert response: writtenRows=%d%n", resp.getWrittenRows());
                }
            }
            long clientRows = countRows(conn, clientTable);
            long clientParts = countParts(conn, clientTable);
            System.out.printf("[client-v2] %s: %d rows in %.3fs (%.0f rows/s), %d active parts%n",
                    clientTable, clientRows, clientElapsedSec, clientRows / clientElapsedSec, clientParts);
            assertFailFast(clientRows == rows, "client-v2 loaded rows (%d) == expected (%d)", clientRows, rows);
            assertFailFast(clientParts <= 6, "client-v2 single insert() produces few parts — one per touched partition: %d <= 6", clientParts);

            System.out.printf("%n[summary] JDBC batch: %.0f rows/s; client-v2 batch: %.0f rows/s (оба — один batch-вызов на %d строк, эквивалентно clickhouse-go/v2 PrepareBatch/Send из Go-стенда)%n",
                    jdbcRows / jdbcElapsedSec, clientRows / clientElapsedSec, rows);

            System.out.println("\n=== ГРАНУЛЫ: фильтр по префиксу ORDER BY (event_time), на jdbc-таблице ===");
            runGranuleCheck(conn, jdbcTable, rows);
        }

        System.out.println("\n[mergetree-java] все фазы завершены, все ассерты прошли");
    }

    private static void runGranuleCheck(Connection conn, String table, long totalRows) throws SQLException {
        String filterSql = "SELECT count(), sum(revenue) FROM demo." + table
                + " WHERE event_time >= '2026-06-08 00:00:00' AND event_time < '2026-06-15 00:00:00'";
        String logComment = "mergetree-java-granule-" + System.nanoTime();

        try (Statement st = conn.createStatement();
             ResultSet rs = st.executeQuery("EXPLAIN indexes = 1 " + filterSql)) {
            System.out.println("[granules] EXPLAIN indexes=1 (WHERE event_time в окне 7 суток):");
            while (rs.next()) {
                System.out.println(rs.getString(1));
            }
        }

        long filteredCount;
        try (Statement st = conn.createStatement();
             ResultSet rs = st.executeQuery(filterSql + " SETTINGS log_comment = '" + logComment + "'")) {
            rs.next();
            filteredCount = rs.getLong(1);
        }

        try (Statement st = conn.createStatement()) {
            st.execute("SYSTEM FLUSH LOGS");
        }

        long readRows;
        try (Statement st = conn.createStatement();
             ResultSet rs = st.executeQuery(
                     "SELECT read_rows FROM system.query_log WHERE log_comment = '" + logComment
                             + "' AND type = 'QueryFinish' ORDER BY event_time DESC LIMIT 1")) {
            if (!rs.next()) {
                throw new SQLException("query_log entry not found for log_comment=" + logComment);
            }
            readRows = rs.getLong(1);
        }

        System.out.printf("[granules] filtered query (7-day window): count()=%d, read_rows=%d (из %d total, %.2f%%)%n",
                filteredCount, readRows, totalRows, 100.0 * readRows / totalRows);

        assertFailFast(readRows > 0, "filtered query read_rows > 0 (query_log populated)");
        assertFailFast(readRows < totalRows / 5, "granule skip: filtered read_rows (%d) < total/5 (%d из %d)",
                readRows, totalRows / 5, totalRows);
    }

    private static void createTable(Connection conn, String table) throws SQLException {
        try (Statement st = conn.createStatement()) {
            st.execute("DROP TABLE IF EXISTS demo." + table);
            st.execute(DDL.formatted(table));
        }
    }

    private static void insertJdbcBatch(Connection conn, String table, List<CsvRow> rows) throws SQLException {
        String sql = "INSERT INTO demo." + table
                + " (event_time, user_id, event_type, url, duration_ms, country, revenue) VALUES (?, ?, ?, ?, ?, ?, ?)";
        try (PreparedStatement ps = conn.prepareStatement(sql)) {
            for (CsvRow r : rows) {
                ps.setTimestamp(1, Timestamp.valueOf(r.eventTime()));
                ps.setLong(2, r.userId());
                ps.setString(3, r.eventType());
                ps.setString(4, r.url());
                ps.setInt(5, r.durationMs());
                ps.setString(6, r.country());
                ps.setBigDecimal(7, new BigDecimal(r.revenue()));
                ps.addBatch();
            }
            ps.executeBatch();
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

    private static long countParts(Connection conn, String table) throws SQLException {
        try (Statement st = conn.createStatement();
             ResultSet rs = st.executeQuery(
                     "SELECT count() FROM system.parts WHERE database = 'demo' AND table = '" + table + "' AND active")) {
            rs.next();
            return rs.getLong(1);
        }
    }

    private static List<String> readLines(String path, int rows) throws IOException {
        List<String> out = new ArrayList<>(rows + 1);
        try (BufferedReader br = new BufferedReader(new FileReader(path), 1 << 20)) {
            String header = br.readLine();
            if (header == null) {
                throw new IOException("empty csv: " + path);
            }
            out.add(header);
            String line;
            while (out.size() - 1 < rows && (line = br.readLine()) != null) {
                out.add(line);
            }
        }
        if (out.size() - 1 < rows) {
            throw new IOException("csv has fewer than " + rows + " data rows (" + (out.size() - 1) + ")");
        }
        return out;
    }

    private static void assertFailFast(boolean cond, String format, Object... args) {
        String msg = String.format(format, args);
        if (!cond) {
            System.out.println("[ASSERT FAILED] " + msg);
            throw new IllegalStateException("assertion failed: " + msg);
        }
        System.out.println("[assert OK] " + msg);
    }
}
