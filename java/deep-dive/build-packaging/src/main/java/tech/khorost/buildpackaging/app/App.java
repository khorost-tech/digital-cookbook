package tech.khorost.buildpackaging.app;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import tech.khorost.buildpackaging.lib.ResponseFormatter;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;

/**
 * Стенд build-packaging: один и тот же простой HTTP-сервис ("hello" +
 * /health), собираемый и упаковываемый 5 разными способами — fat-jar,
 * layered-jar, JVM+AppCDS, GraalVM native-image, jlink custom runtime.
 * Числа (startup/RSS/размер) снимаются снаружи (см. bench.sh) — сам сервис
 * не измеряет собственный startup, чтобы не тянуть java.management в
 * native-image/jlink-образ и не искажать сравнение.
 *
 * Только JDK API (java.base + jdk.httpserver) — без внешних зависимостей,
 * чтобы native-image/jlink не спотыкались о reflection-конфиги сторонних
 * библиотек (риск GraalVM native с внешними зависимостями).
 */
public final class App {

    public static void main(String[] args) throws IOException {
        int port = Integer.parseInt(System.getenv().getOrDefault("PORT", "8080"));
        String mode = System.getenv().getOrDefault("MODE", "unknown");

        HttpServer server = HttpServer.create(new InetSocketAddress(port), 0);
        server.createContext("/health", exchange -> respond(exchange, 200, ResponseFormatter.health()));
        server.createContext("/", exchange -> respond(exchange, 200, ResponseFormatter.hello(mode)));
        server.setExecutor(null); // дефолтный executor — сервис однопоточный по умолчанию, для стенда достаточно
        server.start();

        // Простой маркер готовности в лог — для визуальной проверки живьём.
        // Точные числа старта снимаются снаружи через опрос /health (bench.sh).
        System.out.println("Started build-packaging service, mode=" + mode + ", port=" + port);
    }

    private static void respond(HttpExchange exchange, int status, String body) throws IOException {
        byte[] bytes = body.getBytes(StandardCharsets.UTF_8);
        exchange.getResponseHeaders().add("Content-Type", "text/plain; charset=utf-8");
        exchange.sendResponseHeaders(status, bytes.length);
        try (OutputStream os = exchange.getResponseBody()) {
            os.write(bytes);
        }
    }
}
