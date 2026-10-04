package tech.khorost.observability;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.server.ResponseStatusException;

@RestController
public class InventoryController {

    private static final Logger log = LoggerFactory.getLogger(InventoryController.class);

    private final InventoryRepository repository;

    public InventoryController(InventoryRepository repository) {
        this.repository = repository;
    }

    @GetMapping("/inventory/{sku}")
    public ResponseEntity<Item> lookup(@PathVariable String sku) {
        if (InventoryRepository.FAILING_SKU.equals(sku)) {
            // Намеренный отказ: нужен, чтобы в стенде были ошибочные трейсы для
            // tail sampling (ст. 2) и ненулевой error rate для алертов (ст. 5).
            log.error("обработка sku {} прервана намеренно", sku);
            throw new ResponseStatusException(HttpStatus.INTERNAL_SERVER_ERROR, "обработка недоступна");
        }

        return repository.findBySku(sku)
                .map(item -> {
                    log.info("найден товар sku={} quantity={}", item.sku(), item.quantity());
                    return ResponseEntity.ok(item);
                })
                .orElseGet(() -> {
                    log.warn("товар не найден sku={}", sku);
                    return ResponseEntity.notFound().build();
                });
    }

    /** Служебный эндпоинт: сколько заказов сохранил подписчик NATS. Нужен проверкам стенда. */
    @GetMapping("/orders/count")
    public ResponseEntity<Long> ordersCount() {
        return ResponseEntity.ok(repository.countOrders());
    }
}
