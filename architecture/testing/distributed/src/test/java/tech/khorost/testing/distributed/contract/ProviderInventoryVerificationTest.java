package tech.khorost.testing.distributed.contract;

import au.com.dius.pact.provider.junit5.HttpTestTarget;
import au.com.dius.pact.provider.junit5.PactVerificationContext;
import au.com.dius.pact.provider.junit5.PactVerificationInvocationContextProvider;
import au.com.dius.pact.provider.junitsupport.Provider;
import au.com.dius.pact.provider.junitsupport.State;
import au.com.dius.pact.provider.junitsupport.loader.PactFolder;
import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.TestTemplate;
import org.junit.jupiter.api.extension.ExtendWith;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;

/**
 * Provider-сторона. Поднимаем настоящий HTTP-сервер провайдера (на голом
 * com.sun.net.httpserver, без внешних зависимостей) и прогоняем против него
 * контракт из src/test/resources/pacts. Pact воспроизводит каждый запрос из
 * пакта и сверяет ответ с ожиданиями consumer'а.
 *
 * Как ловится несовместимость (см. README): если сервер ниже вернёт статус,
 * отличный от 200, уберёт поле "sku"/"available" или сменит тип available на
 * строку — context.verifyInteraction() провалит тест. Это и есть страховка
 * от молчаливой поломки контракта между сервисами.
 */
@Provider("InventoryService")
@PactFolder("src/test/resources/pacts")
class ProviderInventoryVerificationTest {

    private static HttpServer server;
    private static int port;

    @BeforeAll
    static void startProvider() throws IOException {
        server = HttpServer.create(new InetSocketAddress("localhost", 0), 0);
        server.createContext("/inventory/WIDGET-1", exchange -> {
            // Ответ провайдера, совместимый с контрактом: статус 200,
            // поле sku == "WIDGET-1", поле available — число.
            byte[] body = "{\"sku\":\"WIDGET-1\",\"available\":17}".getBytes(StandardCharsets.UTF_8);
            exchange.getResponseHeaders().add("Content-Type", "application/json");
            exchange.sendResponseHeaders(200, body.length);
            try (OutputStream os = exchange.getResponseBody()) {
                os.write(body);
            }
        });
        server.start();
        port = server.getAddress().getPort();
    }

    @AfterAll
    static void stopProvider() {
        if (server != null) {
            server.stop(0);
        }
    }

    @BeforeEach
    void bindTarget(PactVerificationContext context) {
        context.setTarget(new HttpTestTarget("localhost", port));
    }

    @TestTemplate
    @ExtendWith(PactVerificationInvocationContextProvider.class)
    void verifyContract(PactVerificationContext context) {
        context.verifyInteraction();
    }

    @State("inventory exists for WIDGET-1")
    void inventoryExists() {
        // Состояние провайдера. Здесь ставился бы фикстур-данные под контракт;
        // наш сервер отдаёт статичный ответ, отдельная подготовка не нужна.
    }
}
