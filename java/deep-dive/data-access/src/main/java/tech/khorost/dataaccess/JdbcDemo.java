package tech.khorost.dataaccess;

import javax.sql.DataSource;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.TreeMap;

/**
 * (1) Ручной JDBC: один SQL с JOIN, весь маппинг ResultSet -> объекты руками.
 * Максимальный контроль над SQL (можно писать любой запрос как есть),
 * но и максимум ручного кода — маппинг, порядок колонок, приведение типов.
 */
public final class JdbcDemo {
    private JdbcDemo() {
    }

    /** authorName -> список названий книг, по одному JOIN-запросу. */
    public static Map<String, java.util.List<String>> authorsWithBooks(DataSource ds) throws SQLException {
        String sql = """
                SELECT a.id, a.name, b.title
                FROM authors a
                JOIN books b ON b.author_id = a.id
                ORDER BY a.id, b.id""";

        Map<String, java.util.List<String>> result = new LinkedHashMap<>();
        try (Connection conn = ds.getConnection();
             PreparedStatement ps = conn.prepareStatement(sql);
             ResultSet rs = ps.executeQuery()) {
            while (rs.next()) {
                String author = rs.getString("name");
                String title = rs.getString("title");
                result.computeIfAbsent(author, k -> new java.util.ArrayList<>()).add(title);
            }
        }
        System.out.println("JDBC: 1 SQL-запрос (JOIN authors+books), authors=" + result.size());
        return new TreeMap<>(result);
    }
}
