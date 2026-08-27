package tech.khorost.testing.distributed.integration;

import org.flywaydb.core.Flyway;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.postgresql.util.PSQLState;
import org.testcontainers.containers.PostgreSQLContainer;

import java.math.BigDecimal;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.SQLException;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Интеграционный тест на живом Postgres. Показывает то, что мок пропустил бы:
 * настоящий UNIQUE-constraint и идемпотентный upsert (ON CONFLICT).
 *
 * Контейнер поднимается один раз на класс. Схема накатывается Flyway'ем из
 * classpath:db/migration.
 */
class OrderRepositoryTest {

    // Образ есть локально — Testcontainers не будет тянуть его из сети.
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

    @Test
    void duplicateExternalId_violatesUniqueConstraint() throws SQLException {
        OrderRepository repo = new OrderRepository(connection);

        long id = repo.insert("ORD-1001", new BigDecimal("199.90"));
        assertTrue(id > 0, "первая вставка должна вернуть сгенерированный id");

        // Повторная прямая вставка того же external_id — реальный constraint БД.
        // Мок-репозиторий это поведение не воспроизвёл бы.
        SQLException ex = assertThrows(SQLException.class,
                () -> repo.insert("ORD-1001", new BigDecimal("199.90")));
        assertEquals(PSQLState.UNIQUE_VIOLATION.getState(), ex.getSQLState(),
                "ожидаем нарушение UNIQUE (SQLState 23505)");
    }

    @Test
    void insertIfAbsent_isIdempotent() throws SQLException {
        OrderRepository repo = new OrderRepository(connection);

        boolean firstInserted = repo.insertIfAbsent("ORD-2002", new BigDecimal("10.00"));
        boolean secondInserted = repo.insertIfAbsent("ORD-2002", new BigDecimal("10.00"));

        assertTrue(firstInserted, "первый upsert реально вставляет строку");
        assertFalse(secondInserted, "повторный upsert строку не создаёт");
        assertEquals(1, repo.countByExternalId("ORD-2002"),
                "в БД ровно одна строка, дубля нет");
    }
}
