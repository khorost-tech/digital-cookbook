package tech.khorost.profiling;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Random;

/**
 * Нагружаемый "сервис" для стенда профилирования (JFR + async-profiler).
 *
 * <p>Каждая итерация основного цикла делает две вещи одновременно:
 * <ul>
 *   <li><b>горячий CPU-путь</b> — ручной FNV-1a хэш по буферу байт
 *       ({@link HashUtil#fnv1a}) + рекурсивная сортировка слиянием
 *       ({@link SortUtil#mergeSort}), чтобы у профайлера был конкретный
 *       "горячий" метод, а не JIT-заинлайненная тривиальщина;</li>
 *   <li><b>аллокации</b> — создание пачки {@link Order} (record с
 *       конкатенацией строк для id) на каждой итерации, плюс временные
 *       массивы merge sort. Пачка тут же становится мусором — это
 *       умышленно, чтобы генерировать давление на young-GC и увидеть
 *       реальные {@code jdk.GCPhasePause} в записи.</li>
 * </ul>
 *
 * <p>Запуск: {@code java -jar profiling.jar [durationSeconds]}
 * (по умолчанию 40 секунд).
 */
public final class Workload {

    private static final int HASH_BUF_SIZE = 8192;
    private static final int SORT_ARRAY_SIZE = 2000;
    private static final int ORDERS_PER_ITERATION = 500;

    private Workload() {
    }

    public static void main(String[] args) throws InterruptedException {
        int durationSeconds = args.length > 0 ? Integer.parseInt(args[0]) : 40;
        System.out.printf("profiling workload: duration=%ds%n", durationSeconds);

        Random rnd = new Random(42);
        byte[] hashBuf = new byte[HASH_BUF_SIZE];

        long hashAccumulator = 0L; // мешает JIT выкинуть "мёртвый" код
        long iterations = 0L;
        long ordersCreated = 0L;

        long startNanos = System.nanoTime();
        long endNanos = startNanos + durationSeconds * 1_000_000_000L;
        long lastReportNanos = startNanos;

        while (System.nanoTime() < endNanos) {
            // --- горячий CPU-путь ---
            rnd.nextBytes(hashBuf);
            hashAccumulator ^= HashUtil.fnv1a(hashBuf);

            int[] toSort = randomInts(rnd, SORT_ARRAY_SIZE);
            int[] sorted = SortUtil.mergeSort(toSort);
            hashAccumulator ^= sorted[0] ^ sorted[sorted.length - 1];

            // --- аллокации ---
            List<Order> batch = new ArrayList<>(ORDERS_PER_ITERATION);
            for (int i = 0; i < ORDERS_PER_ITERATION; i++) {
                batch.add(OrderFactory.create(iterations * ORDERS_PER_ITERATION + i, rnd));
            }
            ordersCreated += batch.size();
            hashAccumulator ^= batch.size();

            iterations++;

            long now = System.nanoTime();
            if (now - lastReportNanos >= 5_000_000_000L) {
                double elapsedSec = (now - startNanos) / 1e9;
                System.out.printf(
                        "t=%.1fs iterations=%d orders=%d hashAcc=%d%n",
                        elapsedSec, iterations, ordersCreated, hashAccumulator);
                lastReportNanos = now;
            }
        }

        double totalSec = (System.nanoTime() - startNanos) / 1e9;
        System.out.printf(
                "DONE: total=%.1fs iterations=%d orders=%d hashAcc=%d%n",
                totalSec, iterations, ordersCreated, hashAccumulator);
    }

    private static int[] randomInts(Random rnd, int size) {
        int[] arr = new int[size];
        for (int i = 0; i < size; i++) {
            arr[i] = rnd.nextInt();
        }
        return arr;
    }

    /** Заказ — типичный DTO, аллоцируется пачками и тут же становится мусором. */
    record Order(String id, double amount, Instant ts) {
    }

    static final class OrderFactory {
        private OrderFactory() {
        }

        static Order create(long seq, Random rnd) {
            // Конкатенация строк умышленно (не StringBuilder явно) —
            // типичный "случайный" источник аллокаций в прикладном коде.
            String id = "order-" + seq + "-" + rnd.nextInt(1_000_000);
            double amount = rnd.nextDouble() * 10_000;
            return new Order(id, amount, Instant.now());
        }
    }

    /** Ручной FNV-1a — намеренно не System.arraycopy/JDK-хэш, чтобы профайлер увидел наш метод. */
    static final class HashUtil {
        private static final long FNV_OFFSET_BASIS = 0xcbf29ce484222325L;
        private static final long FNV_PRIME = 0x100000001b3L;

        private HashUtil() {
        }

        static long fnv1a(byte[] data) {
            long hash = FNV_OFFSET_BASIS;
            for (byte b : data) {
                hash ^= (b & 0xff);
                hash *= FNV_PRIME;
            }
            return hash;
        }
    }

    /** Рекурсивный merge sort (не Arrays.sort) — CPU + временные аллокации на каждом merge. */
    static final class SortUtil {
        private SortUtil() {
        }

        static int[] mergeSort(int[] input) {
            if (input.length <= 1) {
                return input;
            }
            int mid = input.length / 2;
            int[] left = mergeSort(java.util.Arrays.copyOfRange(input, 0, mid));
            int[] right = mergeSort(java.util.Arrays.copyOfRange(input, mid, input.length));
            return merge(left, right);
        }

        private static int[] merge(int[] left, int[] right) {
            int[] result = new int[left.length + right.length];
            int i = 0;
            int j = 0;
            int k = 0;
            while (i < left.length && j < right.length) {
                result[k++] = (left[i] <= right[j]) ? left[i++] : right[j++];
            }
            while (i < left.length) {
                result[k++] = left[i++];
            }
            while (j < right.length) {
                result[k++] = right[j++];
            }
            return result;
        }
    }
}
