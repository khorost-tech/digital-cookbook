package tech.khorost.highload;

import io.helidon.webserver.WebServer;
import io.helidon.webserver.http.HttpRouting;
import io.helidon.webserver.http.ServerRequest;
import io.helidon.webserver.http.ServerResponse;

import java.io.IOException;
import java.io.InputStream;
import java.time.Duration;
import java.util.concurrent.ThreadLocalRandom;
import java.util.concurrent.atomic.AtomicLong;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * highload-java-backend — эталонный Java-бэкенд стенда highload-lowlatency.
 *
 * Транспорт: HTTP/2 cleartext (h2c). Helidon 4 WebServer (Nima) обрабатывает
 * каждый запрос на отдельном virtual thread по умолчанию — без явной настройки
 * пулов потоков. Модуль {@code helidon-webserver-http2} регистрируется через
 * ServiceLoader/SPI и добавляет HTTP/2 upgrade codec: сервер принимает
 * HTTP/2-соединения как через Upgrade (h2), так и по prior-knowledge (h2c без
 * Upgrade-заголовка) на одном и том же plaintext-порту — именно так, как
 * HAProxy обращается к бэкенду директивой {@code server ... proto h2}.
 * Никакого TLS, никакой дополнительной конфигурации для h2c не требуется:
 * присутствия зависимости на classpath достаточно.
 *
 * Контракт (см. performance/highload-lowlatency/README.md):
 * {@code POST /check} — имитация синхронной проверки полезной нагрузки
 * (случайная задержка 100-200мс), ответ JSON с эхом request_id, backend,
 * runtime="java", check_ms, in_flight_peak. {@code GET /healthz} — 200 "ok".
 */
public final class Main {

    private static final String DEFAULT_LISTEN_ADDR = ":9000";
    private static final String DEFAULT_BACKEND = "java-?";

    private static final long SIM_MIN_DELAY_MS = 100L;
    // rand.nextLong(101) -> 0..100 включительно, миллисекунды джиттера.
    private static final long SIM_MAX_JITTER_MS = 101L;

    // Извлекаем "request_id":"<uuid>" из тела запроса без полного JSON-парсинга —
    // остальная часть payload (items, ~8КБ паддинга) нам не нужна, только эхо id.
    private static final Pattern REQUEST_ID_PATTERN =
            Pattern.compile("\"request_id\"\\s*:\\s*\"([^\"]*)\"");

    private Main() {
    }

    public static void main(String[] args) {
        String backendName = System.getenv("BACKEND_NAME");
        if (backendName == null || backendName.isBlank()) {
            backendName = DEFAULT_BACKEND;
            System.out.println("warning: BACKEND_NAME is not set, using default \"" + backendName + "\"");
        }

        String listenAddr = System.getenv("LISTEN_ADDR");
        if (listenAddr == null || listenAddr.isBlank()) {
            listenAddr = DEFAULT_LISTEN_ADDR;
        }

        HostPort hostPort = HostPort.parse(listenAddr);
        CheckHandler handler = new CheckHandler(backendName);

        WebServer server = WebServer.builder()
                .host(hostPort.host())
                .port(hostPort.port())
                .routing(routing -> routing
                        .post("/check", handler::handleCheck)
                        .get("/healthz", Main::handleHealthz))
                .build()
                .start();

        System.out.println("highload-java-backend \"" + backendName + "\" listening on "
                + listenAddr + " (h2c), port=" + server.port());
    }

    /** GET /healthz — простой liveness/readiness ответ для HAProxy httpchk. */
    private static void handleHealthz(ServerRequest req, ServerResponse res) {
        res.header("Content-Type", "text/plain");
        res.send("ok");
    }

    /**
     * CheckHandler инкапсулирует состояние одного инстанса бэкенда: имя и
     * счётчики in-flight запросов (текущее значение + исторический пик).
     */
    private static final class CheckHandler {

        private final String backendName;
        private final AtomicLong inFlight = new AtomicLong();
        private final AtomicLong inFlightPeak = new AtomicLong();

        private CheckHandler(String backendName) {
            this.backendName = backendName;
        }

        /**
         * POST /check — имитация синхронной проверки payload: читает и
         * отбрасывает тело (~8КБ), спит случайные 100-200мс, отвечает JSON
         * согласно контракту. Выполняется на virtual thread — блокирующий
         * Thread.sleep() здесь дёшев и не занимает платформенный поток.
         */
        private void handleCheck(ServerRequest req, ServerResponse res) throws IOException {
            long current = inFlight.incrementAndGet();
            try {
                bumpPeak(current);

                String body = readBody(req);
                String requestId = extractRequestId(body);

                long delayMs = SIM_MIN_DELAY_MS + ThreadLocalRandom.current().nextLong(SIM_MAX_JITTER_MS);
                long start = System.nanoTime();
                sleep(delayMs);
                long checkMs = Duration.ofNanos(System.nanoTime() - start).toMillis();

                String json = "{"
                        + "\"request_id\":\"" + escape(requestId) + "\","
                        + "\"backend\":\"" + escape(backendName) + "\","
                        + "\"runtime\":\"java\","
                        + "\"check_ms\":" + checkMs + ","
                        + "\"in_flight_peak\":" + inFlightPeak.get()
                        + "}";

                res.header("Content-Type", "application/json");
                res.send(json);
            } finally {
                inFlight.decrementAndGet();
            }
        }

        /** bumpPeak атомарно поднимает inFlightPeak до current через CAS-цикл. */
        private void bumpPeak(long current) {
            long peak;
            do {
                peak = inFlightPeak.get();
                if (current <= peak) {
                    return;
                }
            } while (!inFlightPeak.compareAndSet(peak, current));
        }

        /**
         * readBody читает и полностью отбрасывает тело запроса (~8КБ payload
         * с request_id + items), возвращая его как UTF-8 строку. Читаем напрямую
         * из {@link InputStream}, не полагаясь на media-support модули для
         * String/JSON — достаточно базового {@code helidon-webserver}.
         */
        private static String readBody(ServerRequest req) throws IOException {
            if (!req.content().hasEntity()) {
                return "";
            }
            try (InputStream in = req.content().inputStream()) {
                return new String(in.readAllBytes(), java.nio.charset.StandardCharsets.UTF_8);
            }
        }

        private static String extractRequestId(String body) {
            if (body == null) {
                return "";
            }
            Matcher m = REQUEST_ID_PATTERN.matcher(body);
            return m.find() ? m.group(1) : "";
        }

        private static void sleep(long millis) {
            try {
                Thread.sleep(millis);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
        }

        private static String escape(String s) {
            return s.replace("\\", "\\\\").replace("\"", "\\\"");
        }
    }

    /** HostPort разбирает LISTEN_ADDR вида ":9000" или "0.0.0.0:9000" в host+port. */
    private record HostPort(String host, int port) {

        static HostPort parse(String addr) {
            int idx = addr.lastIndexOf(':');
            if (idx < 0) {
                return new HostPort("0.0.0.0", Integer.parseInt(addr));
            }
            String host = addr.substring(0, idx);
            int port = Integer.parseInt(addr.substring(idx + 1));
            if (host.isBlank()) {
                host = "0.0.0.0";
            }
            return new HostPort(host, port);
        }
    }
}
