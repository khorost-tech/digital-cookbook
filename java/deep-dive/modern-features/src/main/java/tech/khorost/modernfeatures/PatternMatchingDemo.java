package tech.khorost.modernfeatures;

import java.util.List;

/**
 * Pattern matching: instanceof-patterns, switch с type/record patterns,
 * guarded patterns ({@code when}).
 *
 * <p>Статус на JDK 25: finalized.
 * <ul>
 *   <li>pattern matching for instanceof — JEP 394, finalized в JDK 16.</li>
 *   <li>pattern matching for switch (type patterns, null-case, guarded patterns
 *       {@code when}) — JEP 441, finalized в JDK 21 (после preview в 17/18/19/20
 *       под JEP 406/420/427/433).</li>
 * </ul>
 * Не требует {@code --enable-preview} на 25.
 */
public final class PatternMatchingDemo {

    // instanceof pattern: переменная типа сразу доступна в true-ветке,
    // без отдельного явного каста.
    static String formatLength(Object o) {
        if (o instanceof String s && !s.isEmpty()) {
            return "строка длиной " + s.length();
        }
        if (o instanceof List<?> list) {
            return "список из " + list.size() + " элементов";
        }
        return "неизвестный тип: " + o;
    }

    // switch с type patterns + guarded patterns (when) + case null.
    // Явный "case null" — тоже часть JEP 441: без него switch над ссылочным
    // типом по-прежнему бросает NullPointerException на null, как и раньше.
    static String classify(Object o) {
        return switch (o) {
            case null -> "null";
            case Integer i when i < 0 -> "отрицательное целое: " + i;
            case Integer i when i == 0 -> "ноль";
            case Integer i -> "положительное целое: " + i;
            case String s when s.isBlank() -> "пустая/пробельная строка";
            case String s -> "строка: \"" + s + "\"";
            default -> "прочее: " + o;
        };
    }

    public static void main(String[] args) {
        System.out.println(formatLength("hello"));
        System.out.println(formatLength(List.of(1, 2, 3)));
        System.out.println(formatLength(42));

        for (Object o : new Object[] {null, -5, 0, 7, "  ", "text", 3.14}) {
            System.out.println("classify(" + o + ") = " + classify(o));
        }
    }
}
