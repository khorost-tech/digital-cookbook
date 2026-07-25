package tech.khorost.concurrency;

import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * Точка входа стенда "Модели конкурентности на JVM под I/O-bound нагрузкой".
 * Каждый режим — отдельный процесс JVM (см. run-all.sh), чтобы peak RSS
 * одного режима не смешивался с другим внутри общего процесса/кучи.
 *
 * <pre>
 * java -jar target/concurrency.jar &lt;mode&gt; [n] [sleepMs]
 *
 * mode:
 *   vt              — Executors.newVirtualThreadPerTaskExecutor()
 *   platform        — Executors.newFixedThreadPool(200)   (демонстрация: throughput упирается в размер пула)
 *   platform-large  — Executors.newFixedThreadPool(n)      (демонстрация: N платформенных потоков — стоимость по памяти)
 *   reactor         — Flux.range(n).flatMap(Mono.delay, concurrency=n)
 * </pre>
 */
public final class Bench {

    private Bench() {
    }

    public static void main(String[] args) throws Exception {
        String mode = args.length > 0 ? args[0] : "vt";
        int n = args.length > 1 ? Integer.parseInt(args[1]) : 10_000;
        int sleepMs = args.length > 2 ? Integer.parseInt(args[2]) : 100;

        Result result = switch (mode) {
            case "vt" -> BlockingBench.run(mode, Executors.newVirtualThreadPerTaskExecutor(), n, sleepMs);
            case "platform" -> BlockingBench.run(mode, fixedPool(200), n, sleepMs);
            case "platform-large" -> BlockingBench.run(mode, fixedPool(n), n, sleepMs);
            case "reactor" -> ReactorBench.run(n, sleepMs);
            default -> throw new IllegalArgumentException(
                    "unknown mode '" + mode + "', ожидается: vt | platform | platform-large | reactor");
        };

        result.printReport();
    }

    private static ExecutorService fixedPool(int size) {
        return Executors.newFixedThreadPool(size);
    }
}
