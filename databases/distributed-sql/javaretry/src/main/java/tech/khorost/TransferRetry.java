package tech.khorost;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.ThreadLocalRandom;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * Переводы между двумя счетами из нескольких потоков под SERIALIZABLE.
 *
 * <p>Все потоки бьют в одни и те же две строки, поэтому конфликты гарантированы.
 * Движок разрешает их, обрывая одну из транзакций с SQLSTATE 40001, и
 * повторить её — дело приложения. Драйвер об этом ничего не знает: тот же
 * код против одиночного PostgreSQL под READ COMMITTED повторов не видел бы.
 *
 * <p>Два режима:
 * <ul>
 *   <li>{@code update} — {@code UPDATE ... SET balance = balance - ?}. Запись без
 *       предварительного чтения: движок берёт блокировку строки, и конкурирующие
 *       транзакции встают в очередь, а не обрываются;</li>
 *   <li>{@code rmw} — read-modify-write: прочитали балансы, посчитали в приложении,
 *       записали. Типичный код бизнес-логики, и именно он порождает 40001.</li>
 * </ul>
 *
 * <p>Запуск: {@code TransferRetry crdb|yb update|rmw [потоков] [переводов на поток]}.
 */
public final class TransferRetry {

    /**
     * Ретраибельные коды. 40001 — serialization failure, его ждут все. 40P01 —
     * deadlock: им YugabyteDB под SERIALIZABLE сообщает о конфликте, и цикл
     * «только по 40001» на нём падает — первая версия этого кода так и падала.
     */
    private static final java.util.Set<String> RETRY_SQLSTATES = java.util.Set.of("40001", "40P01");
    private static final java.util.concurrent.ConcurrentHashMap<String, AtomicInteger> BY_CODE =
            new java.util.concurrent.ConcurrentHashMap<>();
    private static final int MAX_ATTEMPTS = 20;

    public static void main(String[] args) throws Exception {
        String engine = args.length > 0 ? args[0] : "crdb";
        String mode = args.length > 1 ? args[1] : "rmw";
        int threads = args.length > 2 ? Integer.parseInt(args[2]) : 4;
        int perThread = args.length > 3 ? Integer.parseInt(args[3]) : 100;

        String url;
        String user;
        if (engine.equals("yb")) {
            url = "jdbc:postgresql://yb1:5433/yugabyte";
            user = "yugabyte";
        } else {
            url = "jdbc:postgresql://crdb1:26257/defaultdb?sslmode=disable";
            user = "root";
        }

        try (Connection c = DriverManager.getConnection(url, user, null);
             Statement st = c.createStatement()) {
            st.execute("DROP TABLE IF EXISTS jaccounts");
            st.execute("CREATE TABLE jaccounts (id INT PRIMARY KEY, balance BIGINT NOT NULL)");
            st.execute("INSERT INTO jaccounts VALUES (1, 1000000), (2, 1000000)");
        }

        AtomicInteger ok = new AtomicInteger();
        AtomicInteger gaveUp = new AtomicInteger();
        AtomicInteger aborted = new AtomicInteger();
        AtomicInteger unknown = new AtomicInteger();
        AtomicInteger notStarted = new AtomicInteger();
        AtomicInteger failed = new AtomicInteger();
        AtomicInteger maxAttempts = new AtomicInteger();

        long started = System.nanoTime();
        // Лимит на весь прогон. Останавливают его сами рабочие потоки: проверяют
        // дедлайн перед каждой попыткой, а у каждого запроса таймаут не дальше
        // дедлайна. shutdownNow() для этого не годится — он лишь просит потоки
        // остановиться, а JDBC-вызов на прерывание не реагирует.
        long deadline = started + TimeUnit.MINUTES.toNanos(LIMIT_MINUTES);
        ExecutorService pool = Executors.newFixedThreadPool(threads);
        for (int t = 0; t < threads; t++) {
            pool.submit(() -> {
                try (Connection c = DriverManager.getConnection(url, user, null)) {
                    c.setAutoCommit(false);
                    c.setTransactionIsolation(Connection.TRANSACTION_SERIALIZABLE);
                    int i = 0;
                    try {
                        for (; i < perThread; i++) {
                            if (System.nanoTime() >= deadline) {
                                notStarted.addAndGet(perThread - i);
                                break;
                            }
                            int attempts = transferWithRetry(c, mode, deadline);
                            if (attempts == ROLLED_BACK) {
                                aborted.incrementAndGet();
                            } else if (attempts == UNKNOWN) {
                                // Соединение после сетевого таймаута непригодно —
                                // поток заканчивает, остаток не начат.
                                unknown.incrementAndGet();
                                notStarted.addAndGet(perThread - i - 1);
                                break;
                            } else if (attempts == GAVE_UP) {
                                gaveUp.incrementAndGet();
                            } else {
                                ok.incrementAndGet();
                                maxAttempts.accumulateAndGet(attempts, Math::max);
                            }
                        }
                    } catch (SQLException e) {
                        // Поток упал на неретраибельной ошибке: текущий и оставшиеся
                        // переводы учитываются явно, чтобы итог сходился к 100%.
                        System.out.println("поток упал: " + e.getSQLState() + " " + e.getMessage());
                        failed.incrementAndGet();
                        notStarted.addAndGet(perThread - i - 1);
                    }
                } catch (SQLException e) {
                    System.out.println("не удалось подключиться: " + e.getSQLState() + " " + e.getMessage());
                    notStarted.addAndGet(perThread);
                }
                return null;
            });
        }
        pool.shutdown();
        // Ждём, пока все потоки действительно закончатся: итоги печатаются только
        // после этого. Запас сверх лимита — на таймаут последнего запроса и откат.
        if (!pool.awaitTermination(LIMIT_MINUTES + 2, TimeUnit.MINUTES)) {
            System.out.println("ОШИБКА: потоки не остановились после дедлайна, итоги недостоверны");
            System.exit(2);
        }
        boolean cut = aborted.get() + unknown.get() + notStarted.get() > 0;

        long sum;
        try (Connection c = DriverManager.getConnection(url, user, null);
             Statement st = c.createStatement();
             ResultSet rs = st.executeQuery("SELECT sum(balance) FROM jaccounts")) {
            rs.next();
            sum = rs.getLong(1);
        }
        System.out.printf("engine=%s режим=%s потоков=%d переводов=%d%n", engine, mode, threads, threads * perThread);
        System.out.printf("заняло=%.1f с%s%n", (System.nanoTime() - started) / 1e9,
                cut ? String.format(" — ОСТАНОВЛЕНО по лимиту %d мин, все потоки завершены", LIMIT_MINUTES) : "");
        System.out.printf("успешно=%d  сдались=%d  оборваны лимитом (не применены)=%d  исход неизвестен=%d  упали с ошибкой=%d  не начаты=%d%n",
                ok.get(), gaveUp.get(), aborted.get(), unknown.get(), failed.get(), notStarted.get());
        System.out.printf("повторов всего (включая оборванные)=%d  максимум попыток среди успешных=%d%n",
                RETRIES.get(), maxAttempts.get());
        System.out.printf("ошибки по кодам: %s%n", new java.util.TreeMap<>(BY_CODE));
        System.out.printf("сумма балансов=%d (ожидается 2000000)%n", sum);
    }

    private static final int GAVE_UP = -1;
    /**
     * Оборван лимитом и заведомо не применён: коммит либо не отправлялся, либо
     * база явно сообщила об откате (40001 / 40P01, в том числе на commit()).
     */
    private static final int ROLLED_BACK = -2;
    /** Ошибка во время или после отправки коммита — мог примениться, мог нет. */
    private static final int UNKNOWN = -3;
    /** Исполнитель для setNetworkTimeout: прерывает зависший commit(). */
    private static final ExecutorService NET = Executors.newCachedThreadPool(r -> {
        Thread t = new Thread(r, "net-timeout");
        t.setDaemon(true);
        return t;
    });
    private static final int LIMIT_MINUTES = 10;
    /** Повторы считаются в момент ошибки — так в итог попадают и оборванные переводы. */
    private static final AtomicInteger RETRIES = new AtomicInteger();

    /**
     * Возвращает число попыток; GAVE_UP — исчерпан предел попыток; ROLLED_BACK —
     * оборван лимитом и заведомо не применён; UNKNOWN — ошибка во время или после
     * отправки коммита, исход неизвестен.
     */
    private static int transferWithRetry(Connection c, String mode, long deadline) throws SQLException {
        long amount = ThreadLocalRandom.current().nextLong(1, 100);
        for (int attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
            boolean commitSent = false;
            try {
                if (mode.equals("update")) {
                    exec(c, "UPDATE jaccounts SET balance = balance - ? WHERE id = 1", amount, deadline);
                    exec(c, "UPDATE jaccounts SET balance = balance + ? WHERE id = 2", amount, deadline);
                } else {
                    long from = balance(c, 1, deadline);
                    long to = balance(c, 2, deadline);
                    exec(c, "UPDATE jaccounts SET balance = ? WHERE id = 1", from - amount, deadline);
                    exec(c, "UPDATE jaccounts SET balance = ? WHERE id = 2", to + amount, deadline);
                }
                // У commit() нет таймаута оператора, поэтому его ограничивает сетевой
                // таймаут соединения — тоже не дальше дедлайна.
                c.setNetworkTimeout(NET, (int) Math.max(1, leftMillis(deadline)));
                commitSent = true;
                c.commit();
                c.setNetworkTimeout(NET, 0);
                return attempt;
            } catch (DeadlineReached d) {
                // Дедлайн наступил до отправки коммита: транзакция не могла
                // закоммититься, перевод не применён.
                rollbackQuietly(c);
                return ROLLED_BACK;
            } catch (SQLException e) {
                String state = e.getSQLState();
                boolean retry = RETRY_SQLSTATES.contains(state);
                if (commitSent && !retry) {
                    // Ошибка во время или после отправки коммита, и это не отказ
                    // сериализации: ответ мог потеряться после успешного коммита.
                    // Откатом это назвать нельзя — исход неизвестен.
                    return UNKNOWN;
                }
                rollbackQuietly(c);
                if (!retry) {
                    // 57014 — оператор отменён по таймауту, а таймаут не дальше
                    // дедлайна; коммит при этом не отправлялся.
                    if ("57014".equals(state) || System.nanoTime() >= deadline) {
                        return ROLLED_BACK;
                    }
                    throw e;
                }
                // 40001 / 40P01 — движок сам оборвал транзакцию, в том числе на
                // коммите: она точно не применена, её можно повторить.
                BY_CODE.computeIfAbsent(state, k -> new AtomicInteger()).incrementAndGet();
                if (System.nanoTime() >= deadline) {
                    return ROLLED_BACK;
                }
                if (attempt < MAX_ATTEMPTS) {
                    RETRIES.incrementAndGet();
                }
                // Экспоненциальная пауза с джиттером: без неё потоки
                // синхронно столкнутся снова на следующей же попытке.
                sleepQuietly(ThreadLocalRandom.current().nextLong(1, 1L << Math.min(attempt, 8)));
            }
        }
        return GAVE_UP;
    }

    /** Дедлайн наступил до очередного оператора. */
    private static final class DeadlineReached extends RuntimeException {
        DeadlineReached() {
            super(null, null, false, false);
        }
    }

    private static long leftMillis(long deadline) {
        return TimeUnit.NANOSECONDS.toMillis(deadline - System.nanoTime());
    }

    /** Остаток до дедлайна в секундах, округлённый вверх; пересчитывается перед каждым оператором. */
    private static int leftSeconds(long deadline) {
        long left = deadline - System.nanoTime();
        if (left <= 0) {
            throw new DeadlineReached();
        }
        return (int) Math.max(1, (left + 999_999_999L) / 1_000_000_000L);
    }

    private static void rollbackQuietly(Connection c) {
        try {
            c.rollback();
        } catch (SQLException ignored) {
            // соединение после таймаута может быть уже без транзакции
        }
    }

    private static long balance(Connection c, int id, long deadline) throws SQLException {
        try (PreparedStatement ps = c.prepareStatement("SELECT balance FROM jaccounts WHERE id = ?")) {
            ps.setQueryTimeout(leftSeconds(deadline));
            ps.setInt(1, id);
            try (ResultSet rs = ps.executeQuery()) {
                rs.next();
                return rs.getLong(1);
            }
        }
    }

    private static void exec(Connection c, String sql, long v, long deadline) throws SQLException {
        try (PreparedStatement ps = c.prepareStatement(sql)) {
            ps.setQueryTimeout(leftSeconds(deadline));
            ps.setLong(1, v);
            ps.executeUpdate();
        }
    }

    private static void sleepQuietly(long ms) {
        try {
            Thread.sleep(ms);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
