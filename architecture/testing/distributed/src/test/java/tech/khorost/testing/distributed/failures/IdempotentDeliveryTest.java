package tech.khorost.testing.distributed.failures;

import org.flywaydb.core.Flyway;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.testcontainers.containers.PostgreSQLContainer;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.SQLException;
import java.sql.Statement;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Тест идемпотентности при повторной доставке. Имитируем дубль: одно и то же
 * событие (тот же event_id) обрабатывается дважды. Эффект (изменение баланса)
 * должен примениться РОВНО один раз.
 *
 * Реализация без внешних chaos-тулов — чистая логика обработчика плюс таблица
 * processed_events как ключ идемпотентности.
 */
class IdempotentDeliveryTest {

    private static final PostgreSQLContainer<?> POSTGRES =
            new PostgreSQLContainer<>("postgres:18-alpine");

    private static Connection connection;

    @BeforeAll
    static void startAndMigrate() throws SQLException {
        POSTGRES.start();

        Flyway.configure()
                .dataSource(POSTGRES.getJdbcUrl(), POSTGRES.getUsername(), POSTGRES.getPassword())
                .locations("classpath:db/migration")
                .load()
                .migrate();

        connection = DriverManager.getConnection(
                POSTGRES.getJdbcUrl(), POSTGRES.getUsername(), POSTGRES.getPassword());
    }

    @AfterAll
    static void stop() throws SQLException {
        if (connection != null) {
            connection.close();
        }
        POSTGRES.stop();
    }

    @BeforeEach
    void resetState() throws SQLException {
        // Чистое состояние на каждый тест: пустой журнал событий и счёт с нулём.
        try (Statement st = connection.createStatement()) {
            st.execute("TRUNCATE processed_events");
            st.execute("DELETE FROM accounts");
            st.execute("INSERT INTO accounts (id, balance) VALUES ('acc-1', 0)");
        }
    }

    @Test
    void duplicateDelivery_appliesEffectExactlyOnce() throws SQLException {
        IdempotentTransferHandler handler = new IdempotentTransferHandler(connection);

        // Одно и то же событие приходит дважды (дубль доставки).
        boolean firstApplied = handler.applyOnce("evt-1", "acc-1", 100);
        boolean secondApplied = handler.applyOnce("evt-1", "acc-1", 100);

        assertTrue(firstApplied, "первая доставка применяет эффект");
        assertFalse(secondApplied, "повторная доставка эффект пропускает");
        assertEquals(100, balanceOf("acc-1"),
                "баланс изменился ровно один раз, несмотря на два вызова");
    }

    @Test
    void distinctEvents_applyIndependently() throws SQLException {
        IdempotentTransferHandler handler = new IdempotentTransferHandler(connection);

        // Разные event_id — оба эффекта применяются.
        assertTrue(handler.applyOnce("evt-A", "acc-1", 30));
        assertTrue(handler.applyOnce("evt-B", "acc-1", 70));
        assertEquals(100, balanceOf("acc-1"), "два разных события дают сумму эффектов");
    }

    private long balanceOf(String accountId) throws SQLException {
        try (var ps = connection.prepareStatement("SELECT balance FROM accounts WHERE id = ?")) {
            ps.setString(1, accountId);
            try (var rs = ps.executeQuery()) {
                rs.next();
                return rs.getLong(1);
            }
        }
    }
}
