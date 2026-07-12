package tech.khorost.highload;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.NoSuchFileException;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicLong;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * highload-java-client — нагрузочный клиент стенда highload-lowlatency,
 * аналог Go-клиента (см. {@code clients/go/main.go}), но на
 * {@link java.net.http.HttpClient} (JDK 21).
 *
 * Держит пул из {@code CONNS} переиспользуемых {@code HttpClient}, каждый —
 * отдельный H2-транспорт/соединение, бьёт {@code POST /check} через HAProxy,
 * собирает latency p50/p95/p99 + min/max и распределение ответов по полю
 * {@code backend} (контракт — performance/highload-lowlatency/README.md).
 *
 * ИЗВЕСТНОЕ ОГРАНИЧЕНИЕ JDK (проверено эмпирически, см. отчёт задачи 6):
 * {@code java.net.http.HttpClient} НЕ умеет h2c prior-knowledge. Для
 * {@code http://} с {@code .version(HTTP_2)} первый запрос на новом
 * соединении всегда уходит как HTTP/1.1 с заголовками
 * {@code Connection: Upgrade, HTTP2-Settings} / {@code Upgrade: h2c}
 * (RFC 7540 §3.2 upgrade-механизм) — а не как h2c-преамбула
 * {@code PRI * HTTP/2.0}. Дальнейшее поведение зависит от сервера:
 * <ul>
 *   <li>Go-бэкенд стенда ({@code golang.org/x/net/http2/h2c.NewHandler})
 *       понимает Upgrade-механизм — апгрейдится, следующие запросы на этом
 *       соединении реально идут по HTTP/2 ({@link HttpResponse#version()}
 *       возвращает {@code HTTP_2}).</li>
 *   <li>Java-бэкенд стенда (Helidon {@code webserver-http2}) поддерживает
 *       только prior-knowledge, Upgrade-заголовки молча игнорирует и отвечает
 *       200 по HTTP/1.1 — соединение остаётся на {@code HTTP_1_1}.</li>
 *   <li>HAProxy с {@code bind :8080 proto h2} (реальный TARGET стенда)
 *       принимает на этом listener ТОЛЬКО h2c prior-knowledge и не понимает
 *       HTTP/1.1-Upgrade — получив Upgrade-запрос, отвечает {@code <BADREQ>}
 *       и рвёт соединение. Итог: этот клиент не может пройти h2c-фронтенд
 *       HAProxy стенда как есть; см. отчёт задачи для деталей и возможных
 *       обходных путей.</li>
 * </ul>
 * Пул из {@code CONNS} клиентов и виртуально-поточный параллелизм
 * демонстрируются в любом случае (клиент работает против бэкендов напрямую
 * или через L4-прокси); фактическая версия протокола ответа логируется
 * через {@link HttpResponse#version()} и печатается в отчёте.
 */
public final class Client {

    private static final String DEFAULT_TARGET = "http://haproxy:8080/check";
    private static final int DEFAULT_CONCURRENCY = 200;
    private static final int DEFAULT_REQUESTS = 5000;
    private static final int DEFAULT_CONNS = 4;
    private static final int DEFAULT_TIMEOUT_MS = 280;
    private static final String DEFAULT_PAYLOAD_PATH = "/payload/sample-request.json";

    // Используется, если PAYLOAD-файл не найден — клиент всё равно должен
    // запускаться отдельно от полной топологии стенда (симметрично Go-клиенту).
    private static final String FALLBACK_PAYLOAD = """
            {
              "request_id": "00000000-0000-0000-0000-000000000000",
              "issued_at": "2026-01-01T00:00:00Z",
              "items": [
                {"id": 1, "code": "ITEM-0001", "value": 1.0, "note": "placeholder-payload-used-because-no-PAYLOAD-file-was-found"}
              ]
            }""";

    private static final Pattern BACKEND_PATTERN =
            Pattern.compile("\"backend\"\\s*:\\s*\"([^\"]*)\"");

    private Client() {
    }

    public static void main(String[] args) throws InterruptedException {
        Config cfg;
        try {
            cfg = Config.fromEnv();
        } catch (IllegalArgumentException e) {
            System.err.println("config error: " + e.getMessage());
            System.exit(1);
            return;
        }

        System.out.printf(
                "highload-java-client: target=%s concurrency=%d requests=%d conns=%d timeout=%dms payload_bytes=%d%n",
                cfg.target(), cfg.concurrency(), cfg.requests(), cfg.conns(), cfg.timeoutMs(), cfg.payload().length);

        List<PooledClient> pool = new ArrayList<>(cfg.conns());
        for (int i = 0; i < cfg.conns(); i++) {
            pool.add(new PooledClient(newH2CHttpClient()));
        }

        Results results = new Results();
        AtomicLong nextRequest = new AtomicLong(0);
        CountDownLatch done = new CountDownLatch(cfg.concurrency());

        Instant start = Instant.now();
        try (ExecutorService executor = Executors.newVirtualThreadPerTaskExecutor()) {
            for (int w = 0; w < cfg.concurrency(); w++) {
                executor.submit(() -> {
                    try {
                        while (true) {
                            long idx = nextRequest.getAndIncrement();
                            if (idx >= cfg.requests()) {
                                return;
                            }
                            PooledClient pc = pickLeastInFlight(pool);
                            results.record(doRequest(pc, cfg));
                        }
                    } finally {
                        done.countDown();
                    }
                });
            }
            done.await();
        }
        Duration elapsed = Duration.between(start, Instant.now());

        printReport(cfg, results, elapsed);
    }

    /**
     * newH2CHttpClient строит {@code HttpClient} с версией HTTP_2. Каждый
     * член пула — отдельный экземпляр {@code HttpClient} (своя пара
     * коннекшн-пул/executor внутри JDK), что и демонстрирует несколько
     * независимых соединений (ключевой момент статьи про JVM-клиент).
     *
     * ВАЖНО: см. class-level javadoc — {@code .version(HTTP_2)} для
     * {@code http://} задаёт лишь попытку HTTP/1.1-Upgrade на h2c, а не
     * prior-knowledge; фактическая версия зависит от сервера на другом конце.
     */
    private static HttpClient newH2CHttpClient() {
        return HttpClient.newBuilder()
                .version(HttpClient.Version.HTTP_2)
                .connectTimeout(Duration.ofSeconds(2))
                .build();
    }

    /**
     * pickLeastInFlight выбирает клиента пула с наименьшим числом активных
     * запросов, чтобы CONCURRENCY воркеров равномерно распределялись по
     * CONNS соединениям, а не наваливались на одно. При равенстве —
     * наименьший индекс (стабильно под нагрузкой, т.к. inFlight убывает по
     * завершении запросов).
     */
    private static PooledClient pickLeastInFlight(List<PooledClient> pool) {
        PooledClient best = pool.get(0);
        long bestLoad = best.inFlight.get();
        for (int i = 1; i < pool.size(); i++) {
            PooledClient pc = pool.get(i);
            long load = pc.inFlight.get();
            if (load < bestLoad) {
                best = pc;
                bestLoad = load;
            }
        }
        return best;
    }

    /** doRequest выполняет один POST /check через данное соединение пула. */
    private static RequestOutcome doRequest(PooledClient pc, Config cfg) {
        pc.inFlight.incrementAndGet();
        try {
            HttpRequest request = HttpRequest.newBuilder()
                    .uri(URI.create(cfg.target()))
                    .timeout(Duration.ofMillis(cfg.timeoutMs()))
                    .header("Content-Type", "application/json")
                    .POST(HttpRequest.BodyPublishers.ofByteArray(cfg.payload()))
                    .build();

            Instant reqStart = Instant.now();
            HttpResponse<String> response;
            try {
                response = pc.client.send(request, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
            } catch (java.net.http.HttpTimeoutException e) {
                return RequestOutcome.timeout();
            } catch (IOException e) {
                return RequestOutcome.error();
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return RequestOutcome.error();
            }

            if (response.statusCode() != 200) {
                return RequestOutcome.error();
            }

            String backend = extractBackend(response.body());
            Duration latency = Duration.between(reqStart, Instant.now());

            return RequestOutcome.success(latency, backend, response.version());
        } finally {
            pc.inFlight.decrementAndGet();
        }
    }

    private static String extractBackend(String body) {
        if (body == null) {
            return "";
        }
        Matcher m = BACKEND_PATTERN.matcher(body);
        return m.find() ? m.group(1) : "";
    }

    /** printReport печатает latency-статистику, исходы запросов и таблицу backend->count. */
    private static void printReport(Config cfg, Results results, Duration elapsed) {
        Results.Snapshot snap = results.snapshot();
        int total = snap.successes() + snap.timeouts() + snap.errors();

        System.out.println();
        System.out.println("=== highload-java-client report ===");
        System.out.printf("target:       %s%n", cfg.target());
        System.out.printf("requests:     %d (concurrency=%d, conns=%d, timeout=%dms)%n",
                cfg.requests(), cfg.concurrency(), cfg.conns(), cfg.timeoutMs());
        System.out.printf("elapsed:      %s%n", elapsed);
        System.out.printf("protocol:     %s%n", "[]".equals(snap.protocolVersions()) ? "n/a" : snap.protocolVersions());
        System.out.println();

        System.out.println("--- outcomes ---");
        System.out.printf("total:        %d%n", total);
        System.out.printf("successes:    %d%n", snap.successes());
        System.out.printf("timeouts:     %d%n", snap.timeouts());
        System.out.printf("errors:       %d%n", snap.errors());
        System.out.println();

        System.out.println("--- latency (successful requests only) ---");
        List<Duration> latencies = snap.latencies();
        if (latencies.isEmpty()) {
            System.out.println("no successful requests, no latency stats available");
        } else {
            List<Duration> sorted = new ArrayList<>(latencies);
            sorted.sort(Duration::compareTo);

            System.out.printf("min:          %s%n", sorted.get(0));
            System.out.printf("p50:          %s%n", percentile(sorted, 50));
            System.out.printf("p95:          %s%n", percentile(sorted, 95));
            System.out.printf("p99:          %s%n", percentile(sorted, 99));
            System.out.printf("max:          %s%n", sorted.get(sorted.size() - 1));
        }
        System.out.println();

        System.out.println("--- backend distribution ---");
        Map<String, Integer> hits = snap.backendHits();
        if (hits.isEmpty()) {
            System.out.println("no successful responses to attribute to a backend");
        } else {
            System.out.printf("%-24s %10s %8s%n", "backend", "requests", "share");
            for (Map.Entry<String, Integer> e : hits.entrySet()) {
                double share = (double) e.getValue() / snap.successes() * 100;
                System.out.printf("%-24s %10d %7.2f%%%n", e.getKey(), e.getValue(), share);
            }
        }
    }

    /** percentile — p-й перцентиль (0-100) отсортированного списка задержек, near-rank интерполяция. */
    private static Duration percentile(List<Duration> sorted, double p) {
        if (sorted.isEmpty()) {
            return Duration.ZERO;
        }
        if (sorted.size() == 1) {
            return sorted.get(0);
        }
        double rank = p / 100 * (sorted.size() - 1);
        int lo = (int) rank;
        int hi = lo + 1;
        if (hi >= sorted.size()) {
            return sorted.get(sorted.size() - 1);
        }
        double frac = rank - lo;
        Duration loD = sorted.get(lo);
        Duration hiD = sorted.get(hi);
        long nanos = loD.toNanos() + (long) ((hiD.toNanos() - loD.toNanos()) * frac);
        return Duration.ofNanos(nanos);
    }

    /** PooledClient — один член пула: HttpClient + счётчик in-flight запросов. */
    private static final class PooledClient {
        private final HttpClient client;
        private final AtomicLong inFlight = new AtomicLong();

        private PooledClient(HttpClient client) {
            this.client = client;
        }
    }

    /** RequestOutcome — результат одного запроса: latency+backend+version (success) либо категория неудачи. */
    private record RequestOutcome(Kind kind, Duration latency, String backend, HttpClient.Version version) {

        enum Kind { SUCCESS, TIMEOUT, ERROR }

        static RequestOutcome success(Duration latency, String backend, HttpClient.Version version) {
            return new RequestOutcome(Kind.SUCCESS, latency, backend, version);
        }

        static RequestOutcome timeout() {
            return new RequestOutcome(Kind.TIMEOUT, null, null, null);
        }

        static RequestOutcome error() {
            return new RequestOutcome(Kind.ERROR, null, null, null);
        }
    }

    /** Results накапливает исходы всех воркеров под синхронизацией. */
    private static final class Results {
        private final List<Duration> latencies = new ArrayList<>();
        private final Map<String, Integer> backendHits = new TreeMap<>();
        private final Set<HttpClient.Version> protocolVersions = ConcurrentHashMap.newKeySet();
        private int successes;
        private int timeouts;
        private int errors;

        synchronized void record(RequestOutcome o) {
            switch (o.kind()) {
                case SUCCESS -> {
                    successes++;
                    latencies.add(o.latency());
                    backendHits.merge(o.backend(), 1, Integer::sum);
                    protocolVersions.add(o.version());
                }
                case TIMEOUT -> timeouts++;
                case ERROR -> errors++;
            }
        }

        synchronized Snapshot snapshot() {
            return new Snapshot(
                    List.copyOf(latencies),
                    Map.copyOf(backendHits),
                    successes,
                    timeouts,
                    errors,
                    new TreeSet<>(protocolVersions).toString());
        }

        record Snapshot(List<Duration> latencies, Map<String, Integer> backendHits,
                         int successes, int timeouts, int errors, String protocolVersions) {
        }
    }

    /** Config — конфигурация клиента, полностью из env-переменных. */
    private record Config(String target, int concurrency, int requests, int conns, int timeoutMs, byte[] payload) {

        static Config fromEnv() {
            String target = getEnvDefault("TARGET", DEFAULT_TARGET);
            int concurrency = getEnvIntDefault("CONCURRENCY", DEFAULT_CONCURRENCY);
            int requests = getEnvIntDefault("REQUESTS", DEFAULT_REQUESTS);
            int conns = getEnvIntDefault("CONNS", DEFAULT_CONNS);
            int timeoutMs = getEnvIntDefault("TIMEOUT_MS", DEFAULT_TIMEOUT_MS);
            byte[] payload = loadPayload(getEnvDefault("PAYLOAD", DEFAULT_PAYLOAD_PATH));

            if (concurrency <= 0) {
                throw new IllegalArgumentException("CONCURRENCY must be positive, got " + concurrency);
            }
            if (requests <= 0) {
                throw new IllegalArgumentException("REQUESTS must be positive, got " + requests);
            }
            if (conns <= 0) {
                throw new IllegalArgumentException("CONNS must be positive, got " + conns);
            }
            if (timeoutMs <= 0) {
                throw new IllegalArgumentException("TIMEOUT_MS must be positive, got " + timeoutMs);
            }

            return new Config(target, concurrency, requests, conns, timeoutMs, payload);
        }

        private static byte[] loadPayload(String path) {
            try {
                return Files.readAllBytes(Path.of(path));
            } catch (NoSuchFileException e) {
                System.out.printf("warning: payload file %s not found, using built-in fallback payload%n", path);
                return FALLBACK_PAYLOAD.getBytes(StandardCharsets.UTF_8);
            } catch (IOException e) {
                throw new IllegalArgumentException("reading payload " + path + ": " + e.getMessage(), e);
            }
        }

        private static String getEnvDefault(String name, String def) {
            String v = System.getenv(name);
            return (v == null || v.isEmpty()) ? def : v;
        }

        private static int getEnvIntDefault(String name, int def) {
            String v = System.getenv(name);
            if (v == null || v.isEmpty()) {
                return def;
            }
            try {
                return Integer.parseInt(v);
            } catch (NumberFormatException e) {
                throw new IllegalArgumentException("env " + name + ": invalid integer \"" + v + "\"", e);
            }
        }
    }
}
