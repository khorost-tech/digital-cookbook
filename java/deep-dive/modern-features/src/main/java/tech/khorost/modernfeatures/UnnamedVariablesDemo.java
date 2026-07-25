package tech.khorost.modernfeatures;

import java.util.List;
import java.util.function.BiFunction;

/**
 * Unnamed variables & patterns ({@code _}).
 *
 * <p>Статус на JDK 25: finalized (JEP 456), finalized уже в JDK 22 (был preview
 * в JDK 21 под JEP 443). На 25 — тот же финализированный синтаксис, без изменений
 * и без {@code --enable-preview}.
 *
 * <p>{@code _} можно использовать там, где значение обязано существовать
 * (переменная, параметр лямбды, catch-параметр, компонент record pattern), но
 * реально не используется в теле. Компилятор запрещает ЧИТАТЬ {@code _} —
 * это не идентификатор для использования, а маркер "не называем и не трогаем".
 */
public final class UnnamedVariablesDemo {

    record Point(int x, int y) {}

    public static void main(String[] args) {
        // 1) catch-параметр: тип исключения важен (для сортировки multi-catch/
        // логики выше), а сам объект исключения — нет.
        try {
            Integer.parseInt("not-a-number");
        } catch (NumberFormatException _) {
            System.out.println("поймали NumberFormatException, детали не нужны");
        }

        // 2) unnamed variable в enhanced-for: важно количество итераций, не элемент.
        List<String> items = List.of("a", "b", "c");
        int count = 0;
        for (String _ : items) {
            count++;
        }
        System.out.println("count=" + count);

        // 3) unnamed pattern-компонент в деконструкции record: нужен только x.
        Object o = new Point(3, 4);
        if (o instanceof Point(int x, int _)) {
            System.out.println("x=" + x + " (y деконструирован, но не назван)");
        }

        // 4) unnamed lambda-параметр: BiFunction, где второй аргумент не нужен телу.
        BiFunction<Integer, Integer, Integer> firstOnly = (a, _) -> a * 10;
        System.out.println("firstOnly(5, 999) = " + firstOnly.apply(5, 999));

        // 5) unnamed local variable для игнорирования результата вызова
        // (когда возвращаемое значение важно проигнорировать явно, не implicit-statement).
        var list = new java.util.ArrayList<Integer>();
        var _ = list.add(42); // add() возвращает boolean — явно не используем
        System.out.println("list after add: " + list);
    }
}
