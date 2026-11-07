package shortlinks.consumer;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpServer;
import io.nats.client.ConsumeOptions;
import io.nats.client.Connection;
import io.nats.client.ConsumerContext;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import io.nats.client.Message;
import io.nats.client.MessageConsumer;
import io.nats.client.Nats;
import io.nats.client.Options;
import io.nats.client.api.AckPolicy;
import io.nats.client.api.ConsumerConfiguration;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.impl.Headers;
import io.opentelemetry.api.GlobalOpenTelemetry;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.context.Context;
import io.opentelemetry.context.Scope;
import io.opentelemetry.context.propagation.TextMapGetter;

import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.SQLException;
import java.sql.Timestamp;
import java.time.Duration;
import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Consumer аналитики кликов: JetStream durable "analytics", агрегат по минутам в PostgreSQL.
 *
 * <p>Доставка at-least-once: неподтверждённое за ACK_WAIT сообщение придёт снова. Учёт события и
 * агрегат пишутся в одной транзакции, повтор того же id не учитывается (IDEMPOTENT), ack — после
 * коммита. Трейс-контекст продюсера на Go приходит в заголовках сообщения (W3C traceparent).
 */
public final class Main {
    static final String STREAM = "SHORTLINKS";
    static final ObjectMapper JSON = new ObjectMapper();
    static final AtomicLong processed = new AtomicLong(), duplicates = new AtomicLong(), linkEvents = new AtomicLong(),
            commits = new AtomicLong(), reconnects = new AtomicLong();

    record Config(String jdbcUrl, Properties db, String natsUrl, boolean idempotent, long crashAfterCommit, Duration ackWait,
                  Duration consumeExpires, boolean traceManual, int metricsPort) {}

    public static void main(String[] args) throws Exception {
        Config cfg;
        try {
            cfg = load(System.getenv());
        } catch (IllegalArgumentException e) {
            log("error", "старт отменён", Map.of("err", e.getMessage()));
            System.exit(2);
            return;
        }
        // соединение — в ячейке: после перезапуска PostgreSQL старое закрыто, и его надо заменить, а не
        // обрабатывать следующую доставку тем же мёртвым соединением
        java.sql.Connection[] db = {connect(cfg)};
        Connection nc = Nats.connect(new Options.Builder().server(cfg.natsUrl()).maxReconnects(-1)
                .reconnectWait(Duration.ofMillis(500)).build());
        JetStreamManagement jsm = nc.jetStreamManagement();
        ensureStream(jsm);
        jsm.addOrUpdateConsumer(STREAM, ConsumerConfiguration.builder().durable("analytics").ackPolicy(AckPolicy.Explicit)
                .ackWait(cfg.ackWait()).maxAckPending(1000).build());
        metrics(cfg.metricsPort());

        ConsumerContext ctx = nc.getConsumerContext(STREAM, "analytics");
        // expiresIn — срок одного pull-запроса; по пропуску heartbeat внутри него consumer узнаёт, что подписка
        // потеряна (например, брокер перезапущен), и переподписывается. Чем срок длиннее, тем дольше молчание.
        ConsumeOptions co = ConsumeOptions.builder().expiresIn(cfg.consumeExpires().toMillis()).build();
        MessageConsumer mc = ctx.consume(co, m -> {
            try {
                handle(db[0], cfg, m);
                m.ack();
            } catch (Exception e) {
                try {
                    db[0].rollback();
                } catch (SQLException ignored) {
                    // соединение потеряно — заменим ниже
                }
                db[0] = revive(db[0], cfg);
                log("warn", "сообщение не обработано, придёт снова", Map.of("subject", m.getSubject(), "err", String.valueOf(e)));
                // с задержкой: пока база недоступна, немедленный повтор крутил бы цикл ошибок
                m.nakWithDelay(Duration.ofSeconds(1));
            }
        });
        log("info", "consumer запущен", Map.of("idempotent", cfg.idempotent(), "lang", "java"));
        CountDownLatch done = new CountDownLatch(1);
        Runtime.getRuntime().addShutdownHook(new Thread(() -> {
            try {
                mc.stop();
                nc.close();
                db[0].close();
            } catch (Exception ignored) {
                // выходим в любом случае
            }
            done.countDown();
        }));
        done.await();
    }

    static java.sql.Connection connect(Config cfg) throws SQLException {
        java.sql.Connection c = DriverManager.getConnection(cfg.jdbcUrl(), cfg.db());
        c.setAutoCommit(false);
        return c;
    }

    /** Исправное соединение: прежнее, если живо, иначе новое; база недоступна — прежнее, попытка при следующей ошибке. */
    static java.sql.Connection revive(java.sql.Connection old, Config cfg) {
        try {
            if (old.isValid(1)) {
                return old;
            }
        } catch (SQLException ignored) {
            // считаем мёртвым
        }
        try {
            java.sql.Connection c = connect(cfg);
            try {
                old.close();
            } catch (SQLException ignored) {
                // уже закрыто
            }
            reconnects.incrementAndGet();
            log("info", "соединение с базой пересоздано", Map.of());
            return c;
        } catch (SQLException e) {
            return old;
        }
    }

    static void ensureStream(JetStreamManagement jsm) throws Exception {
        try {
            jsm.getStreamInfo(STREAM);
        } catch (JetStreamApiException e) {
            jsm.addStream(StreamConfiguration.builder().name(STREAM).subjects("clicks.>", "links.>")
                    .duplicateWindow(Duration.ofMinutes(2)).build());
        }
    }

    static final TextMapGetter<Headers> GETTER = new TextMapGetter<>() {
        @Override
        public Iterable<String> keys(Headers h) {
            return h == null ? List.of() : h.keySet();
        }

        @Override
        public String get(Headers h, String key) {
            return h == null ? null : h.getFirst(key);
        }
    };

    static void handle(java.sql.Connection db, Config cfg, Message m) throws Exception {
        // Ручной спан обработки — родитель JDBC-спанов агента. TRACE_MANUAL=false — без него: тогда трейс
        // держит только jnats-инструментация агента (опыт статьи 4).
        Span span = null;
        Scope scope = null;
        if (cfg.traceManual()) {
            Context parent = GlobalOpenTelemetry.getPropagators().getTextMapPropagator()
                    .extract(Context.current(), m.getHeaders(), GETTER);
            span = GlobalOpenTelemetry.getTracer("shortlinks-consumer").spanBuilder("consume " + m.getSubject())
                    .setParent(parent).setSpanKind(SpanKind.CONSUMER).startSpan();
            scope = span.makeCurrent();
        }
        try {
            JsonNode ev = JSON.readTree(m.getData());
            if (m.getSubject().equals("links.created")) {
                try (PreparedStatement st = db.prepareStatement("INSERT INTO seen_links (code) VALUES (?) ON CONFLICT DO NOTHING")) {
                    st.setString(1, ev.path("code").asText());
                    st.executeUpdate();
                }
                db.commit();
                linkEvents.incrementAndGet();
                return;
            }
            String id = ev.path("id").asText();
            boolean count = true;
            if (cfg.idempotent()) {
                try (PreparedStatement st = db.prepareStatement("INSERT INTO processed_events (event_id) VALUES (?) ON CONFLICT DO NOTHING")) {
                    st.setString(1, id);
                    count = st.executeUpdate() == 1;
                }
            }
            if (count) {
                try (PreparedStatement st = db.prepareStatement(
                        "INSERT INTO clicks_by_minute (code, minute, clicks) VALUES (?, date_trunc('minute', ?::timestamptz), 1) "
                                + "ON CONFLICT (code, minute) DO UPDATE SET clicks = clicks_by_minute.clicks + 1")) {
                    st.setString(1, ev.path("code").asText());
                    st.setTimestamp(2, Timestamp.from(OffsetDateTime.parse(ev.path("at").asText()).toInstant()));
                    st.executeUpdate();
                }
            } else {
                duplicates.incrementAndGet();
            }
            db.commit();
            if (count) {
                processed.incrementAndGet();
            }
            if (cfg.crashAfterCommit() > 0 && commits.incrementAndGet() == cfg.crashAfterCommit()) {
                // опыт: запись в базе есть, ack брокеру нет — после перезапуска сообщение придёт снова
                log("warn", "падение после коммита до ack", Map.of("event", id));
                Runtime.getRuntime().halt(3);
            }
        } finally {
            if (scope != null) {
                scope.close();
                span.end();
            }
        }
    }

    /** Конфигурация целиком: все ошибки сразу, как у сервиса на Go. */
    static Config load(Map<String, String> env) {
        List<String> errs = new ArrayList<>();
        String dsn = env.getOrDefault("DATABASE_URL", "");
        String jdbc = null;
        Properties props = new Properties();
        if (dsn.isEmpty()) {
            errs.add("DATABASE_URL не задана");
        } else {
            URI u = URI.create(dsn);
            if (!"postgres".equals(u.getScheme()) && !"postgresql".equals(u.getScheme())) {
                errs.add("DATABASE_URL: ожидается postgres://…");
            } else {
                jdbc = "jdbc:postgresql://" + u.getHost() + ":" + (u.getPort() > 0 ? u.getPort() : 5432) + u.getPath();
                String[] ui = String.valueOf(u.getUserInfo()).split(":", 2);
                props.setProperty("user", ui[0]);
                if (ui.length > 1) {
                    props.setProperty("password", ui[1]);
                }
            }
        }
        String nats = env.getOrDefault("NATS_URL", "");
        if (nats.isEmpty()) {
            errs.add("NATS_URL не задана");
        }
        boolean idem = bool(env, "IDEMPOTENT", true, errs);
        long crash = 0;
        try {
            crash = Long.parseLong(env.getOrDefault("CRASH_AFTER_COMMIT", "0"));
        } catch (NumberFormatException e) {
            errs.add("CRASH_AFTER_COMMIT: целое");
        }
        Duration ack = Duration.ofSeconds(30);
        String aw = env.getOrDefault("ACK_WAIT", "30s");
        if (aw.matches("\\d+s")) {
            ack = Duration.ofSeconds(Long.parseLong(aw.substring(0, aw.length() - 1)));
        } else {
            errs.add("ACK_WAIT=\"" + aw + "\": длительность вида 5s");
        }
        Duration expires = Duration.ofSeconds(30);
        String ce = env.getOrDefault("CONSUME_EXPIRES", "30s");
        if (ce.matches("\\d+s")) {
            expires = Duration.ofSeconds(Long.parseLong(ce.substring(0, ce.length() - 1)));
        } else {
            errs.add("CONSUME_EXPIRES=\"" + ce + "\": длительность вида 5s");
        }
        boolean manual = bool(env, "TRACE_MANUAL", true, errs);
        int port = Integer.parseInt(env.getOrDefault("METRICS_ADDR", ":9090").replace(":", ""));
        if (!errs.isEmpty()) {
            throw new IllegalArgumentException("конфигурация: " + String.join("; ", errs));
        }
        return new Config(jdbc, props, nats, idem, crash, ack, expires, manual, port);
    }

    static boolean bool(Map<String, String> env, String key, boolean def, List<String> errs) {
        String v = env.get(key);
        if (v == null) {
            return def;
        }
        if (!v.equals("true") && !v.equals("false")) {
            errs.add(key + "=\"" + v + "\": true или false");
            return def;
        }
        return Boolean.parseBoolean(v);
    }

    static void metrics(int port) throws Exception {
        HttpServer srv = HttpServer.create(new InetSocketAddress(port), 0);
        srv.createContext("/metrics", ex -> {
            String body = "# TYPE clicks_processed_total counter\nclicks_processed_total " + processed.get() + "\n"
                    + "# TYPE clicks_duplicates_total counter\nclicks_duplicates_total " + duplicates.get() + "\n"
                    + "# TYPE link_events_seen_total counter\nlink_events_seen_total " + linkEvents.get() + "\n"
                    + "# TYPE db_reconnects_total counter\ndb_reconnects_total " + reconnects.get() + "\n";
            byte[] b = body.getBytes(StandardCharsets.UTF_8);
            ex.getResponseHeaders().add("Content-Type", "text/plain; version=0.0.4");
            ex.sendResponseHeaders(200, b.length);
            try (OutputStream os = ex.getResponseBody()) {
                os.write(b);
            }
        });
        srv.start();
    }

    static void log(String level, String msg, Map<String, ?> attrs) {
        try {
            Map<String, Object> rec = new java.util.LinkedHashMap<>();
            rec.put("time", OffsetDateTime.now().toString());
            rec.put("level", level.toUpperCase());
            rec.put("msg", msg);
            rec.putAll(attrs);
            System.out.println(JSON.writeValueAsString(rec));
        } catch (Exception e) {
            System.out.println(msg);
        }
    }
}
