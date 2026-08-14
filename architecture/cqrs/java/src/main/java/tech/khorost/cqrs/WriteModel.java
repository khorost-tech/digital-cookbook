package tech.khorost.cqrs;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.List;

/**
 * Write-сторона CQRS: единственная append-only таблица событий {@code order_events} —
 * «источник» для проекций. Команды только добавляют строки; текущее состояние получается
 * сверткой (fold) этого лога. Намеренно простая (без event store со снапшотами: тем занят
 * соседний стенд event-sourcing/) — фокус здесь на READ-стороне.
 */
final class WriteModel {

    private final Connection db;

    WriteModel(Connection db) {
        this.db = db;
    }

    /** Результат команды createOrder: бизнес-id заказа + seq порождённого события (токен позиции). */
    record CreateResult(long orderId, long seq) {
    }

    /** Создаёт write-сторону: append-only лог событий + отдельную последовательность для order_id. */
    void setup() throws SQLException {
        try (Statement st = db.createStatement()) {
            st.execute("""
                DROP TABLE IF EXISTS order_events;
                CREATE TABLE order_events (
                    seq        BIGSERIAL PRIMARY KEY,   -- монотонная позиция в логе (хвост = max(seq))
                    order_id   BIGINT      NOT NULL,
                    user_id    TEXT        NOT NULL,
                    type       TEXT        NOT NULL,     -- 'created' | 'paid'
                    amount     BIGINT      NOT NULL DEFAULT 0,
                    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
                );
                CREATE INDEX ON order_events (user_id);
                -- Отдельная последовательность для order_id, чтобы id заказа не совпадал с seq лога
                -- (в реальности это разные пространства: бизнес-id заказа vs позиция в журнале).
                DROP SEQUENCE IF EXISTS order_id_seq;
                CREATE SEQUENCE order_id_seq START 1000;
                """);
        }
    }

    /**
     * Команда write-стороны: заводит новый заказ. Возвращает id заказа и seq события (токен:
     * по нему read-сторона понимает, догнала ли проекция эту запись). Только append в лог.
     */
    CreateResult createOrder(String userId, long amount) throws SQLException {
        long orderId;
        try (Statement st = db.createStatement();
             ResultSet rs = st.executeQuery("SELECT nextval('order_id_seq')")) {
            rs.next();
            orderId = rs.getLong(1);
        }
        long seq;
        try (PreparedStatement ps = db.prepareStatement(
                "INSERT INTO order_events (order_id, user_id, type, amount) "
                        + "VALUES (?, ?, 'created', ?) RETURNING seq")) {
            ps.setLong(1, orderId);
            ps.setString(2, userId);
            ps.setLong(3, amount);
            try (ResultSet rs = ps.executeQuery()) {
                rs.next();
                seq = rs.getLong(1);
            }
        }
        return new CreateResult(orderId, seq);
    }

    /** Команда: помечает заказ оплаченным (ещё одно событие в логе). Возвращает seq события. */
    long payOrder(long orderId) throws SQLException {
        String userId;
        try (PreparedStatement ps = db.prepareStatement(
                "SELECT user_id FROM order_events WHERE order_id=? AND type='created'")) {
            ps.setLong(1, orderId);
            try (ResultSet rs = ps.executeQuery()) {
                rs.next();
                userId = rs.getString(1);
            }
        }
        try (PreparedStatement ps = db.prepareStatement(
                "INSERT INTO order_events (order_id, user_id, type) VALUES (?, ?, 'paid') RETURNING seq")) {
            ps.setLong(1, orderId);
            ps.setString(2, userId);
            try (ResultSet rs = ps.executeQuery()) {
                rs.next();
                return rs.getLong(1);
            }
        }
    }

    /** Позиция хвоста лога (максимальный seq). Вместе с чекпоинтом проектора даёт лаг проекции. */
    long tailSeq() throws SQLException {
        try (Statement st = db.createStatement();
             ResultSet rs = st.executeQuery("SELECT coalesce(max(seq), 0) FROM order_events")) {
            rs.next();
            return rs.getLong(1);
        }
    }

    /**
     * АВТОРИТЕТНОЕ текущее состояние заказов пользователя, свёрнутое прямо из лога событий
     * (write-сторона). Всегда свежее — здесь нет лага. Именно этот путь используется для
     * read-your-writes: собственные, только что записанные заказы читаем отсюда, а не из
     * отстающей проекции.
     */
    List<Order> foldUserOrdersWriteSide(String userId) throws SQLException {
        String q = """
            SELECT e.order_id, e.user_id,
                   (SELECT amount FROM order_events c
                      WHERE c.order_id = e.order_id AND c.type = 'created')            AS amount,
                   (SELECT CASE WHEN bool_or(type = 'paid') THEN 'paid' ELSE 'new' END
                      FROM order_events s WHERE s.order_id = e.order_id)               AS status,
                   max(e.seq)                                                          AS updated_seq
              FROM order_events e
             WHERE e.user_id = ?
             GROUP BY e.order_id, e.user_id
             ORDER BY e.order_id""";
        try (PreparedStatement ps = db.prepareStatement(q)) {
            ps.setString(1, userId);
            try (ResultSet rs = ps.executeQuery()) {
                return scanOrders(rs);
            }
        }
    }

    /** Общий разбор результата: колонки идут order_id, user_id, amount, status, updated_seq. */
    static List<Order> scanOrders(ResultSet rs) throws SQLException {
        List<Order> out = new ArrayList<>();
        while (rs.next()) {
            out.add(new Order(
                    rs.getLong(1),     // order_id
                    rs.getString(2),   // user_id
                    rs.getString(4),   // status
                    rs.getLong(3),     // amount
                    rs.getLong(5)));   // updated_seq
        }
        return out;
    }
}
