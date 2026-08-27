package tech.khorost.across;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.util.ArrayList;
import java.util.List;
import javax.sql.DataSource;

/** Реализация Store поверх Postgres — для интеграционного теста. */
public final class PostgresStore implements Store {

    public static final String SCHEMA = """
            CREATE TABLE IF NOT EXISTS orders (
                id         TEXT PRIMARY KEY,
                user_id    TEXT   NOT NULL,
                total_cent BIGINT NOT NULL,
                price_cent BIGINT NOT NULL
            )""";

    private final DataSource ds;

    public PostgresStore(DataSource ds) {
        this.ds = ds;
    }

    @Override
    public void save(Order o) {
        String sql = "INSERT INTO orders (id, user_id, total_cent, price_cent) VALUES (?, ?, ?, ?)";
        try (Connection c = ds.getConnection(); PreparedStatement st = c.prepareStatement(sql)) {
            st.setString(1, o.id());
            st.setString(2, o.userId());
            st.setLong(3, o.totalCent());
            st.setLong(4, o.priceCent());
            st.executeUpdate();
        } catch (SQLException e) {
            throw new IllegalStateException("вставка заказа " + o.id(), e);
        }
    }

    @Override
    public List<Order> byUser(String userId) {
        String sql = "SELECT id, user_id, total_cent, price_cent FROM orders WHERE user_id = ? ORDER BY id";
        try (Connection c = ds.getConnection(); PreparedStatement st = c.prepareStatement(sql)) {
            st.setString(1, userId);
            try (ResultSet rs = st.executeQuery()) {
                List<Order> out = new ArrayList<>();
                while (rs.next()) {
                    out.add(new Order(rs.getString(1), rs.getString(2), rs.getLong(3), rs.getLong(4)));
                }
                return out;
            }
        } catch (SQLException e) {
            throw new IllegalStateException("выборка заказов " + userId, e);
        }
    }
}
