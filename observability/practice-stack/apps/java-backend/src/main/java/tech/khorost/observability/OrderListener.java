package tech.khorost.observability;

import tools.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.nats.client.Nats;
import io.nats.client.Options;
import jakarta.annotation.PostConstruct;
import jakarta.annotation.PreDestroy;
import java.time.Duration;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Component;

/**
 * Подписчик событий о заказах. Асинхронная граница здесь не для красоты: именно
 * на ней трейс рвётся сам собой, если контекст не передан в заголовках
 * сообщения. В статье 2 это становится отдельной демонстрацией.
 */
@Component
public class OrderListener {

    private static final Logger log = LoggerFactory.getLogger(OrderListener.class);

    private final InventoryRepository repository;
    // Jackson 3, а не 2: Spring Boot 4 / Framework 7 переехали на пакет
    // tools.jackson.*, и com.fasterxml.jackson.databind в classpath больше нет.
    private final ObjectMapper mapper = new ObjectMapper();
    private final String natsUrl;
    private final String subject;

    private Connection connection;
    private Dispatcher dispatcher;

    public OrderListener(InventoryRepository repository,
                         @Value("${app.nats.url:nats://nats:4222}") String natsUrl,
                         @Value("${app.nats.subject:orders.created}") String subject) {
        this.repository = repository;
        this.natsUrl = natsUrl;
        this.subject = subject;
    }

    @PostConstruct
    void subscribe() {
        try {
            Options options = new Options.Builder()
                    .server(natsUrl)
                    .connectionName("java-backend")
                    .connectionTimeout(Duration.ofSeconds(3))
                    .maxReconnects(-1)
                    .build();
            connection = Nats.connect(options);
            dispatcher = connection.createDispatcher(msg -> handle(msg.getData()));
            dispatcher.subscribe(subject);
            log.info("подписка на {} по адресу {}", subject, natsUrl);
        } catch (Exception e) {
            // Сервис остаётся работоспособным без NATS: HTTP-часть не должна
            // зависеть от брокера, иначе половина стенда не поднимется.
            log.warn("подписка на NATS не удалась: {}", e.getMessage());
        }
    }

    void handle(byte[] payload) {
        try {
            Order order = mapper.readValue(payload, Order.class);
            repository.saveOrder(order.orderId(), order.sku(), order.quantity(), order.reserved());
            log.info("заказ сохранён order_id={} sku={}", order.orderId(), order.sku());
        } catch (Exception e) {
            log.error("событие не обработано: {}", e.getMessage());
        }
    }

    @PreDestroy
    void close() {
        try {
            if (dispatcher != null && connection != null) {
                connection.closeDispatcher(dispatcher);
            }
            if (connection != null) {
                connection.close();
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
