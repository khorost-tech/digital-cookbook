package tech.khorost.productionpatterns;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

/**
 * Свой liveness/readiness — без Spring Actuator (plain-Java стенд).
 *   /health/live  — 200, пока процесс жив (не зависит от readiness).
 *   /health/ready — 200, пока сервис принимает нагрузку; 503 во время graceful drain
 *                   (readiness падает раньше, чем перестают приниматься новые задачи —
 *                   так балансировщик в реальной системе успевает вывести под из ротации
 *                   до того, как под откажется отвечать).
 */
public class HealthServer {

    private static final Logger log = LoggerFactory.getLogger("HealthServer");

    private final HttpServer server;
    private final ExecutorService executor;
    private final AtomicBoolean ready = new AtomicBoolean(true);

    public HealthServer(int port) throws IOException {
        server = HttpServer.create(new InetSocketAddress(port), 0);
        server.createContext("/health/live", ex -> respond(ex, 200, "LIVE"));
        server.createContext("/health/ready", ex -> {
            boolean r = ready.get();
            respond(ex, r ? 200 : 503, r ? "READY" : "NOT_READY (draining)");
        });
        executor = Executors.newFixedThreadPool(2);
        server.setExecutor(executor);
    }

    private void respond(HttpExchange ex, int status, String body) throws IOException {
        byte[] bytes = body.getBytes(StandardCharsets.UTF_8);
        ex.sendResponseHeaders(status, bytes.length);
        try (OutputStream os = ex.getResponseBody()) {
            os.write(bytes);
        }
    }

    public void start() {
        server.start();
        log.info("[Health] listening on port {}", server.getAddress().getPort());
    }

    public void setReady(boolean value) {
        ready.set(value);
        log.warn("[Health] readiness = {}", value);
    }

    public void stop() {
        server.stop(0);
        executor.shutdown();
        try {
            if (!executor.awaitTermination(2, TimeUnit.SECONDS)) {
                executor.shutdownNow();
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            executor.shutdownNow();
        }
    }
}
