package tech.khorost.realtime;

import java.lang.management.ManagementFactory;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.Arrays;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicLong;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.context.annotation.Bean;
import org.springframework.http.HttpCookie;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.http.codec.ServerSentEvent;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.reactive.HandlerMapping;
import org.springframework.web.reactive.handler.SimpleUrlHandlerMapping;
import org.springframework.web.reactive.socket.CloseStatus;
import org.springframework.web.reactive.socket.WebSocketHandler;
import org.springframework.web.reactive.socket.WebSocketMessage;
import org.springframework.web.reactive.socket.server.support.HandshakeWebSocketService;
import org.springframework.web.reactive.socket.server.support.WebSocketHandlerAdapter;
import org.springframework.web.reactive.socket.server.upgrade.ReactorNettyRequestUpgradeStrategy;
import org.springframework.web.server.ServerWebExchange;
import org.springframework.web.server.WebFilter;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.core.publisher.Sinks;
import reactor.netty.http.server.WebsocketServerSpec;

/**
 * Сервер стенда realtime на Java — тот же протокол, что server-go/main.go: маршруты,
 * формат события, переменные окружения RT_*, заголовок X-Instance.
 */
@SpringBootApplication
@RestController
public class App {
    static final String INSTANCE = env("RT_INSTANCE", "java-1");
    static final String BUS_KIND = env("RT_BUS", "none");
    static final String QUEUE = env("RT_QUEUE", Hub.UNBOUNDED);
    static final int QUEUE_LEN = Integer.parseInt(env("RT_QUEUE_LEN", "256"));
    static final long HEARTBEAT_MS = Long.parseLong(env("RT_HEARTBEAT_MS", "0"));
    static final long LONGPOLL_MS = Long.parseLong(env("RT_LONGPOLL_MS", "2000"));
    static final long SSE_RETRY_MS = Long.parseLong(env("RT_SSE_RETRY_MS", "1000"));
    static final boolean SSE_NO_BUFFER = env("RT_SSE_NO_BUFFER_HEADER", "0").equals("1");
    static final boolean WS_COMPRESS = env("RT_WS_COMPRESS", "0").equals("1");

    static final Hub hub = new Hub();
    static Bus bus;

    static String env(String name, String def) {
        String v = System.getenv(name);
        return v == null || v.isEmpty() ? def : v;
    }

    public static void main(String[] args) throws Exception {
        List<String> topics = Arrays.asList(env("RT_TOPICS", "t").split(","));
        bus = Bus.create(BUS_KIND, env("RT_REDIS", "redis:6379"), env("RT_NATS", "nats://nats:4222"), topics);
        bus.run(hub);
        SpringApplication app = new SpringApplication(App.class);
        Map<String, Object> props = new java.util.HashMap<>(Map.of("server.port", env("RT_PORT", "8080"),
                "spring.main.banner-mode", "off"));
        if (env("RT_SSE_GZIP", "0").equals("1")) { // gzip ответов, включая поток SSE
            props.put("server.compression.enabled", "true");
            props.put("server.compression.mime-types", "text/event-stream,application/json");
            props.put("server.compression.min-response-size", "0");
        }
        app.setDefaultProperties(props);
        app.run(args);
        System.out.printf("%s: шина %s, очередь %s/%d, heartbeat %d мс%n", INSTANCE, BUS_KIND, QUEUE, QUEUE_LEN,
                HEARTBEAT_MS);
    }

    /** X-Instance в каждом ответе, включая рукопожатие WebSocket. */
    @Bean
    WebFilter instanceHeader() {
        return (exchange, chain) -> {
            exchange.getResponse().getHeaders().set("X-Instance", INSTANCE);
            return chain.filter(exchange);
        };
    }

    static String topicOf(String t) {
        return t == null || t.isEmpty() ? "t" : t;
    }

    // --- SSE ------------------------------------------------------------------------

    static final long HISTORY_DELAY_MS = Long.parseLong(env("RT_HISTORY_DELAY_MS", "0"));
    static final boolean HISTORY_FAIL = env("RT_HISTORY_FAIL", "0").equals("1");
    static final boolean HISTORY_BARRIER = env("RT_HISTORY_BARRIER", "0").equals("1");
    static final boolean SUBSCRIBE_LATE = env("RT_SUBSCRIBE_LATE", "0").equals("1");

    /** Управляемый стык истории и живого потока — то же, что barrier в Go. */
    static final java.util.concurrent.atomic.AtomicReference<java.util.concurrent.CompletableFuture<Void>> gate =
            new java.util.concurrent.atomic.AtomicReference<>(new java.util.concurrent.CompletableFuture<>());
    static final java.util.concurrent.atomic.AtomicBoolean waiting = new java.util.concurrent.atomic.AtomicBoolean();

    @GetMapping("/debug/barrier")
    Map<String, Object> barrierState() {
        return Map.of("waiting", waiting.get());
    }

    @PostMapping("/debug/release")
    ResponseEntity<Void> barrierRelease() {
        waiting.set(false);
        gate.getAndSet(new java.util.concurrent.CompletableFuture<>()).complete(null);
        return ResponseEntity.noContent().build();
    }

    /**
     * История — до начала ответа (как в Go). Ошибка её чтения — отказ 503, а не
     * поток: живое событие после непрочитанной истории сдвинуло бы курсор клиента
     * за пропуск. «Истории нет» (шина без журнала) — не ошибка: поток идёт.
     */
    @GetMapping(path = "/sse", produces = MediaType.TEXT_EVENT_STREAM_VALUE)
    Mono<ResponseEntity<Flux<ServerSentEvent<String>>>> sse(@RequestParam(required = false) String topic,
            @RequestHeader(name = "Last-Event-ID", required = false) String lastEventId) {
        String t = topicOf(topic);
        // Подписка раньше истории; RT_SUBSCRIBE_LATE — инъекция неверного порядка.
        java.util.concurrent.atomic.AtomicReference<Hub.Stream> reg = new java.util.concurrent.atomic.AtomicReference<>(
                SUBSCRIBE_LATE ? null : hub.open(t, QUEUE, QUEUE_LEN));
        Runnable release = () -> {
            Hub.Stream r = reg.get();
            if (r != null) r.release().run();
        };
        long after = lastEventId == null ? 0 : Long.parseLong(lastEventId.trim());
        Mono<List<Hub.Event>> past = lastEventId == null ? Mono.just(List.of())
                : Mono.delay(Duration.ofMillis(HISTORY_DELAY_MS))
                        .then(Mono.fromCallable(() -> {
                            if (HISTORY_FAIL) throw new IllegalStateException("инъекция RT_HISTORY_FAIL");
                            List<Hub.Event> h = bus.history(t, after);
                            return h == null ? List.<Hub.Event>of() : h;
                        }).subscribeOn(reactor.core.scheduler.Schedulers.boundedElastic()))
                        .flatMap(h -> !HISTORY_BARRIER ? Mono.just(h) : Mono.defer(() -> {
                            waiting.set(true); // история прочитана — ждать разрешения сценария
                            return Mono.fromFuture(gate.get()).thenReturn(h);
                        }));
        return past.map(h -> {
            if (SUBSCRIBE_LATE) reg.set(hub.open(t, QUEUE, QUEUE_LEN));
            var resp = ResponseEntity.ok().cacheControl(org.springframework.http.CacheControl.noCache());
            if (SSE_NO_BUFFER) resp = resp.header("X-Accel-Buffering", "no");
            return resp.body(stream(reg.get().flux(), h, after).doFinally(s -> release.run()));
        }).doOnCancel(release) // клиент ушёл, пока читалась история: подписка ещё не тронута
                .onErrorResume(ex -> {
                    release.run();
                    return Mono.just(ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE)
                            .body(Flux.<ServerSentEvent<String>>empty()));
                });
    }

    private Flux<ServerSentEvent<String>> stream(Flux<Hub.Event> live, List<Hub.Event> past, long after) {
        AtomicLong sent = new AtomicLong(after);
        Flux<ServerSentEvent<String>> events = Flux.concat(Flux.fromIterable(past), live)
                .filter(e -> e.id() > sent.get())
                .doOnNext(e -> sent.set(e.id()))
                .map(e -> ServerSentEvent.<String>builder().id(Long.toString(e.id())).data(Bus.json(e)).build());
        Flux<ServerSentEvent<String>> head = Flux.just(
                ServerSentEvent.<String>builder().retry(Duration.ofMillis(SSE_RETRY_MS)).build());
        if (HEARTBEAT_MS <= 0) return head.concatWith(events);
        Sinks.Empty<Void> done = Sinks.empty();
        Flux<ServerSentEvent<String>> hb = Flux.interval(Duration.ofMillis(HEARTBEAT_MS))
                .map(i -> ServerSentEvent.<String>builder().comment("heartbeat").build())
                .takeUntilOther(done.asMono());
        return head.concatWith(events.doFinally(s -> done.tryEmitEmpty()).mergeWith(hb));
    }

    // --- WebSocket -------------------------------------------------------------------

    @Bean
    HandlerMapping wsMapping() {
        WebSocketHandler handler = session -> {
            String query = session.getHandshakeInfo().getUri().getQuery();
            String t = "t";
            if (query != null)
                for (String kv : query.split("&"))
                    if (kv.startsWith("topic=")) t = kv.substring(6);
            Flux<WebSocketMessage> events = hub.stream(t, QUEUE, QUEUE_LEN)
                    .map(e -> session.textMessage(Bus.json(e)));
            Flux<WebSocketMessage> out = events;
            if (HEARTBEAT_MS > 0) {
                Sinks.Empty<Void> done = Sinks.empty();
                out = events.doFinally(s -> done.tryEmitEmpty()).mergeWith(
                        Flux.interval(Duration.ofMillis(HEARTBEAT_MS))
                                .map(i -> session.pingMessage(f -> f.wrap(new byte[0])))
                                .takeUntilOther(done.asMono()));
            }
            Mono<Void> output = session.send(out)
                    .then(session.close(new CloseStatus(1008, "очередь переполнена или соединение закрыто")));
            Mono<Void> input = session.receive().then(); // управляющие кадры и закрытие клиентом
            return Mono.firstWithSignal(input, output);
        };
        return new SimpleUrlHandlerMapping(Map.of("/ws", handler), -1);
    }

    @Bean
    WebSocketHandlerAdapter wsAdapter() {
        var spec = WebsocketServerSpec.builder().compress(WS_COMPRESS).maxFramePayloadLength(1 << 16);
        return new WebSocketHandlerAdapter(new HandshakeWebSocketService(new ReactorNettyRequestUpgradeStrategy(spec)));
    }

    // --- polling -------------------------------------------------------------------------

    @GetMapping("/poll")
    List<Hub.Event> poll(@RequestParam(required = false) String topic,
            @RequestParam(defaultValue = "0") long after) {
        return hub.after(topicOf(topic), after);
    }

    /** Курсор long polling — в памяти инстанса, ключ — cookie sid (как в Go). */
    static final Map<String, Long> cursors = new ConcurrentHashMap<>();

    @GetMapping("/longpoll")
    Mono<List<Hub.Event>> longpoll(@RequestParam(required = false) String topic, ServerWebExchange ex) {
        String t = topicOf(topic);
        HttpCookie c = ex.getRequest().getCookies().getFirst("sid");
        String key = (c == null ? "" : c.getValue()) + "|" + t;
        long cur = cursors.computeIfAbsent(key, k -> hub.latest(t)); // новый — «с текущего момента»
        long deadline = System.currentTimeMillis() + LONGPOLL_MS;
        return waitEvents(t, cur, deadline).doOnNext(evs -> {
            if (!evs.isEmpty()) cursors.put(key, evs.get(evs.size() - 1).id());
        });
    }

    private Mono<List<Hub.Event>> waitEvents(String t, long cur, long deadline) {
        Mono<Void> signal = hub.changed(t); // сначала ожидание, потом проверка — стык без пропуска
        List<Hub.Event> evs = hub.after(t, cur);
        if (!evs.isEmpty()) return Mono.just(evs);
        long left = deadline - System.currentTimeMillis();
        if (left <= 0) return Mono.just(List.of());
        return signal.timeout(Duration.ofMillis(left), Mono.empty())
                .then(Mono.defer(() -> waitEvents(t, cur, deadline)));
    }

    // --- публикация и служебное ----------------------------------------------------------

    @PostMapping("/publish")
    Mono<ResponseEntity<String>> publish(@RequestParam(required = false) String topic, @RequestParam long id,
            @RequestBody(required = false) Mono<String> body) {
        return body.defaultIfEmpty("").publishOn(reactor.core.scheduler.Schedulers.boundedElastic()).map(p -> {
            try {
                bus.publish(new Hub.Event(id, topicOf(topic), p));
                return ResponseEntity.<String>noContent().build();
            } catch (Exception ex) {
                return ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE).body("шина: " + ex.getMessage());
            }
        });
    }

    @GetMapping("/healthz")
    Map<String, Object> health() {
        return Map.of("instance", INSTANCE, "server", "java", "bus", BUS_KIND,
                "bus_state", bus.up() ? "up" : "down", "conns", Hub.conns.get());
    }

    @GetMapping("/debug/stats")
    Map<String, Object> stats() {
        long[] live = hub.liveMaxQueueAndDropped();
        return Map.of("instance", INSTANCE, "conns", Hub.conns.get(), "delivered", Hub.delivered.get(),
                "dropped", Hub.dropped.get() + live[1], "disconnected", Hub.disconnected.get(),
                "max_queue", Math.max(Hub.maxQueue.get(), live[0]));
    }

    @PostMapping("/debug/reset")
    ResponseEntity<Void> reset() {
        Hub.delivered.set(0);
        Hub.dropped.set(0);
        Hub.disconnected.set(0);
        Hub.maxQueue.set(0);
        return ResponseEntity.noContent().build();
    }

    @PostMapping("/debug/gc")
    Map<String, Object> gc() throws Exception {
        System.gc();
        Thread.sleep(200);
        long heap = ManagementFactory.getMemoryMXBean().getHeapMemoryUsage().getUsed() / 1024;
        return Map.of("instance", INSTANCE, "rss_kb", rssKb(), "heap_kb", heap,
                "threads", Thread.activeCount(), "conns", Hub.conns.get());
    }

    /** VmRSS процесса из /proc — так же, как в Go. */
    static long rssKb() {
        try {
            for (String line : Files.readAllLines(Path.of("/proc/self/status")))
                if (line.startsWith("VmRSS:")) return Long.parseLong(line.replaceAll("[^0-9]", ""));
        } catch (Exception ignored) {
            // не Linux
        }
        return -1;
    }
}
