package tech.khorost.observability;

import java.util.Optional;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Repository;

@Repository
public class InventoryRepository {

    /** SKU, обслуживаемый намеренно медленно: задержка создаётся в БД, а не в приложении. */
    public static final String SLOW_SKU = "SKU-0004";

    /** SKU, на котором сервис намеренно отказывает — нужен для tail sampling и алертов. */
    public static final String FAILING_SKU = "SKU-BOOM";

    private final JdbcClient jdbc;

    public InventoryRepository(JdbcClient jdbc) {
        this.jdbc = jdbc;
    }

    public Optional<Item> findBySku(String sku) {
        // Медленный путь сделан через pg_sleep, а не через Thread.sleep в Java,
        // намеренно: в waterfall трейса должно быть видно, что время потрачено
        // ВНУТРИ запроса к базе. Задержка в приложении выглядела бы в трейсе
        // совсем иначе, и вывод «узкое место — БД» был бы подделкой.
        //
        // pg_sleep вызывается в СПИСКЕ ВЫБОРКИ, а не в WHERE. Первая редакция
        // была `WHERE sku = ? AND pg_sleep(0.15) IS NULL`: pg_sleep возвращает
        // void, условие не выполнялось, запрос отдавал ноль строк, и сервис
        // честно отвечал 404 на существующий товар. Поймано сверкой факта с
        // планом нагрузки — 21 запрос из 200 вернул не тот код.
        String sql = SLOW_SKU.equals(sku)
                ? "SELECT sku, name, quantity, pg_sleep(0.15) FROM inventory WHERE sku = ?"
                : "SELECT sku, name, quantity FROM inventory WHERE sku = ?";

        return jdbc.sql(sql)
                .param(sku)
                .query((rs, rowNum) -> new Item(rs.getString("sku"), rs.getString("name"), rs.getInt("quantity")))
                .optional();
    }

    public void saveOrder(String orderId, String sku, int quantity, boolean reserved) {
        jdbc.sql("""
                INSERT INTO orders (order_id, sku, quantity, reserved)
                VALUES (?, ?, ?, ?)
                ON CONFLICT (order_id) DO NOTHING
                """)
                .params(orderId, sku, quantity, reserved)
                .update();
    }

    public long countOrders() {
        return jdbc.sql("SELECT count(*) FROM orders").query(Long.class).single();
    }
}
