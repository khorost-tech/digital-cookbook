package tech.khorost.testing;

import java.util.HashMap;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Ручной stub {@link OrderRepository} — простой Java-класс без Mockito-рефлексии,
 * без Docker, без сети. Используется в {@link OrderServiceUnitTest} как самый
 * дешёвый способ изолировать бизнес-логику от хранилища.
 */
class InMemoryOrderRepository implements OrderRepository {

    private final Map<Long, Order> store = new HashMap<>();
    private final AtomicLong ids = new AtomicLong();

    @Override
    public Order save(Order order) {
        Order saved = order.withId(ids.incrementAndGet());
        store.put(saved.id(), saved);
        return saved;
    }

    @Override
    public Optional<Order> findById(long id) {
        return Optional.ofNullable(store.get(id));
    }

    int size() {
        return store.size();
    }
}
