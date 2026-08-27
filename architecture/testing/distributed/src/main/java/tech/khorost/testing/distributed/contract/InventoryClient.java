package tech.khorost.testing.distributed.contract;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;

/**
 * HTTP-клиент consumer'а к сервису склада (InventoryService). Именно его
 * поведение фиксирует consumer-driven contract: consumer-тест поднимает
 * mock-сервер Pact, дёргает этот клиент и записывает ожидаемый контракт.
 *
 * Тело парсится примитивно (без Jackson), чтобы стенд не тянул лишних
 * зависимостей — для демонстрации контракта этого достаточно.
 */
public final class InventoryClient {

    private final HttpClient http = HttpClient.newHttpClient();
    private final String baseUrl;

    public InventoryClient(String baseUrl) {
        this.baseUrl = baseUrl;
    }

    /** Возвращает доступный остаток по SKU (поле "available" из ответа). */
    public int availableFor(String sku) throws IOException, InterruptedException {
        HttpRequest request = HttpRequest.newBuilder()
                .uri(URI.create(baseUrl + "/inventory/" + sku))
                .GET()
                .build();
        HttpResponse<String> response = http.send(request, HttpResponse.BodyHandlers.ofString());
        if (response.statusCode() != 200) {
            throw new IOException("unexpected status: " + response.statusCode());
        }
        return extractInt(response.body(), "available");
    }

    /** Достаёт целочисленное значение поля из плоского JSON без внешних библиотек. */
    private static int extractInt(String json, String field) {
        String needle = "\"" + field + "\"";
        int at = json.indexOf(needle);
        if (at < 0) {
            throw new IllegalStateException("field not found in response: " + field);
        }
        int colon = json.indexOf(':', at + needle.length());
        int i = colon + 1;
        while (i < json.length() && !Character.isDigit(json.charAt(i)) && json.charAt(i) != '-') {
            i++;
        }
        int start = i;
        if (i < json.length() && json.charAt(i) == '-') {
            i++;
        }
        while (i < json.length() && Character.isDigit(json.charAt(i))) {
            i++;
        }
        return Integer.parseInt(json.substring(start, i));
    }
}
