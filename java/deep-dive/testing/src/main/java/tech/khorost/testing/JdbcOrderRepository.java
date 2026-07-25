package tech.khorost.testing;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.Optional;
import java.util.function.Supplier;

import tech.khorost.testing.Order.OrderStatus;

/**
 * Реальная реализация {@link OrderRepository} поверх JDBC. Используется только
 * в интеграционном тесте — против настоящего Postgres, поднятого Testcontainers,
 * без ORM и без пула соединений (не тема этого стенда).
 */
public class JdbcOrderRepository implements OrderRepository {

    private final Supplier<Connection> connectionSupplier;

    public JdbcOrderRepository(Supplier<Connection> connectionSupplier) {
        this.connectionSupplier = connectionSupplier;
    }

    @Override
    public Order save(Order order) {
        String sql = "INSERT INTO orders (customer_name, amount_cents, status) VALUES (?, ?, ?)";
        try (Connection connection = connectionSupplier.get();
             PreparedStatement statement = connection.prepareStatement(sql, Statement.RETURN_GENERATED_KEYS)) {
            statement.setString(1, order.customerName());
            statement.setLong(2, order.amountCents());
            statement.setString(3, order.status().name());
            statement.executeUpdate();
            try (ResultSet keys = statement.getGeneratedKeys()) {
                if (keys.next()) {
                    return order.withId(keys.getLong(1));
                }
            }
            throw new IllegalStateException("INSERT did not return a generated id");
        } catch (SQLException e) {
            throw new RuntimeException("failed to save order", e);
        }
    }

    @Override
    public Optional<Order> findById(long id) {
        String sql = "SELECT id, customer_name, amount_cents, status FROM orders WHERE id = ?";
        try (Connection connection = connectionSupplier.get();
             PreparedStatement statement = connection.prepareStatement(sql)) {
            statement.setLong(1, id);
            try (ResultSet rs = statement.executeQuery()) {
                if (!rs.next()) {
                    return Optional.empty();
                }
                Order order = new Order(
                        rs.getLong("id"),
                        rs.getString("customer_name"),
                        rs.getLong("amount_cents"),
                        OrderStatus.valueOf(rs.getString("status")));
                return Optional.of(order);
            }
        } catch (SQLException e) {
            throw new RuntimeException("failed to load order", e);
        }
    }
}
