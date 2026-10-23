package tech.khorost.realtime;

import io.lettuce.core.RedisClient;
import io.lettuce.core.RedisURI;
import io.lettuce.core.StreamMessage;
import io.lettuce.core.XAddArgs;
import io.lettuce.core.XReadArgs;
import io.lettuce.core.api.StatefulRedisConnection;
import io.lettuce.core.pubsub.RedisPubSubAdapter;
import io.lettuce.core.pubsub.StatefulRedisPubSubConnection;
import io.nats.client.Connection;
import io.nats.client.JetStream;
import io.nats.client.JetStreamManagement;
import io.nats.client.Nats;
import io.nats.client.Options;
import io.nats.client.PushSubscribeOptions;
import io.nats.client.api.ConsumerConfiguration;
import io.nats.client.api.DeliverPolicy;
import io.nats.client.api.StorageType;
import io.nats.client.api.StreamConfiguration;
import java.time.Duration;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import tools.jackson.databind.json.JsonMapper;

/** Шина между инстансами — то же, что bus.go. */
interface Bus {
    JsonMapper JSON = JsonMapper.builder().build();

    void publish(Hub.Event e) throws Exception;

    void run(Hub hub);

    boolean up();

    /** null — у шины нет истории. */
    List<Hub.Event> history(String topic, long after) throws Exception;

    static Bus create(String kind, String redis, String nats, List<String> topics) throws Exception {
        return switch (kind) {
            case "none" -> new Local();
            case "redis" -> new RedisPubSub(redis);
            case "redis-streams" -> new RedisStreams(redis, topics);
            case "nats" -> new NatsBus(nats, false);
            case "jetstream" -> new NatsBus(nats, true);
            default -> throw new IllegalArgumentException("RT_BUS=" + kind + ": неизвестная шина");
        };
    }

    static Hub.Event parse(String json) {
        return JSON.readValue(json, Hub.Event.class);
    }

    static String json(Hub.Event e) {
        return JSON.writeValueAsString(e);
    }

    // --- без шины -------------------------------------------------------------

    final class Local implements Bus {
        private Hub hub;

        public void publish(Hub.Event e) { hub.deliver(e); }

        public void run(Hub hub) { this.hub = hub; }

        public boolean up() { return true; }

        public List<Hub.Event> history(String topic, long after) { return null; }
    }

    // --- Redis ------------------------------------------------------------------

    static RedisClient redisClient(String addr) {
        String[] hp = addr.split(":");
        RedisURI uri = RedisURI.Builder.redis(hp[0], Integer.parseInt(hp[1]))
                .withTimeout(Duration.ofMillis(500)).build();
        return RedisClient.create(uri);
    }

    /** Pub/sub: «не более одного раза». Живость подписки — PING по самому соединению подписки. */
    final class RedisPubSub implements Bus {
        private final RedisClient client;
        private final StatefulRedisConnection<String, String> pub;
        private final AtomicBoolean up = new AtomicBoolean();
        private StatefulRedisPubSubConnection<String, String> sub;

        RedisPubSub(String addr) {
            client = redisClient(addr);
            pub = connectRetry(() -> client.connect());
        }

        public void publish(Hub.Event e) {
            pub.sync().publish("rt." + e.topic(), json(e));
        }

        public void run(Hub hub) {
            sub = connectRetry(() -> client.connectPubSub());
            sub.addListener(new RedisPubSubAdapter<>() {
                @Override
                public void message(String pattern, String channel, String message) {
                    hub.deliver(parse(message));
                }
            });
            sub.sync().psubscribe("rt.*");
            Executors.newSingleThreadScheduledExecutor().scheduleWithFixedDelay(() -> {
                try {
                    sub.async().ping().get(300, TimeUnit.MILLISECONDS);
                    up.set(true);
                } catch (Exception ex) {
                    up.set(false);
                }
            }, 0, 200, TimeUnit.MILLISECONDS);
        }

        public boolean up() { return up.get(); }

        public List<Hub.Event> history(String topic, long after) { return null; }
    }

    /** Streams: журнал, подписчик дочитывает с последнего id. Id записи — «0-<id события>». */
    final class RedisStreams implements Bus {
        private final RedisClient client;
        private final StatefulRedisConnection<String, String> pub, hist;
        private final List<String> topics;
        private final AtomicBoolean up = new AtomicBoolean();

        RedisStreams(String addr, List<String> topics) {
            client = redisClient(addr);
            pub = connectRetry(() -> client.connect());
            hist = connectRetry(() -> client.connect());
            this.topics = topics;
        }

        public void publish(Hub.Event e) {
            pub.sync().xadd("rt." + e.topic(), new XAddArgs().id("0-" + e.id()), Map.of("payload", e.payload()));
        }

        public void run(Hub hub) {
            Thread t = new Thread(() -> {
                Map<String, String> last = new HashMap<>();
                StatefulRedisConnection<String, String> conn = null;
                while (true) {
                    try {
                        if (conn == null || !conn.isOpen()) conn = client.connect();
                        for (String topic : topics)
                            last.computeIfAbsent(topic, k -> lastId(k));
                        List<XReadArgs.StreamOffset<String>> offs = new ArrayList<>();
                        for (String topic : topics)
                            offs.add(XReadArgs.StreamOffset.from("rt." + topic, last.get(topic)));
                        @SuppressWarnings("unchecked")
                        List<StreamMessage<String, String>> msgs = conn.sync().xread(
                                XReadArgs.Builder.block(250), offs.toArray(new XReadArgs.StreamOffset[0]));
                        if (msgs.isEmpty()) conn.sync().ping();
                        up.set(true);
                        for (StreamMessage<String, String> m : msgs) {
                            String topic = m.getStream().substring(3);
                            last.put(topic, m.getId());
                            hub.deliver(new Hub.Event(seq(m.getId()), topic, m.getBody().get("payload")));
                        }
                    } catch (Exception ex) {
                        up.set(false);
                        if (conn != null) conn.closeAsync();
                        conn = null;
                        sleep(100);
                    }
                }
            }, "redis-streams");
            t.setDaemon(true);
            t.start();
        }

        private String lastId(String topic) {
            try {
                List<StreamMessage<String, String>> r = hist.sync().xrevrange("rt." + topic,
                        io.lettuce.core.Range.create("-", "+"), io.lettuce.core.Limit.from(1));
                return r.isEmpty() ? "0-0" : r.get(0).getId();
            } catch (Exception ex) {
                return "0-0";
            }
        }

        static long seq(String id) {
            return Long.parseLong(id.substring(id.indexOf('-') + 1));
        }

        public boolean up() { return up.get(); }

        public List<Hub.Event> history(String topic, long after) {
            List<Hub.Event> out = new ArrayList<>();
            for (StreamMessage<String, String> m : hist.sync().xrange("rt." + topic,
                    io.lettuce.core.Range.create("0-" + (after + 1), "+")))
                out.add(new Hub.Event(seq(m.getId()), topic, m.getBody().get("payload")));
            return out;
        }
    }

    // --- NATS ---------------------------------------------------------------------

    /** Core — «не более одного раза»; JetStream — упорядоченный потребитель продолжает с последнего номера. */
    final class NatsBus implements Bus {
        private final Connection nc;
        private final boolean js;

        NatsBus(String url, boolean js) throws Exception {
            Options opts = new Options.Builder().server(url).maxReconnects(-1)
                    .reconnectWait(Duration.ofMillis(100)).pingInterval(Duration.ofMillis(300))
                    .maxPingsOut(1).connectionTimeout(Duration.ofMillis(300)).build();
            Connection c = null;
            for (int i = 0; i < 50 && c == null; i++) {
                try {
                    c = Nats.connect(opts);
                } catch (Exception ex) {
                    sleep(200);
                }
            }
            if (c == null) throw new IllegalStateException("NATS недоступен: " + url);
            nc = c;
            this.js = js;
            if (js) {
                JetStreamManagement jsm = nc.jetStreamManagement();
                StreamConfiguration sc = StreamConfiguration.builder().name("RT").subjects("rt.>")
                        .storageType(StorageType.Memory).build();
                try {
                    jsm.addStream(sc);
                } catch (Exception ex) {
                    jsm.updateStream(sc);
                }
            }
        }

        public void publish(Hub.Event e) throws Exception {
            byte[] data = json(e).getBytes();
            if (js) nc.jetStream().publish("rt." + e.topic(), data);
            else nc.publish("rt." + e.topic(), data);
        }

        public void run(Hub hub) {
            try {
                if (!js) {
                    nc.createDispatcher(m -> hub.deliver(parse(new String(m.getData())))).subscribe("rt.>");
                    return;
                }
                JetStream jet = nc.jetStream();
                PushSubscribeOptions o = PushSubscribeOptions.builder().ordered(true)
                        .configuration(ConsumerConfiguration.builder().deliverPolicy(DeliverPolicy.New).build())
                        .build();
                jet.subscribe("rt.>", nc.createDispatcher(), m -> hub.deliver(parse(new String(m.getData()))),
                        false, o);
            } catch (Exception ex) {
                throw new IllegalStateException(ex);
            }
        }

        public boolean up() { return nc.getStatus() == Connection.Status.CONNECTED; }

        public List<Hub.Event> history(String topic, long after) throws Exception {
            if (!js) return null;
            JetStreamManagement jsm = nc.jetStreamManagement();
            long last = jsm.getStreamInfo("RT").getStreamState().getLastSequence();
            List<Hub.Event> out = new ArrayList<>();
            for (long seq = 1; seq <= last; seq++) {
                try {
                    var m = jsm.getMessage("RT", seq);
                    if (!m.getSubject().equals("rt." + topic)) continue;
                    Hub.Event e = parse(new String(m.getData()));
                    if (e.id() > after) out.add(e);
                } catch (Exception ignored) {
                    // номер удалён или пуст
                }
            }
            return out;
        }
    }

    // --- общее ----------------------------------------------------------------------

    interface Connect<T> {
        T get();
    }

    static <T> T connectRetry(Connect<T> c) {
        RuntimeException last = null;
        for (int i = 0; i < 50; i++) {
            try {
                return c.get();
            } catch (RuntimeException ex) {
                last = ex;
                sleep(200);
            }
        }
        throw last;
    }

    static void sleep(long ms) {
        try {
            Thread.sleep(ms);
        } catch (InterruptedException ignored) {
            Thread.currentThread().interrupt();
        }
    }
}
