package tech.khorost.mm;

import org.openjdk.jol.info.ClassLayout;
import org.openjdk.jol.info.GraphLayout;
import org.openjdk.jol.vm.VM;

/**
 * Опора 2 — раскладка объекта и autoboxing через JOL (детерминированные БАЙТЫ).
 *
 * 1) Печатаем раскладку одного узла Cycle.Node: заголовок объекта (mark + class)
 *    и поля (int id + ссылка Node other).
 * 2) Контраст autoboxing: суммарный retained-размер Integer[] (боксированные)
 *    против примитивного int[] той же длины. Боксированный массив тянет за собой
 *    отдельные объекты Integer + ссылки на них.
 */
public final class Layout {

    public static void main(String[] args) {
        System.out.println(VM.current().details());
        System.out.println("=================================================");

        // --- Раскладка объекта Node ---
        Cycle.Node node = new Cycle.Node(42);
        node.other = new Cycle.Node(43);
        System.out.println("== ClassLayout: Cycle.Node ==");
        System.out.println(ClassLayout.parseInstance(node).toPrintable());

        // --- Autoboxing: Integer[] против int[] ---
        final int n = 10_000;

        Integer[] boxed = new Integer[n];
        int[] prim = new int[n];
        for (int i = 0; i < n; i++) {
            boxed[i] = i;   // autoboxing -> отдельные Integer
            prim[i] = i;
        }

        long boxedBytes = GraphLayout.parseInstance((Object) boxed).totalSize();
        long primBytes = GraphLayout.parseInstance(prim).totalSize();

        System.out.println("== Autoboxing: n=" + n + " ==");
        System.out.println("Integer[] total_bytes=" + boxedBytes);
        System.out.println("int[]     total_bytes=" + primBytes);
        System.out.printf("ratio=%.2fx%n", (double) boxedBytes / primBytes);
        System.out.println("overhead_bytes=" + (boxedBytes - primBytes));

        System.out.println();
        System.out.println("== GraphLayout footprint: Integer[] ==");
        System.out.println(GraphLayout.parseInstance((Object) boxed).toFootprint());
        System.out.println("== GraphLayout footprint: int[] ==");
        System.out.println(GraphLayout.parseInstance(prim).toFootprint());
    }
}
