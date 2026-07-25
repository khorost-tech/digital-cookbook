package tech.khorost.productionpatterns;

import java.util.Random;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Заглушка внешней системы с двумя управляемыми фазами:
 *   1) "outage" [0, recoveryAfterMillis) — 100% вызовов падают (полный отказ downstream);
 *   2) "recovered" [recoveryAfterMillis, +inf) — вызовы в основном успешны, но с остаточными
 *      8% ошибок и сетевой задержкой (реалистичный "почти здоровый" сервис, а не идеальный).
 * Обе фазы имитируют задержку сети через Thread.sleep — так retry/backoff и bulkhead-
 * конкуренция ведут себя как на реальном I/O, а не мгновенно.
 */
public class UnstableDownstream {

    private final long startNanos = System.nanoTime();
    private final long recoveryAfterMillis;
    private final AtomicLong totalCalls = new AtomicLong();
    private final AtomicLong totalFailures = new AtomicLong();
    private final Random random = new Random();

    public UnstableDownstream(long recoveryAfterMillis) {
        this.recoveryAfterMillis = recoveryAfterMillis;
    }

    public String call() {
        long callNo = totalCalls.incrementAndGet();
        long elapsedMs = (System.nanoTime() - startNanos) / 1_000_000;
        boolean outagePhase = elapsedMs < recoveryAfterMillis;
        try {
            if (outagePhase) {
                Thread.sleep(30 + random.nextInt(70));
                totalFailures.incrementAndGet();
                throw new DownstreamException(
                        "downstream unavailable (call #" + callNo + ", outage, elapsed=" + elapsedMs + "ms)");
            }
            Thread.sleep(40 + random.nextInt(200));
            if (random.nextInt(100) < 8) {
                totalFailures.incrementAndGet();
                throw new DownstreamException(
                        "downstream transient error (call #" + callNo + ", recovered, elapsed=" + elapsedMs + "ms)");
            }
            return "OK#" + callNo;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new DownstreamException("interrupted while calling downstream", e);
        }
    }

    public long totalCalls() {
        return totalCalls.get();
    }

    public long totalFailures() {
        return totalFailures.get();
    }
}
