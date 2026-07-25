package tech.khorost.modernfeatures;

import java.time.Duration;
import java.util.List;
import java.util.concurrent.Callable;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.stream.IntStream;

/**
 * Virtual threads: {@link Executors#newVirtualThreadPerTaskExecutor()} и
 * {@link Thread#ofVirtual()}.
 *
 * <p>Статус на JDK 25: finalized (JEP 444), finalized уже в JDK 21 (после preview
 * в 19/20 под JEP 425/436). На 25 — та же финализированная API, без изменений
 * и без {@code --enable-preview}. Дополнительный контекст к серии: живой бенчмарк
 * virtual vs platform threads уже есть в соседнем модуле {@code concurrency/}
 * (Bench.java) — здесь только синтаксис/API, не throughput-числа.
 */
public final class VirtualThreadsDemo {

    public static void main(String[] args) throws InterruptedException, ExecutionException {
        // 1) Executors.newVirtualThreadPerTaskExecutor(): по виртуальному потоку на задачу.
        // try-with-resources закрывает executor и дожидается завершения задач (close() блокирующий).
        try (ExecutorService executor = Executors.newVirtualThreadPerTaskExecutor()) {
            List<Callable<Integer>> tasks = IntStream.range(0, 1000)
                    .<Callable<Integer>>mapToObj(i -> () -> {
                        Thread.sleep(Duration.ofMillis(1));
                        return i;
                    })
                    .toList();

            List<Future<Integer>> futures = executor.invokeAll(tasks);
            int sum = 0;
            for (Future<Integer> f : futures) {
                sum += f.get();
            }
            System.out.println("1000 virtual-thread задач (invokeAll), сумма индексов = " + sum);
        }

        // 2) Thread.ofVirtual(): точечный запуск одного виртуального потока,
        // без пула — планировщик виртуальных потоков сам мультиплексирует их
        // на platform-carrier-потоки под капотом.
        Thread vt = Thread.ofVirtual()
                .name("demo-virtual-thread")
                .start(() -> System.out.println("виртуальный поток: " + Thread.currentThread()));
        vt.join();

        System.out.println("Thread.currentThread().isVirtual() в main = " + Thread.currentThread().isVirtual());
        System.out.println("vt.isVirtual() = " + vt.isVirtual());
    }
}
