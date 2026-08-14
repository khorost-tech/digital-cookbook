package tech.khorost.cqrs;

import java.sql.Connection;
import java.sql.SQLException;
import java.sql.Statement;

/**
 * Blue-green пересборка проекции без остановки чтения.
 * <p>
 * Идея: старая проекция {@code orders_read} продолжает обслуживать читателей. Рядом с нуля
 * строим {@code orders_read_v2} полным реплеем лога (например, изменили схему проекции или
 * чиним баг проектора). Когда v2 догнала хвост — атомарно меняем таблицы местами одной DDL-
 * транзакцией. PostgreSQL выполняет RENAME транзакционно: читатели до COMMIT видят старую
 * orders_read, после — новую, без промежуточного «нет таблицы».
 */
final class Rebuild {

    private final Connection db;
    private final Projector projector;

    Rebuild(Connection db, Projector projector) {
        this.db = db;
        this.projector = projector;
    }

    /** Пересобирает orders_read_v2 полным реплеем и атомарно переключает. Возвращает число реплейнутых событий. */
    int rebuildReadModel() throws SQLException {
        // 1) Строим v2 «зелёную» рядом с «синей» orders_read. Схема идентична — в реальной
        //    пересборке здесь могла бы быть новая/исправленная структура проекции.
        try (Statement st = db.createStatement()) {
            st.execute("""
                DROP TABLE IF EXISTS orders_read_v2;
                CREATE TABLE orders_read_v2 (
                    order_id    BIGINT PRIMARY KEY,
                    user_id     TEXT   NOT NULL,
                    status      TEXT   NOT NULL,
                    amount      BIGINT NOT NULL,
                    updated_seq BIGINT NOT NULL
                );
                CREATE INDEX ON orders_read_v2 (user_id);
                INSERT INTO projection_checkpoint (name, last_seq) VALUES ('orders_read_v2', 0)
                    ON CONFLICT (name) DO UPDATE SET last_seq = 0;
                """);
        }

        // 2) Реплеим весь лог в v2 (той же идемпотентной механикой applyLog). Чтение
        //    orders_read в это время не блокируется — работаем в отдельной таблице.
        int applied = projector.applyLog("orders_read_v2", "projection_checkpoint");

        // 3) Атомарный blue-green switch: меняем таблицы местами в одной транзакции. Заодно
        //    переносим позицию чекпоинта, чтобы штатный проектор продолжил с того места, до
        //    которого догнала v2. Многооператорный DDL без параметров — одной транзакцией.
        db.setAutoCommit(false);
        try (Statement st = db.createStatement()) {
            st.execute("""
                DROP TABLE IF EXISTS orders_read_old;
                ALTER TABLE orders_read    RENAME TO orders_read_old;
                ALTER TABLE orders_read_v2 RENAME TO orders_read;
                UPDATE projection_checkpoint SET last_seq =
                    (SELECT last_seq FROM projection_checkpoint WHERE name='orders_read_v2')
                    WHERE name='orders_read';
                DELETE FROM projection_checkpoint WHERE name='orders_read_v2';
                DROP TABLE orders_read_old;
                """);
            db.commit();
        } catch (SQLException e) {
            db.rollback();
            throw e;
        } finally {
            db.setAutoCommit(true);
        }
        return applied;
    }
}
