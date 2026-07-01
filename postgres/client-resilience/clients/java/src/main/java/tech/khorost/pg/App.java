package tech.khorost.pg;

import com.zaxxer.hikari.HikariConfig;
import com.zaxxer.hikari.HikariDataSource;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalTime;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.List;
import java.util.Properties;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Демонстрационный клиент JDBC + HikariCP: пишет через pgbouncer (transaction pool mode),
 * читает напрямую с реплики, раз при старте показывает failover-aware выбор primary
 * через MULTIHOST_DSN (targetServerType=primary).
 * Клиент НИКОГДА не падает на ошибке: при обрыве/failover он логирует ошибку и продолжает,
 * чтобы было видно окно недоступности и последующий reconnect.
 */
public class App {

    private static final int OP_TIMEOUT_SECONDS = 1;
    private static final int MAX_RETRIES = 2;

    public static void main(String[] args) throws InterruptedException {
        try (HikariDataSource writer = buildWriterPool();
             HikariDataSource reader = buildReaderPool()) {

            // Один раз при старте: подключение через MULTIHOST_DSN с targetServerType=primary.
            // pgjdbc перебирает хосты из URL и выбирает тот, что сейчас отвечает как primary.
            // Это и есть failover-aware primary selection — при промоушене реплики следующее
            // подключение с тем же URL уедет на новый primary без изменений в коде клиента.
            logMultihostSelection();

            System.out.println("[java] цикл WRITE(bouncer)/READ(replica) раз в секунду, Ctrl+C для выхода");
            while (true) {
                try {
                    long id = writeRow(writer);
                    System.out.println("[java] WROTE id=" + id);
                } catch (SQLException e) {
                    System.out.println("[java] ERROR write: " + e.getMessage());
                }

                try {
                    List<String> rows = readRows(reader);
                    System.out.println("[java] READ " + rows);
                } catch (SQLException e) {
                    System.out.println("[java] ERROR read: " + e.getMessage());
                }

                Thread.sleep(1000);
            }
        }
    }

    /**
     * buildWriterPool — пул для записи через pgbouncer в TRANSACTION pool mode.
     *
     * КРИТИЧНО: pgbouncer в transaction pool mode отдаёт клиенту разное серверное соединение
     * на каждую транзакцию (а то и на каждый запрос вне явной транзакции). pgjdbc по умолчанию
     * начинает использовать server-side prepared statements после prepareThreshold (по умолчанию 5)
     * исполнений одного и того же PreparedStatement — драйвер один раз готовит запрос (Parse) на
     * конкретном физическом соединении к Postgres и рассчитывает исполнять (Bind/Execute) его там
     * же на следующих вызовах. Через transaction-mode бенчер это ломается: подготовленный statement
     * может "уехать" на другое физическое соединение (стенд не включает max_prepared_statements
     * в pgbouncer), и сервер ответит "prepared statement does not exist" — непредсказуемо и
     * трудноотлаживаемо.
     * Решение: prepareThreshold=0 полностью отключает server-side prepare — каждый запрос всегда
     * уходит как unnamed/simple statement, что безопасно для transaction-mode бенчера.
     */
    private static HikariDataSource buildWriterPool() {
        String dsn = mustGetenv("PGBOUNCER_DSN");
        PgUrl url = PgUrl.parse(dsn);

        HikariConfig config = new HikariConfig();
        // prepareThreshold=0 продублирован и в URL, и как свойство датасорса — оба пути
        // ведут к одному и тому же DriverManager.getConnection(url, props) в HikariCP,
        // но URL-параметр самодостаточен и понятен при простом чтении строки подключения.
        config.setJdbcUrl(url.jdbcUrl() + (url.jdbcUrl().contains("?") ? "&" : "?") + "prepareThreshold=0");
        config.setUsername(url.user());
        config.setPassword(url.password());
        config.addDataSourceProperty("prepareThreshold", "0");

        config.setMaximumPoolSize(10);
        config.setMaxLifetime(5 * 60 * 1000L);
        config.setKeepaliveTime(30 * 1000L);
        config.setConnectionTimeout(OP_TIMEOUT_SECONDS * 1000L);
        config.setPoolName("writer-pgbouncer");

        return new HikariDataSource(config);
    }

    /**
     * buildReaderPool — пул для чтения напрямую с реплики (без bouncer). Extended protocol
     * и server-side prepared statements тут безопасны — соединение с сервером закреплено
     * за пулом Hikari (без прокси transaction-mode бенчера), поэтому оставляем дефолты pgjdbc.
     */
    private static HikariDataSource buildReaderPool() {
        String dsn = mustGetenv("REPLICA_DSN");
        PgUrl url = PgUrl.parse(dsn);

        HikariConfig config = new HikariConfig();
        config.setJdbcUrl(url.jdbcUrl());
        config.setUsername(url.user());
        config.setPassword(url.password());

        config.setMaximumPoolSize(10);
        config.setMaxLifetime(5 * 60 * 1000L);
        config.setKeepaliveTime(30 * 1000L);
        config.setConnectionTimeout(OP_TIMEOUT_SECONDS * 1000L);
        config.setPoolName("reader-replica");

        return new HikariDataSource(config);
    }

    /**
     * logMultihostSelection открывает одно plain JDBC-соединение по MULTIHOST_DSN, логирует,
     * какой хост был выбран как primary (через inet_server_addr()/inet_server_port()),
     * и закрывает соединение. Не влияет на основной цикл.
     */
    private static void logMultihostSelection() {
        String dsn = mustGetenv("MULTIHOST_DSN");
        PgUrl url = PgUrl.parse(dsn);
        // MULTIHOST_DSN приходит в libpq-форме с ?target_session_attrs=read-write — это
        // native-libpq параметр, которого pgjdbc не знает (у него свой targetServerType).
        // Отбрасываем исходный query и ставим только pgjdbc-эквивалент, чтобы не полагаться
        // на то, как драйвер обработает незнакомый параметр.
        String jdbcUrl = url.jdbcUrlWithoutQuery() + "?targetServerType=primary";

        Properties props = new Properties();
        props.setProperty("user", url.user());
        props.setProperty("password", url.password());
        props.setProperty("connectTimeout", String.valueOf(OP_TIMEOUT_SECONDS));

        try (Connection conn = DriverManager.getConnection(jdbcUrl, props);
             PreparedStatement ps = conn.prepareStatement(
                     "SELECT inet_server_addr()::text, inet_server_port()");
             ResultSet rs = ps.executeQuery()) {
            if (rs.next()) {
                String addr = rs.getString(1);
                int port = rs.getInt(2);
                System.out.println("[java] MULTIHOST: targetServerType=primary -> выбран хост "
                        + addr + ":" + port);
            } else {
                System.out.println("[java] MULTIHOST: подключение успешно, но inet_server_addr() пуст");
            }
        } catch (SQLException e) {
            System.out.println("[java] ERROR multihost connect: " + e.getMessage());
        }
    }

    /** writeRow вставляет строку в users через writer pool (pgbouncer) с ретраями транзиентных ошибок. */
    private static long writeRow(HikariDataSource writer) throws SQLException {
        String name = "client-java@" + LocalTime.now().truncatedTo(ChronoUnit.MILLIS);

        return withRetry(() -> {
            try (Connection conn = writer.getConnection();
                 PreparedStatement ps = conn.prepareStatement(
                         "INSERT INTO users (name) VALUES (?) RETURNING id")) {
                ps.setString(1, name);
                try (ResultSet rs = ps.executeQuery()) {
                    rs.next();
                    return rs.getLong(1);
                }
            }
        });
    }

    /** readRows читает несколько последних строк из users через reader pool (реплика). */
    private static List<String> readRows(HikariDataSource reader) throws SQLException {
        return withRetry(() -> {
            try (Connection conn = reader.getConnection();
                 PreparedStatement ps = conn.prepareStatement(
                         "SELECT id, name FROM users ORDER BY id DESC LIMIT 3");
                 ResultSet rs = ps.executeQuery()) {
                List<String> rows = new ArrayList<>();
                while (rs.next()) {
                    rows.add(rs.getLong(1) + ":" + rs.getString(2));
                }
                return rows;
            }
        });
    }

    @FunctionalInterface
    private interface SqlOp<T> {
        T run() throws SQLException;
    }

    /**
     * withRetry повторяет op при транзиентных ошибках Postgres (serialization_failure,
     * deadlock_detected, admin shutdown) с небольшим backoff. Остальные ошибки не ретраятся —
     * они пробрасываются вызывающему коду как есть (и логируются, не уронив цикл).
     */
    private static <T> T withRetry(SqlOp<T> op) throws SQLException {
        SQLException last;
        int attempt = 0;
        while (true) {
            try {
                return op.run();
            } catch (SQLException e) {
                last = e;
                if (!isRetryable(e) || attempt >= MAX_RETRIES) {
                    throw last;
                }
                attempt++;
                try {
                    Thread.sleep(attempt * 50L);
                } catch (InterruptedException ie) {
                    Thread.currentThread().interrupt();
                    throw last;
                }
            }
        }
    }

    /** isRetryable распознаёт транзиентные коды ошибок Postgres по SQLSTATE. */
    private static boolean isRetryable(SQLException e) {
        String sqlState = e.getSQLState();
        if (sqlState == null) {
            return false;
        }
        return switch (sqlState) {
            case "40001", // serialization_failure
                 "40P01", // deadlock_detected
                 "57P01" -> true; // admin_shutdown
            default -> false;
        };
    }

    private static String mustGetenv(String key) {
        String v = System.getenv(key);
        if (v == null || v.isBlank()) {
            System.out.println("[java] FATAL: не задана переменная окружения " + key);
            System.exit(1);
        }
        return v;
    }

    /**
     * PgUrl разбирает libpq-style DSN вида
     * {@code postgres://user:pass@host1:port1,host2:port2/db?params}
     * в JDBC-форму {@code jdbc:postgresql://host1:port1,host2:port2/db?params}
     * плюс отдельные user/password (pgjdbc сам умеет мульти-хост в URL).
     */
    private record PgUrl(String jdbcUrl, String user, String password) {

        private static final Pattern PATTERN = Pattern.compile(
                "^postgres(?:ql)?://([^:@/]+):([^@/]*)@([^/]+)/([^?]+)(\\?.*)?$");

        static PgUrl parse(String dsn) {
            Matcher m = PATTERN.matcher(dsn);
            if (!m.matches()) {
                System.out.println("[java] FATAL: не удалось разобрать DSN: " + dsn);
                System.exit(1);
            }
            String user = m.group(1);
            String password = m.group(2);
            String hosts = m.group(3);
            String db = m.group(4);
            String query = m.group(5) == null ? "" : m.group(5);

            String jdbcUrl = "jdbc:postgresql://" + hosts + "/" + db + query;
            return new PgUrl(jdbcUrl, user, password);
        }

        /** Базовый JDBC URL (host(s)+db) без исходного query — чтобы прицепить свои pgjdbc-параметры. */
        String jdbcUrlWithoutQuery() {
            int idx = jdbcUrl.indexOf('?');
            return idx < 0 ? jdbcUrl : jdbcUrl.substring(0, idx);
        }
    }
}
