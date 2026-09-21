package tech.khorost.mm;

import java.lang.management.ManagementFactory;
import java.lang.ref.Cleaner;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * Опора 1 — трассирующий GC собирает циклическую структуру.
 *
 * Строим большой циклический граф из пар узлов (a -> b -> a). Такая структура
 * НЕ собирается подсчётом ссылок (Rc в C++/Rust): взаимные ссылки держат
 * счётчик > 0. Трассирующий GC в JVM видит, что от корней граф недостижим,
 * и собирает его целиком.
 *
 * Cleaner регистрируется на каждый узел. Действие Cleaner НЕ захватывает сам
 * Node (иначе узел остался бы достижимым через замыкание и не был бы собран) —
 * захватывается только общий AtomicInteger-счётчик и int-id.
 */
public final class Cycle {

    static final class Node {
        int id;
        Node other; // взаимная ссылка -> цикл

        Node(int id) {
            this.id = id;
        }
    }

    public static void main(String[] args) throws Exception {
        final int pairs = 200_000; // 400_000 узлов, каждый в цикле длины 2
        final Cleaner cleaner = Cleaner.create();
        final AtomicInteger cleaned = new AtomicInteger();

        // Держим граф за корневой массив, чтобы контролировать достижимость.
        Node[] roots = new Node[pairs];

        for (int i = 0; i < pairs; i++) {
            Node a = new Node(2 * i);
            Node b = new Node(2 * i + 1);
            a.other = b;
            b.other = a; // цикл a <-> b

            final int idA = a.id;
            final int idB = b.id;
            // ВАЖНО: не захватываем a/b — только счётчик и примитивные id.
            cleaner.register(a, () -> cleaned.incrementAndGet());
            cleaner.register(b, () -> cleaned.incrementAndGet());

            roots[i] = a; // держим только 'a'; 'b' достижим через a.other
        }

        int totalNodes = 2 * pairs;
        System.out.println("built pairs=" + pairs + " nodes=" + totalNodes);

        // Прогреем и стабилизируем кучу перед замером.
        System.gc();
        Thread.sleep(200);

        long usedBefore = ManagementFactory.getMemoryMXBean()
                .getHeapMemoryUsage().getUsed();
        System.out.println("used_before_bytes=" + usedBefore);

        // Делаем весь граф недостижимым.
        for (int i = 0; i < pairs; i++) {
            roots[i] = null;
        }
        roots = null;

        // Несколько раундов GC + короткое ожидание, чтобы Cleaner успел отработать.
        for (int r = 0; r < 10; r++) {
            System.gc();
            Thread.sleep(150);
        }

        long usedAfter = ManagementFactory.getMemoryMXBean()
                .getHeapMemoryUsage().getUsed();
        System.out.println("used_after_bytes=" + usedAfter);

        long reclaimedBytes = usedBefore - usedAfter;
        double reclaimedMb = reclaimedBytes / (1024.0 * 1024.0);
        System.out.printf("reclaimed_mb=%.2f%n", reclaimedMb);
        System.out.println("cleaners_fired=" + cleaned.get() + " of " + totalNodes);

        // FAIL-LOUD: опора 1 доказана, только если цикл реально собран.
        // Первичный признак — освобождённая память; ожидаем ~34 МБ, порог 20 МБ.
        // Вторичный — все Cleaner отработали (при 10 раундах System.gc()+пауза
        // это устойчиво воспроизводится; расхождение = регресс, а не шум).
        final double MIN_RECLAIMED_MB = 20.0;
        boolean ok = true;
        if (reclaimedMb < MIN_RECLAIMED_MB) {
            System.err.printf("FAIL: reclaimed_mb=%.2f < %.1f — граф НЕ собран%n",
                    reclaimedMb, MIN_RECLAIMED_MB);
            ok = false;
        }
        if (cleaned.get() != totalNodes) {
            System.err.println("FAIL: cleaners_fired=" + cleaned.get()
                    + " != " + totalNodes);
            ok = false;
        }
        if (!ok) {
            System.err.println("=== ОПОРА 1 НЕ ДОКАЗАНА ===");
            System.exit(1);
        }
        System.out.println("OK: цикл собран (reclaimed >= " + MIN_RECLAIMED_MB
                + " МБ, все " + totalNodes + " Cleaner сработали)");
    }
}
