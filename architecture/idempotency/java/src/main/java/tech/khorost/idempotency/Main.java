package tech.khorost.idempotency;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.List;

/**
 * Стенд «Гарантии доставки и идемпотентность» (Java, чистый JDBC) — порт Go-версии
 * (idempotency/go/main.go) к статье
 * https://khorost.tech/architecture/delivery-guarantees-idempotency/.
 * <p>
 * Что демонстрирует: связку «at-least-once доставка + идемпотентный консьюмер =
 * effectively-once». Источник эмитит события списания кислорода из бака со стабильным
 * event_id. «Брокер» имитирует at-least-once — доставляет часть событий ДВАЖДЫ. Один и
 * тот же поток доставок обрабатывают три консьюмера:
 * <ul>
 *   <li>(a) наивный дельта — balance -= amount на каждую доставку. Дубли ЗАДВАИВАЮТ
 *       списание → баланс НЕВЕРНЫЙ (ожидаемо, ломаем нарочно).</li>
 *   <li>(b) dedup-ключ — в ОДНОЙ транзакции: пытаемся записать event_id в таблицу
 *       обработанных ключей; ключ новый — применяем эффект, уже был — пропускаем.
 *       Дубли безвредны → баланс ВЕРНЫЙ.</li>
 *   <li>(c) естественная идемпотентность — событие несёт итоговый снимок remaining;
 *       применение balance = remaining (UPSERT) идемпотентно само по себе, дубли
 *       безвредны БЕЗ хранилища ключей → баланс ВЕРНЫЙ.</li>
 * </ul>
 * Ассерты падают (System.exit(1)) при расхождении: у (b) и (c) итог == сумме уникальных
 * списаний; у (a) итог обязан быть НЕВЕРНЫМ (иначе задвоение не воспроизвелось).
 * <p>
 * Запуск: docker compose -f idempotency/compose/compose.yml up -d; затем в idempotency/java:
 * mvn -q compile exec:java. Переопределить строку подключения — переменная окружения
 * DATABASE_URL (JDBC-URL), по умолчанию jdbc:postgresql://localhost:5450/idem.
 */
public final class Main {

    /** Единственный бак в демонстрации. */
    static final String TANK = "T-1";

    /** Стартовый объём кислорода в баке (условные единицы). */
    static final long START_BALANCE = 1000;

    static final String SCHEMA = """
        CREATE TABLE IF NOT EXISTS balances (
            variant text   NOT NULL,
            tank    text   NOT NULL,
            balance bigint NOT NULL,
            PRIMARY KEY (variant, tank)
        );
        CREATE TABLE IF NOT EXISTS processed_keys (
            event_id     text        PRIMARY KEY,
            processed_at timestamptz NOT NULL DEFAULT now()
        );
        """;

    /**
     * Событие списания кислорода.
     * <p>
     * eventId — стабильный идентификатор события (одинаков у оригинала и его дубля);
     * amount — сколько списать в этом событии;
     * remaining — итоговый снимок остатка ПОСЛЕ этого списания (для варианта (c)):
     * событие несёт не дельту, а конечное значение, поэтому повторное применение
     * ничего не меняет.
     */
    record Event(String eventId, long amount, long remaining) {
    }

    public static void main(String[] args) throws Exception {
        String url = System.getenv().getOrDefault("DATABASE_URL", "jdbc:postgresql://localhost:5450/idem");
        String user = System.getenv().getOrDefault("PGUSER", "idem");
        String pass = System.getenv().getOrDefault("PGPASSWORD", "idem");

        try (Connection db = DriverManager.getConnection(url, user, pass)) {
            db.setAutoCommit(true);
            setupSchema(db);

            // --- Источник: генерируем поток уникальных событий списания ---
            // Амплитуды подобраны так, чтобы сумма не ушла в минус. remaining считаем
            // как бегущий остаток — это и есть «снимок» для естественной идемпотентности.
            long[] amounts = {50, 30, 70, 20, 90, 40, 60, 25};
            List<Event> events = new ArrayList<>(amounts.length);
            long running = START_BALANCE;
            long uniqueSpend = 0;
            for (int i = 0; i < amounts.length; i++) {
                running -= amounts[i];
                uniqueSpend += amounts[i];
                events.add(new Event(String.format("evt-%03d", i + 1), amounts[i], running));
            }
            long expected = START_BALANCE - uniqueSpend; // корректный итог после уникальных списаний

            // --- «Брокер» с семантикой at-least-once ---
            // Каждое третье событие доставляется ДВАЖДЫ (инъекция дубля сразу за оригиналом,
            // порядок сохраняется — так дубль варианта (c) безопасен и без хранилища ключей).
            List<Event> deliveries = new ArrayList<>();
            int dupCount = 0;
            for (int i = 0; i < events.size(); i++) {
                Event e = events.get(i);
                deliveries.add(e);
                if ((i + 1) % 3 == 0) {
                    deliveries.add(e); // повторная доставка того же event_id
                    dupCount++;
                }
            }

            System.out.printf(
                    "Бак %s: старт %d, уникальных событий %d, доставок (с дублями) %d, инъецировано дублей %d%n",
                    TANK, START_BALANCE, events.size(), deliveries.size(), dupCount);
            System.out.printf("Сумма уникальных списаний %d → корректный итог должен быть %d%n%n",
                    uniqueSpend, expected);

            // --- Прогоняем ОДИН И ТОТ ЖЕ поток доставок через три консьюмера ---
            long naive = runNaive(db, deliveries);
            long[] dedupResult = runDedup(db, deliveries);
            long dedup = dedupResult[0];
            long dropped = dedupResult[1];
            long natural = runNatural(db, deliveries);

            // --- Итоги ---
            System.out.println("Итоговые балансы:");
            System.out.printf("  (a) наивный дельта          balance=%d  (ожидаемо НЕВЕРНО, задвоено на %d)%n",
                    naive, expected - naive);
            System.out.printf("  (b) dedup-ключ              balance=%d  (отброшено дублей: %d)%n", dedup, dropped);
            System.out.printf("  (c) естественная идемпот.   balance=%d  (хранилище ключей не нужно)%n", natural);
            System.out.println();

            // --- Ассерты (падают при расхождении с ожиданием) ---
            assertTrue(dedup == expected,
                    "(b) dedup баланс " + dedup + " != ожидаемого " + expected);
            assertTrue(natural == expected,
                    "(c) естественная идемпотентность " + natural + " != ожидаемого " + expected);
            assertTrue(dropped == dupCount,
                    "(b) отброшено дублей " + dropped + ", а инъецировано " + dupCount);
            // Вариант (a) ОБЯЗАН быть неверным — иначе задвоение не воспроизвелось и демонстрация бессмысленна.
            assertTrue(naive != expected,
                    "(a) наивный баланс " + naive + " совпал с корректным — дубли не задвоили списание");

            System.out.println("OK: (b) dedup-ключ и (c) естественная идемпотентность дают effectively-once");
            System.out.println("    поверх at-least-once; (a) наивный дельта задваивает списание на дублях.");
        }
    }

    /**
     * Создаёт таблицы (idempotent) и сбрасывает данные, чтобы повторные запуски были
     * воспроизводимы. Балансы каждого варианта — в отдельной строке таблицы balances
     * (variant = a/b/c). Обработанные ключи — только для (b).
     */
    static void setupSchema(Connection db) throws SQLException {
        try (Statement st = db.createStatement()) {
            st.execute(SCHEMA);
            st.execute("TRUNCATE processed_keys");
        }
        try (PreparedStatement ps = db.prepareStatement("DELETE FROM balances WHERE tank=?")) {
            ps.setString(1, TANK);
            ps.executeUpdate();
        }
        try (PreparedStatement ps = db.prepareStatement(
                "INSERT INTO balances(variant, tank, balance) VALUES(?,?,?)")) {
            for (String v : new String[]{"a", "b", "c"}) {
                ps.setString(1, v);
                ps.setString(2, TANK);
                ps.setLong(3, START_BALANCE);
                ps.executeUpdate();
            }
        }
    }

    /**
     * Вариант (a): на КАЖДУЮ доставку безусловно вычитаем amount. Никакой защиты от
     * дублей — повторная доставка списывает ещё раз. Итог занижен.
     */
    static long runNaive(Connection db, List<Event> deliveries) throws SQLException {
        try (PreparedStatement ps = db.prepareStatement(
                "UPDATE balances SET balance = balance - ? WHERE variant='a' AND tank=?")) {
            for (Event e : deliveries) {
                ps.setLong(1, e.amount());
                ps.setString(2, TANK);
                ps.executeUpdate();
            }
        }
        return readBalance(db, "a");
    }

    /**
     * Вариант (b): дедупликация по event_id в ОДНОЙ транзакции. INSERT ... ON CONFLICT
     * DO NOTHING атомарно решает «ключ новый или уже был»: если строка вставлена
     * (affected==1) — применяем эффект в той же транзакции; если конфликт (дубль) —
     * эффект не применяем. Запись ключа и списание коммитятся вместе, поэтому падение
     * между ними не разъедет состояние. Возвращает {итоговый баланс, число отброшенных дублей}.
     */
    static long[] runDedup(Connection db, List<Event> deliveries) throws SQLException {
        long dropped = 0;
        db.setAutoCommit(false);
        try (PreparedStatement insKey = db.prepareStatement(
                "INSERT INTO processed_keys(event_id) VALUES(?) ON CONFLICT (event_id) DO NOTHING");
             PreparedStatement upd = db.prepareStatement(
                     "UPDATE balances SET balance = balance - ? WHERE variant='b' AND tank=?")) {
            for (Event e : deliveries) {
                try {
                    insKey.setString(1, e.eventId());
                    int affected = insKey.executeUpdate();
                    if (affected == 1) {
                        // Ключ новый — применяем эффект в той же транзакции.
                        upd.setLong(1, e.amount());
                        upd.setString(2, TANK);
                        upd.executeUpdate();
                    } else {
                        // Ключ уже обработан — это дубль, пропускаем эффект.
                        dropped++;
                    }
                    db.commit();
                } catch (SQLException ex) {
                    db.rollback();
                    throw ex;
                }
            }
        } finally {
            db.setAutoCommit(true);
        }
        return new long[]{readBalance(db, "b"), dropped};
    }

    /**
     * Вариант (c): естественная идемпотентность. Событие несёт итоговый снимок remaining,
     * поэтому применение balance = remaining (UPSERT) не зависит от того, сколько раз
     * доставлено это событие: повторное применение того же снимка — no-op по значению.
     * Хранилище обработанных ключей НЕ требуется.
     * <p>
     * Важно: при at-least-once дубли приходят в порядке (сразу за оригиналом), поэтому
     * снимок безопасен. Если бы доставка ещё и переупорядочивала события, для «последний
     * побеждает» понадобился бы монотонный номер версии в WHERE (здесь не нужен).
     */
    static long runNatural(Connection db, List<Event> deliveries) throws SQLException {
        try (PreparedStatement ps = db.prepareStatement(
                "INSERT INTO balances(variant, tank, balance) VALUES('c', ?, ?) " +
                "ON CONFLICT (variant, tank) DO UPDATE SET balance = EXCLUDED.balance")) {
            for (Event e : deliveries) {
                ps.setString(1, TANK);
                ps.setLong(2, e.remaining());
                ps.executeUpdate();
            }
        }
        return readBalance(db, "c");
    }

    static long readBalance(Connection db, String variant) throws SQLException {
        try (PreparedStatement ps = db.prepareStatement(
                "SELECT balance FROM balances WHERE variant=? AND tank=?")) {
            ps.setString(1, variant);
            ps.setString(2, TANK);
            ResultSet rs = ps.executeQuery();
            if (!rs.next()) {
                throw new SQLException("нет строки баланса для variant=" + variant);
            }
            return rs.getLong(1);
        }
    }

    static void assertTrue(boolean cond, String msg) {
        if (!cond) {
            System.err.println("АССЕРТ провален: " + msg);
            System.exit(1);
        }
    }

    private Main() {
    }
}
