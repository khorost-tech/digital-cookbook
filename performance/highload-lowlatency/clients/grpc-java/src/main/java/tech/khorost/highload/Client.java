package tech.khorost.highload;

import highload.Check.CheckRequest;
import highload.Check.CheckResponse;
import highload.Check.Item;
import highload.CheckServiceGrpc;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Status;
import io.grpc.StatusRuntimeException;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.NoSuchFileException;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * highload-grpc-java-client — нагрузочный клиент стенда highload-lowlatency,
 * gRPC-аналог REST-клиента {@code clients/java} (см. {@code Client.java}
 * там) и Go-эквивалента {@code clients/grpc-go/main.go}, но поверх
 * {@code io.grpc.ManagedChannel} вместо {@link java.net.http.HttpClient}.
 *
 * <p>КЛЮЧЕВОЙ МОМЕНТ СТАТЬИ: {@link ManagedChannelBuilder#usePlaintext()} —
 * это h2c prior-knowledge (клиент сразу шлёт преамбулу {@code PRI * HTTP/2.0}
 * без HTTP/1.1-Upgrade), а не HTTP/1.1-Upgrade-механизм, которым
 * {@code java.net.http.HttpClient} пытается перейти на h2c при
 * {@code .version(HTTP_2)} для {@code http://}-URI (см. javadoc REST-клиента
 * и отчёт задачи 6 стенда). Поэтому там, где REST Java-клиент не может пройти
 * h2c-only фронтенд HAProxy ({@code proto h2}, только prior-knowledge, без
 * поддержки Upgrade) — этот gRPC-клиент проходит его без проблем: у gRPC
 * cleartext-транспорт по умолчанию всегда prior-knowledge, JDK-специфичной
 * проблемы тут в принципе нет.
 *
 * <p>Пул из {@code CONNS} независимых {@code ManagedChannel} (каждый — своё
 * HTTP/2-соединение), least-inflight-распределение (как у REST-клиента и
 * Go gRPC-клиента), параллелизм на виртуальных потоках
 * ({@link Executors#newVirtualThreadPerTaskExecutor()}), per-call deadline
 * через {@code withDeadlineAfter}, статистика p50/p95/p99/min/max + таблица
 * backend → count.
 */
public final class Client {

    private static final String DEFAULT_TARGET = "haproxy:8090";
    private static final int DEFAULT_CONCURRENCY = 200;
    private static final int DEFAULT_REQUESTS = 5000;
    private static final int DEFAULT_CONNS = 4;
    private static final int DEFAULT_TIMEOUT_MS = 280;
    private static final String DEFAULT_PAYLOAD_PATH = "/payload/sample-request.json";

    // Тот же ~8KB JSON payload, что используют REST-клиенты (clients/go,
    // clients/java) и Go gRPC-клиент (clients/grpc-go) — читается один раз
    // при старте и перепаковывается в CheckRequest (см. buildRequest), а не
    // генерируется отдельным путём для gRPC. Так размер и форма тела запроса
    // совпадают у REST и gRPC клиентов стенда, что и нужно для честного
    // сравнения транспортов на одном стенде.
    private static final String FALLBACK_PAYLOAD = """
            {
              "request_id": "00000000-0000-0000-0000-000000000000",
              "issued_at": "2026-01-01T00:00:00Z",
              "items": [
                {"id": 1, "code": "ITEM-0001", "value": 1.0, "note": "placeholder-payload-used-because-no-PAYLOAD-file-was-found"}
              ]
            }""";

    private static final Pattern REQUEST_ID_PATTERN = Pattern.compile("\"request_id\"\\s*:\\s*\"([^\"]*)\"");
    private static final Pattern ISSUED_AT_PATTERN = Pattern.compile("\"issued_at\"\\s*:\\s*\"([^\"]*)\"");
    private static final Pattern ITEM_PATTERN = Pattern.compile(
            "\\{\\s*\"id\"\\s*:\\s*(-?\\d+)\\s*,\\s*\"code\"\\s*:\\s*\"([^\"]*)\"\\s*,\\s*\"value\"\\s*:\\s*(-?[0-9.eE+-]+)\\s*,\\s*\"note\"\\s*:\\s*\"([^\"]*)\"\\s*}");

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
                "highload-grpc-java-client: target=%s concurrency=%d requests=%d conns=%d timeout=%dms payload_bytes=%d items=%d%n",
                cfg.target(), cfg.concurrency(), cfg.requests(), cfg.conns(), cfg.timeoutMs(),
                cfg.payloadBytes(), cfg.request().getItemsCount());
        System.out.println("transport: HTTP/2 cleartext (h2c prior-knowledge) via ManagedChannelBuilder.usePlaintext() "
                + "— in contrast to java.net.http.HttpClient over cleartext (see clients/java), this is prior-knowledge from the first byte");

        List<PooledChannel> pool = new ArrayList<>(cfg.conns());
        for (int i = 0; i < cfg.conns(); i++) {
            pool.add(new PooledChannel(newPlaintextChannel(cfg.target())));
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
                            PooledChannel pc = pickLeastInFlight(pool);
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

        for (PooledChannel pc : pool) {
            pc.channel.shutdown();
        }
        for (PooledChannel pc : pool) {
            try {
                pc.channel.awaitTermination(2, TimeUnit.SECONDS);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
        }

        printReport(cfg, results, elapsed);
    }

    /**
     * newPlaintextChannel строит {@link ManagedChannel} через
     * {@code ManagedChannelBuilder.forTarget(target).usePlaintext()} — именно
     * {@code usePlaintext()} и есть h2c prior-knowledge (см. class-level
     * javadoc). Каждый член пула — отдельный {@code ManagedChannel}
     * (собственное TCP/HTTP2-соединение), что демонстрирует несколько
     * независимых соединений (ключевой момент про пул, как и у REST-клиента).
     * {@code ManagedChannelBuilder.forTarget} не соединяется eagerly — первый
     * RPC устанавливает соединение лениво, это нормально, т.к. пул строится
     * один раз при старте, задолго до цикла нагрузки.
     */
    private static ManagedChannel newPlaintextChannel(String target) {
        return ManagedChannelBuilder.forTarget(target)
                .usePlaintext()
                .build();
    }

    /**
     * pickLeastInFlight выбирает канал пула с наименьшим числом активных
     * запросов, чтобы CONCURRENCY воркеров равномерно распределялись по
     * CONNS каналам, а не наваливались на один. При равенстве — наименьший
     * индекс (стабильно под нагрузкой, т.к. inFlight убывает по завершении
     * запросов). Идентично REST-клиенту (clients/java) и Go gRPC-клиенту
     * (clients/grpc-go).
     */
    private static PooledChannel pickLeastInFlight(List<PooledChannel> pool) {
        PooledChannel best = pool.get(0);
        long bestLoad = best.inFlight.get();
        for (int i = 1; i < pool.size(); i++) {
            PooledChannel pc = pool.get(i);
            long load = pc.inFlight.get();
            if (load < bestLoad) {
                best = pc;
                bestLoad = load;
            }
        }
        return best;
    }

    /** doRequest выполняет один unary Check RPC через данный канал пула, с per-call deadline. */
    private static RequestOutcome doRequest(PooledChannel pc, Config cfg) {
        pc.inFlight.incrementAndGet();
        try {
            CheckServiceGrpc.CheckServiceBlockingStub stub = pc.stub
                    .withDeadlineAfter(cfg.timeoutMs(), TimeUnit.MILLISECONDS);

            Instant reqStart = Instant.now();
            CheckResponse response;
            try {
                response = stub.check(cfg.request());
            } catch (StatusRuntimeException e) {
                if (e.getStatus().getCode() == Status.Code.DEADLINE_EXCEEDED) {
                    return RequestOutcome.timeout();
                }
                return RequestOutcome.error();
            }
            Duration latency = Duration.between(reqStart, Instant.now());

            return RequestOutcome.success(latency, response.getBackend());
        } finally {
            pc.inFlight.decrementAndGet();
        }
    }

    /** printReport печатает latency-статистику, исходы запросов и таблицу backend->count. */
    private static void printReport(Config cfg, Results results, Duration elapsed) {
        Results.Snapshot snap = results.snapshot();
        int total = snap.successes() + snap.timeouts() + snap.errors();

        System.out.println();
        System.out.println("=== highload-grpc-java-client report ===");
        System.out.printf("target:       %s%n", cfg.target());
        System.out.printf("requests:     %d (concurrency=%d, conns=%d, timeout=%dms)%n",
                cfg.requests(), cfg.concurrency(), cfg.conns(), cfg.timeoutMs());
        System.out.printf("elapsed:      %s%n", elapsed);
        System.out.println("transport:    HTTP/2 cleartext, h2c prior-knowledge (usePlaintext) — "
                + "not the HTTP/1.1-Upgrade path java.net.http.HttpClient falls back to");
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

    /** PooledChannel — один член пула: ManagedChannel + готовый blocking-стаб + счётчик in-flight. */
    private static final class PooledChannel {
        private final ManagedChannel channel;
        private final CheckServiceGrpc.CheckServiceBlockingStub stub;
        private final AtomicLong inFlight = new AtomicLong();

        private PooledChannel(ManagedChannel channel) {
            this.channel = channel;
            this.stub = CheckServiceGrpc.newBlockingStub(channel);
        }
    }

    /** RequestOutcome — результат одного RPC: latency+backend (success) либо категория неудачи. */
    private record RequestOutcome(Kind kind, Duration latency, String backend) {

        enum Kind { SUCCESS, TIMEOUT, ERROR }

        static RequestOutcome success(Duration latency, String backend) {
            return new RequestOutcome(Kind.SUCCESS, latency, backend);
        }

        static RequestOutcome timeout() {
            return new RequestOutcome(Kind.TIMEOUT, null, null);
        }

        static RequestOutcome error() {
            return new RequestOutcome(Kind.ERROR, null, null);
        }
    }

    /** Results накапливает исходы всех воркеров под синхронизацией. */
    private static final class Results {
        private final List<Duration> latencies = new ArrayList<>();
        private final Map<String, Integer> backendHits = new TreeMap<>();
        private int successes;
        private int timeouts;
        private int errors;

        synchronized void record(RequestOutcome o) {
            switch (o.kind()) {
                case SUCCESS -> {
                    successes++;
                    latencies.add(o.latency());
                    backendHits.merge(o.backend(), 1, Integer::sum);
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
                    errors);
        }

        record Snapshot(List<Duration> latencies, Map<String, Integer> backendHits,
                         int successes, int timeouts, int errors) {
        }
    }

    /** Config — конфигурация клиента, полностью из env-переменных. */
    private record Config(String target, int concurrency, int requests, int conns, int timeoutMs,
                           CheckRequest request, int payloadBytes) {

        static Config fromEnv() {
            String target = getEnvDefault("TARGET", DEFAULT_TARGET);
            int concurrency = getEnvIntDefault("CONCURRENCY", DEFAULT_CONCURRENCY);
            int requests = getEnvIntDefault("REQUESTS", DEFAULT_REQUESTS);
            int conns = getEnvIntDefault("CONNS", DEFAULT_CONNS);
            int timeoutMs = getEnvIntDefault("TIMEOUT_MS", DEFAULT_TIMEOUT_MS);

            byte[] payload = loadPayload(getEnvDefault("PAYLOAD", DEFAULT_PAYLOAD_PATH));
            CheckRequest request = buildRequest(new String(payload, StandardCharsets.UTF_8));

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

            return new Config(target, concurrency, requests, conns, timeoutMs, request, payload.length);
        }

        /**
         * buildRequest перепаковывает тот же JSON payload, что читают
         * REST-клиенты стенда, в {@link CheckRequest} (proto-контракт
         * {@code grpc/proto/check.proto}), построенный один раз при старте и
         * переиспользуемый (read-only) для всех вызовов — без повторного
         * парсинга/аллокации на горячем пути. Парсер — тот же
         * regex-подход, что у REST-клиента (плоская известная схема из 3
         * top-level и 4 item-полей, полноценный JSON-парсер избыточен —
         * см. javadoc {@code extractBackend} у REST-клиента).
         */
        private static CheckRequest buildRequest(String json) {
            CheckRequest.Builder builder = CheckRequest.newBuilder();

            Matcher ridM = REQUEST_ID_PATTERN.matcher(json);
            if (ridM.find()) {
                builder.setRequestId(ridM.group(1));
            }
            Matcher issuedM = ISSUED_AT_PATTERN.matcher(json);
            if (issuedM.find()) {
                builder.setIssuedAt(issuedM.group(1));
            }

            Matcher itemM = ITEM_PATTERN.matcher(json);
            while (itemM.find()) {
                builder.addItems(Item.newBuilder()
                        .setId(Long.parseLong(itemM.group(1)))
                        .setCode(itemM.group(2))
                        .setValue(Double.parseDouble(itemM.group(3)))
                        .setNote(itemM.group(4))
                        .build());
            }

            return builder.build();
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
