// JDBC-пример к статье «Транзакции в реляционных БД на практике: PostgreSQL».
// https://khorost.tech/databases/transactions-relational-postgres-practice/
//
// Показывает то же, что Go-нагрузчик, но в терминах JDBC: уровень изоляции через
// Connection.setTransactionIsolation и обязательный retry на SQLState 40001 для SERIALIZABLE.
// Мелкий прогон (демонстрация API, не полноценный бенчмарк — он в Go-нагрузчике).
//
// Запуск (Postgres поднят через ../docker-compose.yml, схема применена):
//   mvn -q compile exec:java
//   DSN=jdbc:postgresql://localhost:5440/demo mvn -q compile exec:java

import java.sql.*;
import java.util.concurrent.*;

public class Main {
    static final String URL  = System.getenv().getOrDefault("DSN", "jdbc:postgresql://localhost:5440/demo");
    static final String USER = "demo";
    static final String PASS = "demo";

    static final int THREADS = 8;
    static final int ITERS   = 50;

    public static void main(String[] args) throws Exception {
        try (Connection c = conn()) {
            c.createStatement().execute(
                "INSERT INTO accounts (id, balance) VALUES (1, 0) ON CONFLICT (id) DO UPDATE SET balance = 0");
        }

        // naive read-modify-write на Read Committed → потерянные обновления
        long naive = runConcurrent(Main::incrementNaive);
        // SERIALIZABLE + retry на 40001 → корректный итог
        long serializable = runConcurrent(Main::incrementSerializable);

        long expected = (long) THREADS * ITERS;
        System.out.printf("expected=%d  naive(RC)=%d (lost %d)  serializable+retry=%d%n",
                expected, naive, expected - naive, serializable);
    }

    /** Прогоняет approach в THREADS потоков по ITERS итераций, возвращает итоговый баланс. */
    static long runConcurrent(SqlOp op) throws Exception {
        try (Connection c = conn()) {
            c.createStatement().execute("UPDATE accounts SET balance = 0 WHERE id = 1");
        }
        ExecutorService pool = Executors.newFixedThreadPool(THREADS);
        CountDownLatch done = new CountDownLatch(THREADS);
        for (int t = 0; t < THREADS; t++) {
            pool.submit(() -> {
                try (Connection c = conn()) {
                    for (int i = 0; i < ITERS; i++) op.run(c);
                } catch (SQLException e) {
                    throw new RuntimeException(e);
                } finally {
                    done.countDown();
                }
            });
        }
        done.await();
        pool.shutdown();
        try (Connection c = conn(); ResultSet rs = c.createStatement().executeQuery(
                "SELECT balance FROM accounts WHERE id = 1")) {
            rs.next();
            return rs.getLong(1);
        }
    }

    /** naive: SELECT + UPDATE на Read Committed без блокировки — теряет обновления. */
    static void incrementNaive(Connection c) throws SQLException {
        c.setAutoCommit(false);
        c.setTransactionIsolation(Connection.TRANSACTION_READ_COMMITTED);
        long bal;
        try (ResultSet rs = c.createStatement().executeQuery("SELECT balance FROM accounts WHERE id = 1")) {
            rs.next();
            bal = rs.getLong(1);
        }
        try (PreparedStatement ps = c.prepareStatement("UPDATE accounts SET balance = ? WHERE id = 1")) {
            ps.setLong(1, bal + 1);
            ps.executeUpdate();
        }
        c.commit();
    }

    /** SERIALIZABLE + retry на SQLState 40001 (serialization_failure) — корректно. */
    static void incrementSerializable(Connection c) throws SQLException {
        c.setAutoCommit(false);
        c.setTransactionIsolation(Connection.TRANSACTION_SERIALIZABLE);
        while (true) {
            try {
                long bal;
                try (ResultSet rs = c.createStatement().executeQuery("SELECT balance FROM accounts WHERE id = 1")) {
                    rs.next();
                    bal = rs.getLong(1);
                }
                try (PreparedStatement ps = c.prepareStatement("UPDATE accounts SET balance = ? WHERE id = 1")) {
                    ps.setLong(1, bal + 1);
                    ps.executeUpdate();
                }
                c.commit();
                return;
            } catch (SQLException e) {
                c.rollback();
                if ("40001".equals(e.getSQLState())) continue; // сериализационный конфликт — повторяем
                throw e;
            }
        }
    }

    static Connection conn() throws SQLException {
        return DriverManager.getConnection(URL, USER, PASS);
    }

    interface SqlOp {
        void run(Connection c) throws SQLException;
    }
}
