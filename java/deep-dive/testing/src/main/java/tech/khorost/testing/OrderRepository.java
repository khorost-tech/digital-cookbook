package tech.khorost.testing;

import java.util.Optional;

/**
 * Интерфейс репозитория заказов — намеренно узкий, чтобы unit-тест мог
 * подсунуть ручной in-memory stub, а интеграционный тест — реальную
 * JDBC-реализацию поверх Testcontainers-Postgres.
 */
public interface OrderRepository {

    Order save(Order order);

    Optional<Order> findById(long id);
}
