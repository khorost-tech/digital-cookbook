package tech.khorost.cqrs;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.List;

/**
 * Read-сторона CQRS: async-проекция {@code orders_read} (денормализована и заточена под
 * запрос «список заказов пользователя» — индекс по user_id) плюс таблица чекпоинта
 * {@code projection_checkpoint} (позиция, до которой проектор уже применил лог).
 * <p>
 * Ключевой инвариант: чекпоинт двигается в ОДНОЙ транзакции с апдейтом проекции (см.
 * {@link #applyLog}). Иначе при сбое между «применил» и «записал позицию» события либо
 * потеряются, либо применятся дважды.
 */
final class Projector {

    private final Connection db;

    Projector(Connection db) {
        this.db = db;
    }

    /** Создаёт read-сторону: проекцию orders_read (индекс по user_id) + чекпоинт позиции. */
    void setup() throws SQLException {
        try (Statement st = db.createStatement()) {
            st.execute("""
                DROP TABLE IF EXISTS orders_read;
                CREATE TABLE orders_read (
                    order_id    BIGINT PRIMARY KEY,
                    user_id     TEXT   NOT NULL,
                    status      TEXT   NOT NULL,
                    amount      BIGINT NOT NULL,
                    updated_seq BIGINT NOT NULL
                );
                CREATE INDEX ON orders_read (user_id);   -- проекция под конкретный запрос: список заказов юзера

                DROP TABLE IF EXISTS projection_checkpoint;
                CREATE TABLE projection_checkpoint (
                    name     TEXT PRIMARY KEY,
                    last_seq BIGINT NOT NULL
                );
                INSERT INTO projection_checkpoint (name, last_seq) VALUES ('orders_read', 0);
                """);
        }
    }

    /**
     * Применяет к проекции все события лога с seq &gt; чекпоинта и атомарно сдвигает чекпоинт.
     * Применение идемпотентно (UPSERT по order_id), повтор того же батча после сбоя безопасен.
     * Именно то, что этот шаг вызывается ОТДЕЛЬНО от команд записи, и создаёт лаг.
     * Возвращает число применённых событий.
     */
    int projectOnce() throws SQLException {
        return applyLog("orders_read", "projection_checkpoint");
    }

    /**
     * Общая механика «применить хвост лога в таблицу-проекцию + сдвинуть чекпоинт в той же
     * транзакции». Используется и штатным проектором, и rebuild-ом (там применяет в
     * orders_read_v2 со своим чекпоинтом). Имена table/ckpt подставляются в SQL — это
     * доверенные внутренние константы, не пользовательский ввод.
     */
    int applyLog(String table, String ckpt) throws SQLException {
        db.setAutoCommit(false);
        try {
            long from;
            try (PreparedStatement ps = db.prepareStatement(
                    "SELECT last_seq FROM " + ckpt + " WHERE name=? FOR UPDATE")) {
                ps.setString(1, table);
                try (ResultSet rs = ps.executeQuery()) {
                    rs.next();
                    from = rs.getLong(1);
                }
            }

            record Ev(long seq, long orderId, String userId, String typ, long amount) {
            }
            List<Ev> evs = new ArrayList<>();
            try (PreparedStatement ps = db.prepareStatement(
                    "SELECT seq, order_id, user_id, type, amount FROM order_events "
                            + "WHERE seq > ? ORDER BY seq")) {
                ps.setLong(1, from);
                try (ResultSet rs = ps.executeQuery()) {
                    while (rs.next()) {
                        evs.add(new Ev(rs.getLong(1), rs.getLong(2), rs.getString(3),
                                rs.getString(4), rs.getLong(5)));
                    }
                }
            }

            long last = from;
            for (Ev e : evs) {
                switch (e.typ()) {
                    case "created" -> {
                        // Идемпотентный UPSERT: повторное применение того же события безопасно.
                        try (PreparedStatement ps = db.prepareStatement(
                                "INSERT INTO " + table + " (order_id, user_id, status, amount, updated_seq) "
                                        + "VALUES (?, ?, 'new', ?, ?) "
                                        + "ON CONFLICT (order_id) DO UPDATE "
                                        + "  SET status=EXCLUDED.status, amount=EXCLUDED.amount, "
                                        + "      updated_seq=EXCLUDED.updated_seq")) {
                            ps.setLong(1, e.orderId());
                            ps.setString(2, e.userId());
                            ps.setLong(3, e.amount());
                            ps.setLong(4, e.seq());
                            ps.executeUpdate();
                        }
                    }
                    case "paid" -> {
                        try (PreparedStatement ps = db.prepareStatement(
                                "UPDATE " + table + " SET status='paid', updated_seq=? WHERE order_id=?")) {
                            ps.setLong(1, e.seq());
                            ps.setLong(2, e.orderId());
                            ps.executeUpdate();
                        }
                    }
                    default -> {
                    }
                }
                last = e.seq();
            }

            try (PreparedStatement ps = db.prepareStatement(
                    "UPDATE " + ckpt + " SET last_seq=? WHERE name=?")) {
                ps.setLong(1, last);
                ps.setString(2, table);
                ps.executeUpdate();
            }
            db.commit();
            return evs.size();
        } catch (SQLException e) {
            db.rollback();
            throw e;
        } finally {
            db.setAutoCommit(true);
        }
    }

    /** До какого seq проектор уже применил лог. */
    long checkpointSeq() throws SQLException {
        try (Statement st = db.createStatement();
             ResultSet rs = st.executeQuery(
                     "SELECT last_seq FROM projection_checkpoint WHERE name='orders_read'")) {
            rs.next();
            return rs.getLong(1);
        }
    }

    /**
     * Метрика отставания проекции: хвост лога минус чекпоинт проектора. 0 — проекция догнала
     * лог; &gt;0 — есть непроигранные события (окно, в котором ломается read-your-writes).
     */
    long projectionLag(WriteModel write) throws SQLException {
        return write.tailSeq() - checkpointSeq();
    }

    /**
     * Read-путь через проекцию (быстро, денормализовано, но может отставать). Для «чужих»
     * заказов этого достаточно; для собственных свежих — нет (см. read-your-writes в Main).
     */
    List<Order> readUserOrdersFromProjection(String userId) throws SQLException {
        try (PreparedStatement ps = db.prepareStatement(
                "SELECT order_id, user_id, amount, status, updated_seq "
                        + "FROM orders_read WHERE user_id=? ORDER BY order_id")) {
            ps.setString(1, userId);
            try (ResultSet rs = ps.executeQuery()) {
                return WriteModel.scanOrders(rs);
            }
        }
    }

    /**
     * Второй приём read-your-writes: «подождать по токену». Крутит проектор, пока чекпоинт не
     * дойдёт до нужного seq, после чего из проекции уже видно собственную запись. В проде это
     * ожидание готовности проекции (а не busy-loop).
     */
    void waitForProjection(long token) throws SQLException {
        while (checkpointSeq() < token) {
            projectOnce();
        }
    }
}
