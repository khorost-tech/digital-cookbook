package tech.khorost.concurrency;

import java.time.Duration;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

/**
 * Реактивная модель: N подписок Mono.delay(sleepMs) как заглушка I/O,
 * flatMap с concurrency=N (все задачи "в полёте" одновременно — тот же режим
 * нагрузки, что и у virtual threads: без искусственного ограничения на
 * количество одновременных задач). Mono.delay использует Schedulers.parallel()
 * — таймер-колёса на небольшом пуле потоков, не поток на задачу.
 */
final class ReactorBench {

    private ReactorBench() {
    }

    static Result run(int n, int sleepMs) {
        long[] latenciesNanos = new long[n];

        long wallStart = System.nanoTime();
        Flux.range(0, n)
                .flatMap(i -> {
                    long submitTime = System.nanoTime();
                    return Mono.delay(Duration.ofMillis(sleepMs))
                            .doOnNext(tick -> latenciesNanos[i] = System.nanoTime() - submitTime);
                }, n)
                .blockLast();
        long wallNanos = System.nanoTime() - wallStart;

        return new Result("reactor", n, wallNanos, latenciesNanos);
    }
}
