package tech.khorost.productionpatterns;

import io.github.resilience4j.bulkhead.BulkheadFullException;
import io.github.resilience4j.circuitbreaker.CallNotPermittedException;
import io.github.resilience4j.ratelimiter.RequestNotPermitted;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.time.Instant;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ScheduledFuture;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;
import java.util.function.Supplier;

/**
 * Демо: нагрузка на нестабильный downstream через resilience4j-стек, свой health-сервер,
 * и graceful shutdown по SIGTERM с дренажом in-flight задач.
 *
 * Аргументы (все опциональны):
 *   [0] recoveryAfterMillis  — сколько downstream "лежит" перед восстановлением (default 5000)
 *   [1] healthPort           — порт liveness/readiness (default 8080)
 *   [2] submitIntervalMillis — период запуска новых задач (default 150)
 */
public class Main {

    private static final Logger log = LoggerFactory.getLogger("Main");

    public static void main(String[] args) throws Exception {
        long recoveryAfterMillis = args.length > 0 ? Long.parseLong(args[0]) : 5000;
        int healthPort = args.length > 1 ? Integer.parseInt(args[1]) : 8080;
        long submitIntervalMillis = args.length > 2 ? Long.parseLong(args[2]) : 150;

        UnstableDownstream downstream = new UnstableDownstream(recoveryAfterMillis);
        ResilienceStack resilience = new ResilienceStack();
        HealthServer health = new HealthServer(healthPort);
        health.start();

        ExecutorService workExecutor = Executors.newFixedThreadPool(8, r -> new Thread(r, "work"));
        ScheduledExecutorService scheduler = Executors.newSingleThreadScheduledExecutor(r -> new Thread(r, "scheduler"));

        AtomicBoolean acceptingNewWork = new AtomicBoolean(true);
        AtomicLong submitted = new AtomicLong();
        AtomicLong succeeded = new AtomicLong();
        AtomicLong failedFinal = new AtomicLong();
        AtomicLong cbRejected = new AtomicLong();
        AtomicLong rlRejected = new AtomicLong();
        AtomicLong bhRejected = new AtomicLong();

        Runnable submitTask = () -> {
            if (!acceptingNewWork.get()) {
                return;
            }
            long id = submitted.incrementAndGet();
            workExecutor.submit(() -> {
                // taskId в ResilienceStack.currentTaskId (ThreadLocal) — виден внутри
                // onRetry (тот же поток, decorated.get() выполняется синхронно), так строки
                // "[Retry] [task-N] ..." тоже атрибутируются к задаче, а не висят без
                // привязки к "пути одного запроса".
                resilience.setCurrentTaskId(String.valueOf(id));
                Instant start = Instant.now();
                log.info("[task-{}] start", id);
                try {
                    Supplier<String> decorated = resilience.decorate(downstream::call);
                    try {
                        String result = decorated.get();
                        succeeded.incrementAndGet();
                        log.info("[task-{}] OK: {} ({}ms)", id, result, Duration.between(start, Instant.now()).toMillis());
                    } catch (CallNotPermittedException e) {
                        cbRejected.incrementAndGet();
                        failedFinal.incrementAndGet();
                        log.warn("[task-{}] rejected: circuit breaker OPEN", id);
                    } catch (RequestNotPermitted e) {
                        rlRejected.incrementAndGet();
                        failedFinal.incrementAndGet();
                        log.warn("[task-{}] rejected: rate limiter", id);
                    } catch (BulkheadFullException e) {
                        bhRejected.incrementAndGet();
                        failedFinal.incrementAndGet();
                        log.warn("[task-{}] rejected: bulkhead full", id);
                    } catch (Exception e) {
                        failedFinal.incrementAndGet();
                        log.warn("[task-{}] failed after retries: {} ({}ms)", id, e.getMessage(),
                                Duration.between(start, Instant.now()).toMillis());
                    }
                } finally {
                    resilience.clearCurrentTaskId();
                }
            });
        };

        ScheduledFuture<?> scheduledFuture =
                scheduler.scheduleAtFixedRate(submitTask, 0, submitIntervalMillis, TimeUnit.MILLISECONDS);

        // Периодическая сводка состояния resilience4j-стека — видно переходы CB между строками.
        scheduler.scheduleAtFixedRate(() -> log.info("[state] {}", resilience.summary()), 1, 1, TimeUnit.SECONDS);

        CountDownLatch shutdownComplete = new CountDownLatch(1);
        Runtime.getRuntime().addShutdownHook(new Thread(() -> {
            Instant sigAt = Instant.now();
            log.warn("=== SIGTERM received at {} — starting graceful shutdown ===", sigAt);
            health.setReady(false);
            acceptingNewWork.set(false);
            scheduledFuture.cancel(false);
            scheduler.shutdown();
            workExecutor.shutdown();
            try {
                boolean drained = workExecutor.awaitTermination(15, TimeUnit.SECONDS);
                log.warn("=== drain {} in {}ms (submitted={}, succeeded={}, failed={}, cbRejected={}, rlRejected={}, bhRejected={}) ===",
                        drained ? "completed" : "TIMED OUT",
                        Duration.between(sigAt, Instant.now()).toMillis(),
                        submitted.get(), succeeded.get(), failedFinal.get(),
                        cbRejected.get(), rlRejected.get(), bhRejected.get());
                if (!drained) {
                    workExecutor.shutdownNow();
                }
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                workExecutor.shutdownNow();
            }
            health.stop();
            log.warn("=== shutdown finished at {} (cbState={}) ===", Instant.now(), resilience.cbState());
            shutdownComplete.countDown();
        }, "shutdown-hook"));

        log.info("Started: recoveryAfterMillis={}, healthPort={}, submitIntervalMillis={}",
                recoveryAfterMillis, healthPort, submitIntervalMillis);
        shutdownComplete.await();
    }
}
