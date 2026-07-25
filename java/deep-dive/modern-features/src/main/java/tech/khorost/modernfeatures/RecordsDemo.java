package tech.khorost.modernfeatures;

/**
 * records + compact constructors + record patterns (instanceof / switch).
 *
 * <p>Статус на JDK 25: finalized.
 * <ul>
 *   <li>records — JEP 395, finalized в JDK 16.</li>
 *   <li>record patterns (в instanceof и switch, включая вложенные/деконструкцию) —
 *       JEP 440, finalized в JDK 21 (preview был в 19/20 под JEP 405/432).</li>
 * </ul>
 * Ничего из показанного здесь не требует {@code --enable-preview} на 25.
 */
public final class RecordsDemo {

    // Обычный record: неявные accessor'ы (x(), y()), equals/hashCode/toString,
    // канонический конструктор генерируется компилятором.
    record Point(int x, int y) {}

    // Compact constructor: валидация без повторения списка параметров
    // и без явного присваивания полей (компилятор доассигнует их сам).
    record Range(int lo, int hi) {
        Range {
            if (lo > hi) {
                throw new IllegalArgumentException("lo=%d > hi=%d".formatted(lo, hi));
            }
        }
    }

    // Вложенный record — материал для record pattern с деконструкцией на несколько уровней.
    record Line(Point from, Point to) {}

    sealed interface Shape permits Circle, Rectangle {}
    record Circle(Point center, int radius) implements Shape {}
    record Rectangle(Point topLeft, Point bottomRight) implements Shape {}

    static String describe(Object o) {
        // record pattern в switch: деконструкция + guarded pattern (when) в одном выражении.
        return switch (o) {
            case Point(int x, int y) when x == 0 && y == 0 -> "точка в начале координат";
            case Point(int x, int y) -> "точка (%d, %d)".formatted(x, y);
            case Line(Point(var x1, var y1), Point(var x2, var y2)) ->
                    "отрезок (%d,%d)-(%d,%d)".formatted(x1, y1, x2, y2);
            case Circle(Point c, int r) -> "круг радиуса %d в (%d,%d)".formatted(r, c.x(), c.y());
            case Rectangle r -> "прямоугольник %s-%s".formatted(r.topLeft(), r.bottomRight());
            default -> "нечто иное: " + o;
        };
    }

    static void demoInstanceofPattern(Object o) {
        // record pattern в instanceof — деконструкция сразу в локальные переменные.
        if (o instanceof Point(int x, int y) && x == y) {
            System.out.println("точка на диагонали: x=y=" + x);
        }
    }

    public static void main(String[] args) {
        Range r = new Range(1, 10);
        System.out.println("Range OK: " + r);

        try {
            new Range(10, 1);
        } catch (IllegalArgumentException e) {
            System.out.println("Compact constructor поймал инвариант: " + e.getMessage());
        }

        System.out.println(describe(new Point(0, 0)));
        System.out.println(describe(new Point(3, 4)));
        System.out.println(describe(new Line(new Point(0, 0), new Point(1, 1))));
        System.out.println(describe(new Circle(new Point(5, 5), 2)));
        System.out.println(describe(new Rectangle(new Point(0, 0), new Point(4, 4))));

        demoInstanceofPattern(new Point(7, 7));

        // Автосгенерированные equals/hashCode/toString для records — по значению, не по ссылке.
        System.out.println("equals по значению: " + new Point(1, 2).equals(new Point(1, 2)));
    }
}
