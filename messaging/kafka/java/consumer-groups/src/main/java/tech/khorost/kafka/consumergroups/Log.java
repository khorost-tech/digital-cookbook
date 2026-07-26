package tech.khorost.kafka.consumergroups;

/**
 * Лог с меткой времени от старта сценария — упорядочивает события ребаланса
 * из разных потоков-консьюмеров в читаемую хронологию (аналог common.go
 * в Go-версии стенда).
 */
public final class Log {
    private static volatile long t0 = System.nanoTime();

    private Log() {
    }

    public static synchronized void startClock() {
        t0 = System.nanoTime();
    }

    public static synchronized void f(String format, Object... args) {
        long elapsedMs = (System.nanoTime() - t0) / 1_000_000;
        System.out.printf("[%8s] %s%n", elapsedMs + "ms", String.format(format, args));
    }
}
