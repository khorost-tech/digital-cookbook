package tech.khorost.testing.distributed.failures;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.SQLException;

/**
 * Обработчик перевода с защитой от повторной доставки. В распределённых
 * системах доставка "at-least-once" — норма: одно и то же событие может
 * прийти дважды (ретрай продюсера, ребаланс консюмера, дубль в сети).
 *
 * Инвариант: эффект (изменение баланса) применяется РОВНО ОДИН РАЗ на
 * event_id. Реализация — ключ идемпотентности в таблице processed_events
 * плюс сам эффект в одной транзакции: либо оба, либо ничего.
 */
public final class IdempotentTransferHandler {

    private final Connection connection;

    public IdempotentTransferHandler(Connection connection) {
        this.connection = connection;
    }

    /**
     * Применяет дельту к счёту, если событие ещё не обрабатывалось.
     *
     * @return true — эффект применён (первая доставка);
     *         false — событие уже было обработано (дубль, эффект пропущен).
     */
    public boolean applyOnce(String eventId, String accountId, long delta) throws SQLException {
        boolean previousAutoCommit = connection.getAutoCommit();
        connection.setAutoCommit(false);
        try {
            // Пытаемся "застолбить" event_id. ON CONFLICT DO NOTHING делает
            // проверку и вставку атомарно — без гонки между SELECT и INSERT.
            boolean firstDelivery;
            String claim = "INSERT INTO processed_events (event_id) VALUES (?) "
                    + "ON CONFLICT (event_id) DO NOTHING";
            try (PreparedStatement ps = connection.prepareStatement(claim)) {
                ps.setString(1, eventId);
                firstDelivery = ps.executeUpdate() == 1;
            }

            if (!firstDelivery) {
                // Дубль: событие уже обработано. Эффект не применяем.
                connection.rollback();
                return false;
            }

            // Первая доставка: применяем эффект в той же транзакции.
            String apply = "UPDATE accounts SET balance = balance + ? WHERE id = ?";
            try (PreparedStatement ps = connection.prepareStatement(apply)) {
                ps.setLong(1, delta);
                ps.setString(2, accountId);
                ps.executeUpdate();
            }

            connection.commit();
            return true;
        } catch (SQLException e) {
            connection.rollback();
            throw e;
        } finally {
            connection.setAutoCommit(previousAutoCommit);
        }
    }
}
