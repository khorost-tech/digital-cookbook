package tech.khorost.testing;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import org.junit.jupiter.api.Test;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import tech.khorost.testing.Order.OrderStatus;

/**
 * Юнит-уровень: бизнес-правило {@link OrderService#createOrder} проверяется
 * без Docker, без сети, без реальной БД — обе версии (ручной stub и Mockito-mock)
 * укладываются в единицы миллисекунд, что видно в выводе surefire ("Time elapsed").
 */
class OrderServiceUnitTest {

    private static final Logger log = LoggerFactory.getLogger(OrderServiceUnitTest.class);

    @Test
    void pendingForSmallAmount_withStub() {
        long start = System.nanoTime();

        InMemoryOrderRepository repository = new InMemoryOrderRepository();
        OrderService service = new OrderService(repository);

        Order order = service.createOrder("Alice", 500);

        assertEquals(OrderStatus.PENDING, order.status());
        assertEquals(1, repository.size());

        log.info("pendingForSmallAmount_withStub took {} µs", (System.nanoTime() - start) / 1000);
    }

    @Test
    void reviewForLargeAmount_withStub() {
        InMemoryOrderRepository repository = new InMemoryOrderRepository();
        OrderService service = new OrderService(repository);

        Order order = service.createOrder("Bob", 10_000);

        assertEquals(OrderStatus.REVIEW, order.status());
    }

    @Test
    void rejectsBlankCustomerName_withStub() {
        OrderService service = new OrderService(new InMemoryOrderRepository());

        assertThrows(IllegalArgumentException.class, () -> service.createOrder("  ", 100));
    }

    @Test
    void createOrder_savesThroughRepository_withMockitoMock() {
        // Контраст: тот же сценарий, но через Mockito-mock вместо ручного stub —
        // здесь важна не сама запись, а факт и аргументы вызова save().
        OrderRepository mockRepository = mock(OrderRepository.class);
        when(mockRepository.save(any(Order.class)))
                .thenAnswer(invocation -> invocation.<Order>getArgument(0).withId(42L));

        OrderService service = new OrderService(mockRepository);
        Order order = service.createOrder("Carol", 250);

        assertEquals(42L, order.id());
        assertTrue(order.customerName().equals("Carol"));
        verify(mockRepository).save(any(Order.class));
    }
}
