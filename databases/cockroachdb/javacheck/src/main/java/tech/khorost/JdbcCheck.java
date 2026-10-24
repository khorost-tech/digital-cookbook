package tech.khorost;

import java.sql.Connection;
import java.sql.DatabaseMetaData;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.concurrent.ThreadLocalRandom;

/**
 * Проверка PG-совместимости CockroachDB штатным драйвером PostgreSQL.
 *
 * <p>Показывает две вещи. Первая: драйвер менять не нужно, тот же
 * org.postgresql подключается и работает. Вторая, менее приятная:
 * retry-цикл по SQLSTATE 40001 приходится писать руками — драйвер про
 * распределённые ретраи ничего не знает, и в одноузловом PostgreSQL этот
 * код был не нужен.
 */
public final class JdbcCheck {

    /** Код ретраибельной ошибки. Един для contention и для uncertainty. */
    private static final String RETRY_SQLSTATE = "40001";

    private static final int MAX_ATTEMPTS = 10;

    public static void main(String[] args) throws Exception {
        String url = args.length > 0
                ? args[0]
                : "jdbc:postgresql://127.0.0.1:15450/bank?sslmode=disable";
        String user = args.length > 1 ? args[1] : "root";
        int iterations = args.length > 2 ? Integer.parseInt(args[2]) : 200;

        try (Connection conn = DriverManager.getConnection(url, user, null)) {
            conn.setAutoCommit(false);

            DatabaseMetaData meta = conn.getMetaData();
            System.out.println("  драйвер:          " + meta.getDriverName() + " " + meta.getDriverVersion());
            System.out.println("  сервер назвался:  " + meta.getDatabaseProductName()
                    + " " + meta.getDatabaseProductVersion());

            try (Statement st = conn.createStatement()) {
                st.execute("CREATE TABLE IF NOT EXISTS jdbc_counter (id INT8 PRIMARY KEY, hits INT8 NOT NULL)");
                st.execute("UPSERT INTO jdbc_counter (id, hits) VALUES (1, 0)");
                conn.commit();
            }

            int totalAttempts = 0;
            int retried = 0;
            int failed = 0;
            int succeeded = 0;

            for (int i = 0; i < iterations; i++) {
                int attempts = incrementWithRetry(conn);
                if (attempts < 0) {
                    // Предел повторов исчерпан: транзакция не прошла, но
                    // попытки всё равно были — их ровно MAX_ATTEMPTS.
                    // Прежняя версия прибавляла сюда -1 и тем ВЫЧИТАЛА
                    // попытку из общего счёта, занижая отчёт.
                    failed++;
                    totalAttempts += MAX_ATTEMPTS;
                    retried++;
                    continue;
                }
                succeeded++;
                totalAttempts += attempts;
                if (attempts > 1) {
                    retried++;
                }
            }

            long finalValue;
            try (Statement st = conn.createStatement();
                 ResultSet rs = st.executeQuery("SELECT hits FROM jdbc_counter WHERE id = 1")) {
                rs.next();
                finalValue = rs.getLong(1);
                conn.commit();
            }

            System.out.println("  транзакций начато:  " + iterations);
            System.out.println("  успешных:           " + succeeded);
            System.out.println("  не удалось:         " + failed);
            System.out.println("  из них с повтором:  " + retried);
            System.out.println("  всего попыток:      " + totalAttempts);
            System.out.println("  счётчик в базе:     " + finalValue);

            if (succeeded + failed != iterations) {
                throw new IllegalStateException(
                        "успешные плюс неудачные не сходятся с числом начатых транзакций: "
                                + succeeded + " + " + failed + " != " + iterations);
            }
            if (totalAttempts < succeeded) {
                throw new IllegalStateException(
                        "попыток меньше, чем успешных транзакций — учёт сломан: "
                                + totalAttempts + " < " + succeeded);
            }
            if (finalValue < succeeded) {
                throw new IllegalStateException(
                        "счётчик меньше числа успешных транзакций — потеряны обновления: "
                                + finalValue + " < " + succeeded);
            }
        }
    }

    /**
     * Читает значение и записывает увеличенное — тот самый паттерн, на
     * котором в распределённой БД появляются ретраи.
     *
     * @return число попыток, либо -1, если предел исчерпан
     */
    private static int incrementWithRetry(Connection conn) throws SQLException {
        for (int attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
            try {
                long hits;
                try (PreparedStatement read =
                             conn.prepareStatement("SELECT hits FROM jdbc_counter WHERE id = 1");
                     ResultSet rs = read.executeQuery()) {
                    rs.next();
                    hits = rs.getLong(1);
                }
                try (PreparedStatement write =
                             conn.prepareStatement("UPDATE jdbc_counter SET hits = ? WHERE id = 1")) {
                    write.setLong(1, hits + 1);
                    write.executeUpdate();
                }
                conn.commit();
                return attempt;
            } catch (SQLException e) {
                conn.rollback();
                if (!RETRY_SQLSTATE.equals(e.getSQLState())) {
                    throw e;
                }
                try {
                    // Пауза с ДЖИТТЕРОМ, а не голая экспонента. Без
                    // случайной добавки все конкуренты, отменённые одним и
                    // тем же конфликтом, просыпаются одновременно и
                    // воспроизводят его снова — ровно то, чего retry-цикл
                    // должен избегать. В Go-генераторе этого стенда джиттер
                    // есть, и пример на JDBC не должен ему противоречить.
                    long backoff = Math.min(100L, 1L << (attempt - 1));
                    Thread.sleep(backoff + ThreadLocalRandom.current().nextLong(0, 5));
                } catch (InterruptedException ie) {
                    Thread.currentThread().interrupt();
                    throw new SQLException("прервано во время паузы перед повтором", ie);
                }
            }
        }
        return -1;
    }
}
