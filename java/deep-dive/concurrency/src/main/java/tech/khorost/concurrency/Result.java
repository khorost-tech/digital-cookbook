package tech.khorost.concurrency;

import java.io.IOException;
import java.lang.management.ManagementFactory;
import java.lang.management.MemoryMXBean;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Arrays;
import java.util.Locale;

/**
 * Собранные метрики одного прогона: N задач, суммарное wall-clock время,
 * латентность каждой задачи (submit -> completion, наносекунды).
 * Печатает отчёт в формате "ключ=значение" на stdout — построчно, чтобы
 * run-all.sh мог парсить вывод простым grep-ом.
 */
final class Result {

    private final String mode;
    private final int n;
    private final long wallNanos;
    private final long[] latenciesNanos;

    Result(String mode, int n, long wallNanos, long[] latenciesNanos) {
        this.mode = mode;
        this.n = n;
        this.wallNanos = wallNanos;
        this.latenciesNanos = latenciesNanos;
    }

    void printReport() {
        long[] sorted = latenciesNanos.clone();
        Arrays.sort(sorted);

        double wallSeconds = wallNanos / 1_000_000_000.0;
        double throughput = n / wallSeconds;

        double p50Ms = sorted[percentileIndex(sorted.length, 0.50)] / 1_000_000.0;
        double p99Ms = sorted[percentileIndex(sorted.length, 0.99)] / 1_000_000.0;
        double minMs = sorted[0] / 1_000_000.0;
        double maxMs = sorted[sorted.length - 1] / 1_000_000.0;

        long peakRssKb = readPeakRssKb();

        MemoryMXBean memBean = ManagementFactory.getMemoryMXBean();
        long heapUsedBytes = memBean.getHeapMemoryUsage().getUsed();
        long heapCommittedBytes = memBean.getHeapMemoryUsage().getCommitted();

        System.out.println("mode=" + mode);
        System.out.println("n=" + n);
        System.out.printf(Locale.ROOT, "wall_ms=%.1f%n", wallSeconds * 1000);
        System.out.printf(Locale.ROOT, "throughput_tasks_per_sec=%.1f%n", throughput);
        System.out.printf(Locale.ROOT, "latency_p50_ms=%.1f%n", p50Ms);
        System.out.printf(Locale.ROOT, "latency_p99_ms=%.1f%n", p99Ms);
        System.out.printf(Locale.ROOT, "latency_min_ms=%.1f%n", minMs);
        System.out.printf(Locale.ROOT, "latency_max_ms=%.1f%n", maxMs);
        System.out.println("peak_rss_kb=" + peakRssKb);
        System.out.printf(Locale.ROOT, "heap_used_mb=%.1f%n", heapUsedBytes / 1024.0 / 1024.0);
        System.out.printf(Locale.ROOT, "heap_committed_mb=%.1f%n", heapCommittedBytes / 1024.0 / 1024.0);
    }

    private static int percentileIndex(int length, double p) {
        int idx = (int) Math.ceil(p * length) - 1;
        return Math.max(0, Math.min(length - 1, idx));
    }

    /**
     * Peak RSS процесса из /proc/self/status (VmHWM — "High Water Mark",
     * исторический пик, не текущее значение). Linux-only (контейнер — Ubuntu),
     * что и требуется: все три режима меряются в одинаковом Docker-окружении.
     */
    private static long readPeakRssKb() {
        Path status = Path.of("/proc/self/status");
        try {
            for (String line : Files.readAllLines(status)) {
                if (line.startsWith("VmHWM:")) {
                    String[] parts = line.trim().split("\\s+");
                    return Long.parseLong(parts[1]);
                }
            }
        } catch (IOException e) {
            // не Linux / нет /proc — вернём -1, run-all.sh это учитывает
        }
        return -1;
    }
}
