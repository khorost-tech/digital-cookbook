package tech.khorost.productionpatterns;

import io.github.resilience4j.bulkhead.Bulkhead;
import io.github.resilience4j.bulkhead.BulkheadConfig;
import io.github.resilience4j.bulkhead.BulkheadFullException;
import io.github.resilience4j.circuitbreaker.CallNotPermittedException;
import io.github.resilience4j.circuitbreaker.CircuitBreaker;
import io.github.resilience4j.circuitbreaker.CircuitBreakerConfig;
import io.github.resilience4j.core.IntervalFunction;
import io.github.resilience4j.ratelimiter.RateLimiter;
import io.github.resilience4j.ratelimiter.RateLimiterConfig;
import io.github.resilience4j.ratelimiter.RequestNotPermitted;
import io.github.resilience4j.retry.Retry;
import io.github.resilience4j.retry.RetryConfig;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.time.Instant;
import java.util.function.Supplier;

/**
 * Порядок декораторов (снаружи внутрь): Retry -> CircuitBreaker -> RateLimiter -> Bulkhead
 * -> downstream. Каждая retry-попытка — это отдельный проход через CB/RateLimiter/Bulkhead
 * (а не один сквозной вызов с ретраем только downstream) — так ведёт себя реальный клиент.
 *
 * CircuitBreaker игнорирует RequestNotPermitted и BulkheadFullException в своей статистике:
 * перегрузка нашего же клиента (rate limiter/bulkhead) — это не сигнал деградации downstream,
 * breaker должен реагировать только на реальные DownstreamException.
 *
 * Retry игнорирует CallNotPermittedException: если breaker уже открыт, повторять вызов
 * бессмысленно (только больше нагрузки) — единственная попытка должна быстро провалиться.
 */
public class ResilienceStack {

    private static final Logger log = LoggerFactory.getLogger("ResilienceStack");

    private final CircuitBreaker circuitBreaker;
    private final Retry retry;
    private final RateLimiter rateLimiter;
    private final Bulkhead bulkhead;

    // Retry/CB/RateLimiter/Bulkhead — общие объекты на всё приложение (одна цепочка
    // декораторов на все задачи), поэтому у их event-листенеров нет своего task-id.
    // Каждый вызов decorate(...).get() выполняется синхронно в потоке задачи (retry
    // и есть Thread.sleep между попытками в этом же потоке), так что обычный ThreadLocal,
    // выставленный вызывающим кодом вокруг get(), виден внутри листенера.
    // MDC тут не годится: slf4j-simple не предоставляет MDCAdapter, MDC.put/get — no-op.
    private final ThreadLocal<String> currentTaskId = new ThreadLocal<>();

    public ResilienceStack() {
        CircuitBreakerConfig cbConfig = CircuitBreakerConfig.custom()
                .slidingWindowType(CircuitBreakerConfig.SlidingWindowType.COUNT_BASED)
                .slidingWindowSize(10)
                .minimumNumberOfCalls(5)
                .failureRateThreshold(50.0f)
                .waitDurationInOpenState(Duration.ofSeconds(4))
                .permittedNumberOfCallsInHalfOpenState(3)
                .automaticTransitionFromOpenToHalfOpenEnabled(true)
                .ignoreExceptions(RequestNotPermitted.class, BulkheadFullException.class)
                .build();
        circuitBreaker = CircuitBreaker.of("downstream", cbConfig);
        circuitBreaker.getEventPublisher().onStateTransition(e ->
                log.warn(">>> [CircuitBreaker] {} -> {} at {}",
                        e.getStateTransition().getFromState(), e.getStateTransition().getToState(), Instant.now()));

        RetryConfig retryConfig = RetryConfig.custom()
                .maxAttempts(3)
                .intervalFunction(IntervalFunction.ofExponentialBackoff(Duration.ofMillis(300), 2.0))
                .ignoreExceptions(CallNotPermittedException.class)
                .build();
        retry = Retry.of("downstream", retryConfig);
        retry.getEventPublisher().onRetry(e -> {
            String taskId = currentTaskId.get();
            log.info(">>> [Retry] [task-{}] attempt #{} after: {}",
                    taskId == null ? "?" : taskId,
                    e.getNumberOfRetryAttempts(),
                    e.getLastThrowable() == null ? "?" : e.getLastThrowable().getMessage());
        });

        RateLimiterConfig rlConfig = RateLimiterConfig.custom()
                .limitForPeriod(5)
                .limitRefreshPeriod(Duration.ofSeconds(1))
                .timeoutDuration(Duration.ZERO)
                .build();
        rateLimiter = RateLimiter.of("downstream", rlConfig);
        String rlLimitLabel = rlConfig.getLimitForPeriod() + "/" + rlConfig.getLimitRefreshPeriod().toSeconds() + "s";
        rateLimiter.getEventPublisher().onFailure(e ->
                log.warn(">>> [RateLimiter] rejected — limit exhausted ({})", rlLimitLabel));

        BulkheadConfig bhConfig = BulkheadConfig.custom()
                .maxConcurrentCalls(3)
                .maxWaitDuration(Duration.ZERO)
                .build();
        bulkhead = Bulkhead.of("downstream", bhConfig);
        bulkhead.getEventPublisher().onCallRejected(e ->
                log.warn(">>> [Bulkhead] rejected — {} concurrent slots full", bhConfig.getMaxConcurrentCalls()));
    }

    public <T> Supplier<T> decorate(Supplier<T> supplier) {
        Supplier<T> withBulkhead = Bulkhead.decorateSupplier(bulkhead, supplier);
        Supplier<T> withRateLimiter = RateLimiter.decorateSupplier(rateLimiter, withBulkhead);
        Supplier<T> withCircuitBreaker = CircuitBreaker.decorateSupplier(circuitBreaker, withRateLimiter);
        return Retry.decorateSupplier(retry, withCircuitBreaker);
    }

    /**
     * Вызывающий код (Main) обязан выставить id вокруг decorate(...).get() и снять его
     * в finally — иначе строки "[Retry] [task-?]" останутся без атрибуции к задаче.
     */
    public void setCurrentTaskId(String taskId) {
        currentTaskId.set(taskId);
    }

    public void clearCurrentTaskId() {
        currentTaskId.remove();
    }

    public CircuitBreaker.State cbState() {
        return circuitBreaker.getState();
    }

    public String summary() {
        CircuitBreaker.Metrics cbm = circuitBreaker.getMetrics();
        return String.format(
                "CB state=%s, failureRate=%.1f%%, bufferedCalls=%d, failedCalls=%d | RateLimiter availablePermissions=%d | Bulkhead availableSlots=%d",
                circuitBreaker.getState(), cbm.getFailureRate(), cbm.getNumberOfBufferedCalls(), cbm.getNumberOfFailedCalls(),
                rateLimiter.getMetrics().getAvailablePermissions(), bulkhead.getMetrics().getAvailableConcurrentCalls());
    }
}
