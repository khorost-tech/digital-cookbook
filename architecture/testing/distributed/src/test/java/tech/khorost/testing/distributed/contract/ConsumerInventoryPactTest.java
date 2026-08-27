package tech.khorost.testing.distributed.contract;

import au.com.dius.pact.consumer.MockServer;
import au.com.dius.pact.consumer.dsl.PactDslJsonBody;
import au.com.dius.pact.consumer.dsl.PactDslWithProvider;
import au.com.dius.pact.consumer.junit5.PactConsumerTestExt;
import au.com.dius.pact.consumer.junit5.PactTestFor;
import au.com.dius.pact.core.model.PactSpecVersion;
import au.com.dius.pact.core.model.RequestResponsePact;
import au.com.dius.pact.core.model.annotations.Pact;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;

/**
 * Consumer-сторона consumer-driven contract. Consumer (OrderService) описывает,
 * что он ждёт от провайдера (InventoryService), Pact поднимает mock-сервер по
 * этому описанию, и мы прогоняем через него реальный клиент InventoryClient.
 *
 * Побочный эффект прогона: Pact пишет файл контракта в target/pacts
 * (OrderService-InventoryService.json) — его затем верифицирует provider-тест.
 */
@ExtendWith(PactConsumerTestExt.class)
@PactTestFor(providerName = "InventoryService", pactVersion = PactSpecVersion.V3)
class ConsumerInventoryPactTest {

    @Pact(consumer = "OrderService", provider = "InventoryService")
    RequestResponsePact inventoryPact(PactDslWithProvider builder) {
        return builder
                .given("inventory exists for WIDGET-1")
                .uponReceiving("a request for inventory of WIDGET-1")
                .path("/inventory/WIDGET-1")
                .method("GET")
                .willRespondWith()
                .status(200)
                .headers(Map.of("Content-Type", "application/json"))
                .body(new PactDslJsonBody()
                        // sku — точное значение; available — матчер по типу (любое число).
                        .stringValue("sku", "WIDGET-1")
                        .integerType("available", 42))
                .toPact();
    }

    @Test
    @PactTestFor(pactMethod = "inventoryPact")
    void consumerReadsAvailable(MockServer mockServer) throws Exception {
        InventoryClient client = new InventoryClient(mockServer.getUrl());

        int available = client.availableFor("WIDGET-1");

        assertEquals(42, available, "клиент читает поле available из ответа провайдера");
    }
}
