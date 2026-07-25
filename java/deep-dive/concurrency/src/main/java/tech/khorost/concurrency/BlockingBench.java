package tech.khorost.concurrency;

import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;

/**
 * Общий харнесс для блокирующих моделей (virtual threads, platform threads):
 * N задач, каждая — Thread.sleep(sleepMs) как заглушка I/O. Submit не блокирует
 * (у обоих типов ExecutorService в этом стенде очередь без backpressure), так
 * что "latency" каждой задачи = время в очереди/на исполнении, честно ловит
 * контраст между VT (без очереди) и platform-пулом (очередь при K << N).
 */
final class BlockingBench {

    private BlockingBench() {
    }

    static Result run(String mode, ExecutorService executor, int n, int sleepMs) throws InterruptedException {
        long[] latenciesNanos = new long[n];
        CountDownLatch latch = new CountDownLatch(n);

        long wallStart = System.nanoTime();
        for (int i = 0; i < n; i++) {
            int idx = i;
            long submitTime = System.nanoTime();
            executor.submit(() -> {
                try {
                    Thread.sleep(sleepMs);
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                } finally {
                    latenciesNanos[idx] = System.nanoTime() - submitTime;
                    latch.countDown();
                }
            });
        }
        latch.await();
        long wallNanos = System.nanoTime() - wallStart;

        executor.shutdown();

        return new Result(mode, n, wallNanos, latenciesNanos);
    }
}
