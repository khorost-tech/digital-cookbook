package tech.khorost.dataaccess;

import com.zaxxer.hikari.HikariConfig;
import com.zaxxer.hikari.HikariDataSource;
import com.zaxxer.hikari.HikariPoolMXBean;

/**
 * Общий HikariCP-пул для JDBC- и jOOQ-демо + отдельная демонстрация метрик под
 * конкурентной нагрузкой (пункт 4 задачи). Пул нарочно маленький (maximumPoolSize=4),
 * чтобы конкурентный сценарий в {@link #printMetricsUnderLoad()} реально упирался
 * в лимит и показывал active=total, threadsAwaitingConnection>0 — иначе метрики
 * были бы скучным "всё idle".
 */
public final class HikariManager {
    public static final int MAX_POOL_SIZE = 4;
    public static final int MIN_IDLE = 1;

    private static final HikariDataSource DS = build();

    private HikariManager() {
    }

    private static HikariDataSource build() {
        HikariConfig cfg = new HikariConfig();
        cfg.setJdbcUrl(Db.URL);
        cfg.setUsername(Db.USER);
        cfg.setPassword(Db.PASSWORD);
        cfg.setPoolName("data-access-pool");
        cfg.setMaximumPoolSize(MAX_POOL_SIZE);
        cfg.setMinimumIdle(MIN_IDLE);
        cfg.setConnectionTimeout(5_000);
        return new HikariDataSource(cfg);
    }

    public static HikariDataSource dataSource() {
        return DS;
    }

    /** Печатает active/idle/total/waiting в один поток — снимок состояния пула. */
    public static void printMetrics(String label) {
        HikariPoolMXBean mx = DS.getHikariPoolMXBean();
        System.out.printf(
                "  [%s] total=%d active=%d idle=%d threadsAwaitingConnection=%d%n",
                label, mx.getTotalConnections(), mx.getActiveConnections(),
                mx.getIdleConnections(), mx.getThreadsAwaitingConnection());
    }

    /**
     * Демонстрация: maximumPoolSize={@value MAX_POOL_SIZE}, запускаем 8 потоков,
     * каждый держит соединение ~600мс — пул исчерпывается, часть потоков ждёт
     * в очереди. Метрики снимаются ДО, ВО ВРЕМЯ (пик нагрузки) и ПОСЛЕ.
     */
    public static void printMetricsUnderLoad() throws InterruptedException {
        int workers = 8;
        System.out.printf("HikariCP: maximumPoolSize=%d, minimumIdle=%d, %d конкурентных воркеров%n",
                MAX_POOL_SIZE, MIN_IDLE, workers);
        printMetrics("до нагрузки  ");

        Thread[] threads = new Thread[workers];
        for (int i = 0; i < workers; i++) {
            threads[i] = new Thread(() -> {
                try (var conn = DS.getConnection()) {
                    try (var st = conn.createStatement()) {
                        st.execute("SELECT pg_sleep(0.6)");
                    }
                } catch (Exception e) {
                    System.err.println("worker error: " + e.getMessage());
                }
            });
        }
        for (Thread t : threads) {
            t.start();
        }
        // Даём воркерам время захватить соединения (пул уйдёт в насыщение).
        Thread.sleep(200);
        printMetrics("под нагрузкой");

        for (Thread t : threads) {
            t.join();
        }
        printMetrics("после        ");
    }

    public static void close() {
        DS.close();
    }
}
