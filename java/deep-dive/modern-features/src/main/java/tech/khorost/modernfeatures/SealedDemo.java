package tech.khorost.modernfeatures;

/**
 * sealed interfaces/classes + exhaustive switch.
 *
 * <p>Статус на JDK 25: finalized. sealed classes/interfaces — JEP 409, finalized
 * в JDK 17 (preview был в 15/16 под JEP 360/397). Exhaustiveness-проверка switch
 * над sealed-иерархией (без {@code default}) — часть pattern matching for switch,
 * JEP 441, finalized в JDK 21. Не требует {@code --enable-preview} на 25.
 *
 * <p>Ключевое свойство: компилятор ЗНАЕТ полный список permits-наследников и
 * отказывается компилировать switch без {@code default}, если не покрыты все
 * ветки. Добавление нового наследника в permits без правки switch — ошибка
 * компиляции в каждом месте, где switch считался исчерпывающим (а не runtime-
 * сюрприз, как с обычным полиморфизмом + if-else цепочкой).
 */
public final class SealedDemo {

    sealed interface PaymentEvent permits Authorized, Captured, Refunded, Failed {}

    record Authorized(String orderId, long amountCents) implements PaymentEvent {}
    record Captured(String orderId, long amountCents) implements PaymentEvent {}
    record Refunded(String orderId, long amountCents, String reason) implements PaymentEvent {}
    record Failed(String orderId, String errorCode) implements PaymentEvent {}

    // switch-выражение БЕЗ default: компилятор проверяет, что покрыты все 4
    // permits-варианта. Закомментируй любую ветку — получишь ошибку компиляции
    // "the switch expression does not cover all possible input values".
    static String describe(PaymentEvent event) {
        return switch (event) {
            case Authorized(var orderId, var amount) ->
                    "заказ %s авторизован на %d коп.".formatted(orderId, amount);
            case Captured(var orderId, var amount) ->
                    "заказ %s списан на %d коп.".formatted(orderId, amount);
            case Refunded(var orderId, var amount, var reason) ->
                    "заказ %s возвращён (%d коп., причина: %s)".formatted(orderId, amount, reason);
            case Failed(var orderId, var code) ->
                    "заказ %s упал с кодом %s".formatted(orderId, code);
        };
    }

    // Абстрактный sealed class (не только interface) с явным permits.
    abstract sealed static class Shape permits Circle, Square {
        abstract double area();
    }

    static final class Circle extends Shape {
        final double radius;
        Circle(double radius) { this.radius = radius; }
        @Override double area() { return Math.PI * radius * radius; }
    }

    static final class Square extends Shape {
        final double side;
        Square(double side) { this.side = side; }
        @Override double area() { return side * side; }
    }

    static double areaOf(Shape s) {
        // Exhaustive switch над sealed class-иерархией с обычными (не record)
        // type patterns — тоже проверяется компилятором на полноту.
        return switch (s) {
            case Circle c -> c.area();
            case Square sq -> sq.area();
        };
    }

    public static void main(String[] args) {
        System.out.println(describe(new Authorized("A-1", 10_000)));
        System.out.println(describe(new Captured("A-1", 10_000)));
        System.out.println(describe(new Refunded("A-1", 10_000, "customer request")));
        System.out.println(describe(new Failed("A-2", "CARD_DECLINED")));

        System.out.printf("area(circle r=2) = %.2f%n", areaOf(new Circle(2)));
        System.out.printf("area(square a=3) = %.2f%n", areaOf(new Square(3)));
    }
}
