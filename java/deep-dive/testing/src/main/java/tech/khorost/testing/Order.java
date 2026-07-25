package tech.khorost.testing;

/**
 * Заказ. status выставляется бизнес-правилом в {@link OrderService#createOrder}:
 * суммы от 10000 копеек (100.00) и выше уходят в ручной REVIEW, иначе сразу PENDING.
 */
public record Order(Long id, String customerName, long amountCents, OrderStatus status) {

    public enum OrderStatus {
        PENDING,
        REVIEW
    }

    /** Копия с проставленным id — репозиторий возвращает такую после save(). */
    public Order withId(long newId) {
        return new Order(newId, customerName, amountCents, status);
    }
}
