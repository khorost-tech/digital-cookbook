package tech.khorost.testing.distributed.integration;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.math.BigDecimal;

/**
 * DAO для таблицы orders. Специально написан на голом JDBC, чтобы в тесте
 * работал НАСТОЯЩИЙ SQL реального Postgres, а не поведение мока.
 *
 * Ключевой момент для статьи: UNIQUE(external_id) — это инвариант базы.
 * Юнит-тест с мок-репозиторием такой constraint не воспроизведёт: мок
 * "успешно сохранит" дубль. Интеграционный тест на живой БД поймает.
 */
public final class OrderRepository {

    private final Connection connection;

    public OrderRepository(Connection connection) {
        this.connection = connection;
    }

    /**
     * Прямая вставка. При дубле external_id Postgres бросит нарушение
     * UNIQUE-констрейнта (SQLState 23505) — это и проверяет тест.
     */
    public long insert(String externalId, BigDecimal amount) throws SQLException {
        String sql = "INSERT INTO orders (external_id, amount) VALUES (?, ?) RETURNING id";
        try (PreparedStatement ps = connection.prepareStatement(sql)) {
            ps.setString(1, externalId);
            ps.setBigDecimal(2, amount);
            try (ResultSet rs = ps.executeQuery()) {
                rs.next();
                return rs.getLong(1);
            }
        }
    }

    /**
     * Идемпотентная вставка (upsert через ON CONFLICT DO NOTHING).
     * Повторный вызов с тем же external_id не создаёт дубль и не падает.
     *
     * @return true, если строка реально вставлена; false, если уже была.
     */
    public boolean insertIfAbsent(String externalId, BigDecimal amount) throws SQLException {
        String sql = "INSERT INTO orders (external_id, amount) VALUES (?, ?) "
                + "ON CONFLICT (external_id) DO NOTHING";
        try (PreparedStatement ps = connection.prepareStatement(sql)) {
            ps.setString(1, externalId);
            ps.setBigDecimal(2, amount);
            return ps.executeUpdate() == 1;
        }
    }

    /** Сколько строк с данным external_id (ожидаемо 0 или 1). */
    public int countByExternalId(String externalId) throws SQLException {
        String sql = "SELECT count(*) FROM orders WHERE external_id = ?";
        try (PreparedStatement ps = connection.prepareStatement(sql)) {
            ps.setString(1, externalId);
            try (ResultSet rs = ps.executeQuery()) {
                rs.next();
                return rs.getInt(1);
            }
        }
    }
}
