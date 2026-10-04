package tech.khorost.observability;

import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;

import java.nio.charset.StandardCharsets;
import org.junit.jupiter.api.Test;

/**
 * Обработчик события проверяется без NATS: подписка — дело инфраструктуры, а
 * разбор и сохранение — логика, и её надо проверять отдельно.
 */
class OrderListenerTest {

    private final InventoryRepository repository = mock(InventoryRepository.class);
    private final OrderListener listener = new OrderListener(repository, "nats://unused:4222", "orders.created");

    @Test
    void savesOrderFromEvent() {
        String event = """
                {"order_id":"ord-SKU-0001-1","sku":"SKU-0001","quantity":3,"reserved":true}
                """;

        listener.handle(event.getBytes(StandardCharsets.UTF_8));

        verify(repository).saveOrder("ord-SKU-0001-1", "SKU-0001", 3, true);
    }

    @Test
    void ignoresUnknownFields() {
        // go-frontend может добавить поля в событие; подписчик не должен на этом
        // падать, иначе выкатка одной стороны ломает другую.
        String event = """
                {"order_id":"ord-1","sku":"SKU-0002","quantity":1,"reserved":false,"future_field":"x"}
                """;

        listener.handle(event.getBytes(StandardCharsets.UTF_8));

        verify(repository).saveOrder("ord-1", "SKU-0002", 1, false);
    }

    @Test
    void survivesBrokenPayload() {
        listener.handle("{not json".getBytes(StandardCharsets.UTF_8));

        verify(repository, never()).saveOrder(org.mockito.ArgumentMatchers.anyString(),
                org.mockito.ArgumentMatchers.anyString(),
                org.mockito.ArgumentMatchers.anyInt(),
                org.mockito.ArgumentMatchers.anyBoolean());
    }
}
