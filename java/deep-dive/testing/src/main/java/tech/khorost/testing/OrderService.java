package tech.khorost.testing;

import java.util.Objects;

import tech.khorost.testing.Order.OrderStatus;

/**
 * Бизнес-логика создания заказа. Не знает, как именно репозиторий хранит
 * данные (in-memory stub в unit-тесте, реальный Postgres в integration-тесте) —
 * это и есть точка контраста между двумя уровнями тестов.
 */
public class OrderService {

    /** Заказы от этой суммы (в копейках) и выше уходят в ручной REVIEW. */
    static final long REVIEW_THRESHOLD_CENTS = 10_000L;

    private final OrderRepository repository;

    public OrderService(OrderRepository repository) {
        this.repository = Objects.requireNonNull(repository, "repository");
    }

    public Order createOrder(String customerName, long amountCents) {
        if (customerName == null || customerName.isBlank()) {
            throw new IllegalArgumentException("customerName must not be blank");
        }
        if (amountCents <= 0) {
            throw new IllegalArgumentException("amountCents must be positive");
        }
        OrderStatus status = amountCents >= REVIEW_THRESHOLD_CENTS
                ? OrderStatus.REVIEW
                : OrderStatus.PENDING;
        Order order = new Order(null, customerName, amountCents, status);
        return repository.save(order);
    }
}
