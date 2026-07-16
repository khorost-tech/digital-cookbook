package tech.khorost.redis.ops;

import io.lettuce.core.RedisClient;
import io.lettuce.core.RedisURI;
import io.lettuce.core.api.StatefulRedisConnection;
import io.lettuce.core.support.ConnectionPoolSupport;
import org.apache.commons.pool2.impl.GenericObjectPool;
import org.apache.commons.pool2.impl.GenericObjectPoolConfig;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.NoSuchElementException;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;

/**
 * Стенд #7: Java-зеркало Go-сценария pool-sizing (ops-stand/go/main.go) —
 * тот же контеншн на пуле соединений, только клиент Lettuce +
 * GenericObjectPool&lt;StatefulRedisConnection&lt;String,String&gt;&gt; из
 * commons-pool2 (Lettuce сам по себе мультиплексирует команды на одном
 * соединении и пула в привычном смысле не требует — GenericObjectPool здесь
 * используется намеренно, чтобы получить сравнимый с go-redis сценарий
 * "N соединений на конкурентную нагрузку", а не потому что это
 * рекомендуемый Lettuce-идиом для продакшна).
 *
 * <p>Методология — зеркало Go-стороны, читать обоснование там же:
 * <ul>
 *   <li>{@code -pool-timeout-conf} (здесь: {@code OPS_POOL_TIMEOUT_MS}) —
 *   явный и одинаковый для всех размеров пула {@code maxWait}
 *   ({@link GenericObjectPoolConfig#setMaxWait}) — аналог go-redis
 *   {@code PoolTimeout}.</li>
 *   <li>Таймаут ожидания свободного соединения ловится ОТДЕЛЬНО от ошибки
 *   самой команды и ОТДЕЛЬНО от прочих сбоев борроу: {@code
 *   pool.borrowObject()} считается исчерпанием пула ТОЛЬКО при
 *   {@link NoSuchElementException} (сигнал commons-pool2), любое другое
 *   исключение — фатально. Стенд НЕ полагается на допущение «сервер жив весь
 *   прогон»: оно ничем не обеспечено после warmup-пинга, и при широком
 *   {@code catch (Exception)} умерший на середине сервер отрисовался бы как
 *   аккуратный процент таймаутов с кодом возврата 0. Исключение из самой
 *   команды {@code SET} (после того как соединение уже получено) — тоже
 *   фатально, как и в Go-версии.</li>
 *   <li>Несмещённая средняя латентность попытки — Σ(elapsed потока) /
 *   Σ(attempts), а НЕ overallElapsed/attempts (потоки работают параллельно,
 *   см. комментарий у runPoolSize в Go-версии — тот же аргумент применим
 *   один в один и здесь).</li>
 *   <li>Перцентили — только по успешным операциям, таймауты пула в них не
 *   подмешиваются, считаются отдельно.</li>
 * </ul>
 *
 * <p>Адрес Redis/Valkey — {@code REDIS_ADDR}, по умолчанию
 * {@code 127.0.0.1:6379} (тот же контракт, что и во всех Go-стендах).
 */
public final class App {

    private App() {
    }

    public static void main(String[] args) throws Exception {
        String addr = env("REDIS_ADDR", "127.0.0.1:6379");
        int concurrency = Integer.parseInt(env("OPS_CONCURRENCY", "50"));
        int opsPerThread = Integer.parseInt(env("OPS_PER_THREAD", "200"));
        long poolTimeoutMs = Long.parseLong(env("OPS_POOL_TIMEOUT_MS", "2"));
        int[] poolSizes = Arrays.stream(env("OPS_POOL_SIZES", "5,20,50").split(","))
                .map(String::trim)
                .mapToInt(Integer::parseInt)
                .toArray();

        // OPS_SWAP_ORDER — зеркало флага -swap-order Go-версии: тот же набор
        // арм в обратном порядке, чтобы отделить эффект размера пула от
        // эффекта порядка арм. См. poolSizing() в ops-stand/go/main.go.
        boolean swapOrder = Boolean.parseBoolean(env("OPS_SWAP_ORDER", "false"));
        String order = "прямой (по возрастанию размера)";
        if (swapOrder) {
            for (int i = 0, j = poolSizes.length - 1; i < j; i++, j--) {
                int t = poolSizes[i];
                poolSizes[i] = poolSizes[j];
                poolSizes[j] = t;
            }
            order = "обратный (OPS_SWAP_ORDER=true)";
        }

        System.out.printf(
                "=== pool-sizing (Java/Lettuce) === concurrency=%d ops/thread=%d pool-timeout=%dms sizes=%s порядок=%s%n",
                concurrency, opsPerThread, poolTimeoutMs, Arrays.toString(poolSizes), order);

        RedisURI uri = uri(addr);
        RedisClient client = RedisClient.create(uri);
        try {
            for (int size : poolSizes) {
                runPoolSize(client, size, concurrency, opsPerThread, Duration.ofMillis(poolTimeoutMs));
            }
        } finally {
            client.shutdown();
        }
    }

    /** То, что один поток накопил за свои opsPerThread операций. */
    private record ThreadReport(long elapsedNanos, int attempts, int timeouts, long[] succLatNanos) {
    }

    private static void runPoolSize(RedisClient client, int poolSize, int concurrency, int opsPerThread,
                                     Duration poolTimeout) throws InterruptedException {
        GenericObjectPoolConfig<StatefulRedisConnection<String, String>> config = new GenericObjectPoolConfig<>();
        config.setMaxTotal(poolSize);
        config.setMaxIdle(poolSize);
        config.setMinIdle(0);
        config.setBlockWhenExhausted(true);
        config.setMaxWait(poolTimeout);
        config.setTestOnBorrow(false);
        config.setTestOnReturn(false);
        config.setTestWhileIdle(false);

        GenericObjectPool<StatefulRedisConnection<String, String>> pool =
                ConnectionPoolSupport.createGenericObjectPool(client::connect, config);

        // ConnectionPoolSupport.createGenericObjectPool(supplier, config) по
        // умолчанию отдаёт ОБЁРНУТЫЕ соединения: close() у обёртки САМ
        // возвращает объект в пул (см. официальный wiki Lettuce,
        // Connection-Pooling). Поэтому здесь — try-with-resources, и НИГДЕ
        // в этом классе НЕ вызывается pool.returnObject() явно: комбинация
        // "close() у обёрнутого + ручной returnObject()" даёт
        // IllegalStateException "Object has already been returned to this
        // pool or is invalid" (поймано живьём на первой же попытке).
        // FLUSHDB перед КАЖДЫМ размером пула — условие сравнимости арм,
        // зеркало того же шага в Go-версии (см. подробное обоснование в
        // runPoolSize, ops-stand/go/main.go): без очистки первая арма создаёт
        // ключи с нуля, а следующие перезаписывают уже существующие, и армы
        // отличались бы не только размером пула.
        try (StatefulRedisConnection<String, String> warm = pool.borrowObject()) {
            warm.sync().ping();
            warm.sync().flushdb();
        } catch (Exception e) {
            throw new RuntimeException("pool-sizing (size=" + poolSize + "): warmup ping/flushdb: " + e, e);
        }

        ExecutorService executor = Executors.newFixedThreadPool(concurrency);
        CountDownLatch startGate = new CountDownLatch(1);
        List<Future<ThreadReport>> futures = new ArrayList<>(concurrency);

        for (int t = 0; t < concurrency; t++) {
            final int threadId = t;
            futures.add(executor.submit(() -> runThread(pool, poolSize, threadId, opsPerThread, startGate)));
        }

        Instant overallStart = Instant.now();
        startGate.countDown();

        List<ThreadReport> reports = new ArrayList<>(concurrency);
        for (Future<ThreadReport> f : futures) {
            try {
                reports.add(f.get());
            } catch (ExecutionException e) {
                executor.shutdownNow();
                pool.close();
                throw new RuntimeException(e.getCause() != null ? e.getCause() : e);
            }
        }
        Duration overallElapsed = Duration.between(overallStart, Instant.now());
        executor.shutdown();

        long totalAttempts = 0, totalTimeouts = 0, sumThreadElapsedNanos = 0;
        List<Long> allSuccLat = new ArrayList<>();
        for (ThreadReport r : reports) {
            totalAttempts += r.attempts();
            totalTimeouts += r.timeouts();
            sumThreadElapsedNanos += r.elapsedNanos();
            for (long d : r.succLatNanos()) {
                allSuccLat.add(d);
            }
        }
        long succeeded = totalAttempts - totalTimeouts;
        double meanPerAttemptMicros = totalAttempts == 0 ? 0.0
                : (sumThreadElapsedNanos / 1000.0) / totalAttempts;
        double throughput = totalAttempts / (overallElapsed.toNanos() / 1_000_000_000.0);
        double timeoutPct = totalAttempts == 0 ? 0.0 : 100.0 * totalTimeouts / totalAttempts;

        System.out.printf(
                "pool-sizing size=%d: concurrency=%d ops/thread=%d pool-timeout-conf=%dms attempts=%d succeeded=%d pool_timeouts=%d (%.1f%%)%n",
                poolSize, concurrency, opsPerThread, poolTimeout.toMillis(), totalAttempts, succeeded, totalTimeouts, timeoutPct);
        System.out.printf(
                "pool-sizing size=%d: overall_elapsed=%dms throughput=%.1f ops/s | СРЕДНЯЯ латентность попытки (несмещённая, Sum(elapsed потока)/Sum(attempts))=%.3fms%n",
                poolSize, overallElapsed.toMillis(), throughput, meanPerAttemptMicros / 1000.0);

        allSuccLat.sort(Long::compareTo);
        System.out.printf(
                "pool-sizing size=%d: p50=%.3fms p95=%.3fms p99=%.3fms — по %d успешным операциям (таймауты пула сюда НЕ подмешаны, считаются отдельно строкой выше)%n",
                poolSize,
                percentileMs(allSuccLat, 0.50), percentileMs(allSuccLat, 0.95), percentileMs(allSuccLat, 0.99),
                allSuccLat.size());

        pool.close();
    }

    private static ThreadReport runThread(GenericObjectPool<StatefulRedisConnection<String, String>> pool,
                                           int poolSize, int threadId, int opsPerThread,
                                           CountDownLatch startGate) throws InterruptedException {
        startGate.await();
        long gStart = System.nanoTime();
        int attempts = 0;
        int timeouts = 0;
        long[] succLat = new long[opsPerThread];
        int succCount = 0;
        for (int i = 0; i < opsPerThread; i++) {
            String key = "ops:pool:g" + threadId + ":i" + i;
            long t0 = System.nanoTime();
            StatefulRedisConnection<String, String> conn;
            try {
                conn = pool.borrowObject();
            } catch (NoSuchElementException e) {
                // ТОЛЬКО исчерпание пула. commons-pool2 сигнализирует его
                // строго NoSuchElementException("Timeout waiting for idle
                // object, maxWaitDuration=...") — проверено живьём, а не взято
                // из сигнатуры (она объявляет широкий `throws Exception`).
                attempts++;
                timeouts++;
                continue;
            } catch (Exception e) {
                // Всё остальное — настоящий сбой, а не измеряемый эффект.
                // Это НЕ формальность: первая редакция стенда ловила здесь
                // `Exception` целиком, и тогда мёртвый сервер, сбой создания
                // соединения или OOM посреди прогона отрисовались бы как
                // аккуратные "81% pool timeout" с кодом возврата 0 — то есть
                // провалившийся прогон выглядел бы как честный результат.
                // Проверено живьём, что эти случаи РАЗЛИЧИМЫ: борроу против
                // заведомо мёртвого сервера (порт без слушателя) бросает
                // io.lettuce.core.RedisConnectionException, а не
                // NoSuchElementException — то есть узкий catch выше их не
                // проглотит, и они дойдут сюда и уронят прогон.
                throw new RuntimeException(
                        "pool-sizing (size=" + poolSize + ") thread=" + threadId + " i=" + i
                                + ": borrowObject упал НЕ по исчерпанию пула (" + e.getClass().getName() + "): " + e, e);
            }
            // try-with-resources: соединение обёрнутое (см. комментарий в
            // runPoolSize про warmup) — close() возвращает его в пул сам,
            // без pool.returnObject().
            try (conn) {
                conn.sync().set(key, "v");
                succLat[succCount++] = System.nanoTime() - t0;
            } catch (Exception e) {
                throw new RuntimeException(
                        "pool-sizing (size=" + poolSize + ") thread=" + threadId + " i=" + i + ": ошибка записи: " + e, e);
            }
            attempts++;
        }
        long elapsed = System.nanoTime() - gStart;
        return new ThreadReport(elapsed, attempts, timeouts, Arrays.copyOf(succLat, succCount));
    }

    private static double percentileMs(List<Long> sortedNanos, double p) {
        if (sortedNanos.isEmpty()) {
            return 0.0;
        }
        int idx = (int) Math.ceil(p * sortedNanos.size()) - 1;
        idx = Math.max(0, Math.min(idx, sortedNanos.size() - 1));
        return sortedNanos.get(idx) / 1_000_000.0;
    }

    private static RedisURI uri(String hostPort) {
        String[] hp = hostPort.split(":");
        return RedisURI.create(hp[0], Integer.parseInt(hp[1]));
    }

    private static String env(String key, String def) {
        String v = System.getenv(key);
        return (v == null || v.isBlank()) ? def : v;
    }
}
