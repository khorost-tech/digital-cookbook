package tech.khorost.grpc;

import com.google.rpc.ErrorInfo;
import io.grpc.Context;
import io.grpc.ManagedChannel;
import io.grpc.Metadata;
import io.grpc.Status;
import io.grpc.StatusRuntimeException;
import io.grpc.health.v1.HealthCheckRequest;
import io.grpc.health.v1.HealthCheckResponse;
import io.grpc.health.v1.HealthGrpc;
import io.grpc.netty.shaded.io.grpc.netty.NettyChannelBuilder;
import io.grpc.protobuf.StatusProto;
import io.grpc.stub.MetadataUtils;
import io.grpc.stub.StreamObserver;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import tech.khorost.grpc.demo.v1.ChainReply;
import tech.khorost.grpc.demo.v1.ChainRequest;
import tech.khorost.grpc.demo.v1.DemoGrpc;
import tech.khorost.grpc.demo.v1.Item;
import tech.khorost.grpc.demo.v1.StreamRequest;
import tech.khorost.grpc.demo.v1.Summary;
import tech.khorost.grpc.demo.v1.UnaryReply;
import tech.khorost.grpc.demo.v1.UnaryRequest;

/** Клиент-проба стенда grpc на Java — те же подкоманды и тот же JSON, что go/cmd/client. */
public final class Client {
    final Map<String, String> f = new HashMap<>();

    String s(String k, String d) { return f.getOrDefault(k, d); }

    int i(String k, int d) { return Integer.parseInt(s(k, Integer.toString(d))); }

    boolean b(String k, boolean d) { return Boolean.parseBoolean(s(k, Boolean.toString(d))); }

    long dur(String k, long dms) {
        String v = f.get(k);
        if (v == null) return dms;
        if (v.endsWith("ms")) return Long.parseLong(v.substring(0, v.length() - 2));
        if (v.endsWith("s")) return (long) (Double.parseDouble(v.substring(0, v.length() - 1)) * 1000);
        return Long.parseLong(v);
    }

    ManagedChannel channel() {
        NettyChannelBuilder cb = NettyChannelBuilder.forTarget(s("target", "server:9090")).usePlaintext();
        String retry = s("retry", "");
        if (!retry.isEmpty()) {
            cb.defaultServiceConfig(Json.parseObject(retry)).enableRetry();
        } else if (!s("lb", "").isEmpty()) {
            cb.defaultLoadBalancingPolicy(s("lb", ""));
        }
        if (i("max-recv", 0) > 0) cb.maxInboundMessageSize(i("max-recv", 0));
        if (i("ka-time-ms", 0) > 0) {
            cb.keepAliveTime(i("ka-time-ms", 0), TimeUnit.MILLISECONDS).keepAliveTimeout(2, TimeUnit.SECONDS)
                    .keepAliveWithoutCalls(b("permit", false));
        }
        return cb.build();
    }

    DemoGrpc.DemoBlockingStub stub(ManagedChannel ch, String token) {
        DemoGrpc.DemoBlockingStub st = DemoGrpc.newBlockingStub(ch);
        if (!token.isEmpty()) {
            Metadata m = new Metadata();
            m.put(Server.AUTH, "Bearer " + token);
            st = st.withInterceptors(MetadataUtils.newAttachHeadersInterceptor(m));
        }
        return st;
    }

    static Map<String, Object> result(Throwable t) {
        Map<String, Object> out = new LinkedHashMap<>();
        if (t == null) {
            out.put("code", "OK");
            out.put("message", "");
            out.put("details_reason", "");
            return out;
        }
        Status s = Status.fromThrowable(t);
        out.put("code", Server.camel(s.getCode().name()));
        out.put("message", s.getDescription() == null ? "" : s.getDescription());
        String reason = "";
        com.google.rpc.Status rs = StatusProto.fromThrowable(t);
        if (rs != null) {
            for (var d : rs.getDetailsList()) {
                if (d.is(ErrorInfo.class)) {
                    try {
                        reason = d.unpack(ErrorInfo.class).getReason();
                    } catch (Exception ignored) {
                        // не тот тип
                    }
                }
            }
        }
        out.put("details_reason", reason);
        return out;
    }

    static boolean healthy(String target) {
        ManagedChannel ch = NettyChannelBuilder.forTarget(target).usePlaintext().build();
        try {
            return HealthGrpc.newBlockingStub(ch).withDeadlineAfter(2, TimeUnit.SECONDS)
                    .check(HealthCheckRequest.getDefaultInstance()).getStatus() == HealthCheckResponse.ServingStatus.SERVING;
        } catch (Exception e) {
            return false;
        } finally {
            ch.shutdownNow();
        }
    }

    Map<String, Object> unary() {
        ManagedChannel ch = channel();
        try {
            DemoGrpc.DemoBlockingStub st = stub(ch, s("token", ""));
            if (i("deadline-ms", 0) > 0) st = st.withDeadlineAfter(i("deadline-ms", 0), TimeUnit.MILLISECONDS);
            Map<String, Object> out;
            try {
                UnaryReply r = st.unary(UnaryRequest.newBuilder().setMode(s("mode", "ok")).setKey(s("key", "k"))
                        .setFailCode(s("fail-code", "UNAVAILABLE")).setFailTimes(i("fail-times", 0))
                        .setSize(i("size", 0)).build());
                out = result(null);
                out.put("attempt", r.getAttempt());
                out.put("instance", r.getInstance());
                out.put("payload_len", r.getPayload().size());
            } catch (StatusRuntimeException e) {
                out = result(e);
            }
            out.put("alive_after", healthy(s("target", "server:9090")));
            return out;
        } finally {
            ch.shutdownNow();
        }
    }

    Map<String, Object> chain() {
        ManagedChannel ch = channel();
        try {
            DemoGrpc.DemoBlockingStub st = stub(ch, s("token", ""));
            if (i("deadline-ms", 0) > 0) st = st.withDeadlineAfter(i("deadline-ms", 0), TimeUnit.MILLISECONDS);
            try {
                ChainReply r = st.chain(ChainRequest.newBuilder().setSteps(i("steps", 10)).setStepMs(i("step-ms", 100))
                        .setNext(s("next", "")).setPropagate(b("propagate", true)).build());
                Map<String, Object> out = result(null);
                out.put("steps_done", r.getStepsDone());
                return out;
            } catch (StatusRuntimeException e) {
                return result(e);
            }
        } finally {
            ch.shutdownNow();
        }
    }

    Map<String, Object> cancel() throws Exception {
        ManagedChannel ch = channel();
        try {
            int count = i("count", 20), interval = i("interval-ms", 100), read = i("read", 3);
            Context.CancellableContext ctx = Context.current().withCancellation();
            int got = ctx.call(() -> {
                Iterator<Item> it = DemoGrpc.newBlockingStub(ch).serverStream(
                        StreamRequest.newBuilder().setCount(count).setIntervalMs(interval).build());
                int n = 0;
                while (n < read && it.hasNext()) {
                    it.next();
                    n++;
                }
                return n;
            });
            ctx.cancel(null);
            Thread.sleep((long) count * interval + 1000); // сервер дошёл до конца цикла
            Map<String, Object> out = new LinkedHashMap<>();
            out.put("received", got);
            out.put("read", read);
            out.put("count", count);
            return out;
        } finally {
            ch.shutdownNow();
        }
    }

    Map<String, Object> keepalive() {
        ManagedChannel ch = channel();
        try {
            int got = 0;
            Throwable err = null;
            try {
                Iterator<Item> it = DemoGrpc.newBlockingStub(ch).withDeadlineAfter(dur("duration", 10000), TimeUnit.MILLISECONDS)
                        .serverStream(StreamRequest.newBuilder().setCount(i("count", 20)).setIntervalMs(i("interval-ms", 100)).build());
                while (it.hasNext()) {
                    it.next();
                    got++;
                }
            } catch (StatusRuntimeException e) {
                err = e;
            }
            Map<String, Object> out = result(err);
            out.put("received", got);
            return out;
        } finally {
            ch.shutdownNow();
        }
    }

    /** Локальный адрес TCP-соединения последнего вызова — из атрибутов транспорта. */
    static final class LocalAddr implements io.grpc.ClientInterceptor {
        volatile String last = "";

        public <Q, A> io.grpc.ClientCall<Q, A> interceptCall(io.grpc.MethodDescriptor<Q, A> m, io.grpc.CallOptions o,
                                                              io.grpc.Channel next) {
            return new io.grpc.ForwardingClientCall.SimpleForwardingClientCall<>(next.newCall(m, o)) {
                @Override
                public void start(Listener<A> l, Metadata h) {
                    io.grpc.ClientCall<Q, A> call = this;
                    super.start(new io.grpc.ForwardingClientCallListener.SimpleForwardingClientCallListener<>(l) {
                        @Override
                        public void onHeaders(Metadata hh) {
                            Object a = call.getAttributes().get(io.grpc.Grpc.TRANSPORT_ATTR_LOCAL_ADDR);
                            last = a == null ? "" : a.toString();
                            super.onHeaders(hh);
                        }
                    }, h);
                }
            };
        }
    }

    Map<String, Object> idle() throws Exception {
        ManagedChannel ch = channel();
        try {
            LocalAddr local = new LocalAddr();
            DemoGrpc.DemoBlockingStub st = DemoGrpc.newBlockingStub(ch).withInterceptors(local);
            Map<String, Object> first = call(st);
            String firstLocal = local.last;
            Thread.sleep(dur("idle", 6000));
            // Состояние — второстепенный признак: READY бывает и после переподключения.
            String state = ch.getState(false).name();
            local.last = "";
            Map<String, Object> second = call(st);
            Map<String, Object> out = new LinkedHashMap<>();
            out.put("first", first.get("code"));
            out.put("second", second.get("code"));
            out.put("second_message", second.get("message"));
            out.put("state_before_second", state);
            out.put("first_local", firstLocal);
            out.put("second_local", local.last);
            out.put("idle_ms", dur("idle", 6000));
            return out;
        } finally {
            ch.shutdownNow();
        }
    }

    static Map<String, Object> call(DemoGrpc.DemoBlockingStub st) {
        try {
            st.withDeadlineAfter(5, TimeUnit.SECONDS).unary(UnaryRequest.newBuilder().setMode("ok").build());
            return result(null);
        } catch (StatusRuntimeException e) {
            return result(e);
        }
    }

    Map<String, Object> balance() {
        ManagedChannel ch = channel();
        try {
            DemoGrpc.DemoBlockingStub st = DemoGrpc.newBlockingStub(ch);
            Map<String, Long> per = new TreeMap<>();
            int errors = 0, n = i("n", 30);
            for (int k = 0; k < n; k++) {
                try {
                    per.merge(st.withDeadlineAfter(5, TimeUnit.SECONDS).unary(UnaryRequest.newBuilder().setMode("ok").build())
                            .getInstance(), 1L, Long::sum);
                } catch (StatusRuntimeException e) {
                    errors++;
                }
            }
            Map<String, Object> out = new LinkedHashMap<>();
            out.put("calls", n);
            out.put("errors", errors);
            out.put("per_instance", per);
            out.put("instances", per.size());
            return out;
        } finally {
            ch.shutdownNow();
        }
    }

    Map<String, Object> streamRetry() {
        ManagedChannel ch = channel();
        try {
            int got = 0;
            Throwable err = null;
            try {
                Iterator<Item> it = DemoGrpc.newBlockingStub(ch).withDeadlineAfter(10, TimeUnit.SECONDS)
                        .serverStream(StreamRequest.newBuilder().setCount(i("count", 3)).build());
                while (it.hasNext()) {
                    it.next();
                    got++;
                }
            } catch (StatusRuntimeException e) {
                err = e;
            }
            Map<String, Object> out = result(err);
            out.put("received", got);
            return out;
        } finally {
            ch.shutdownNow();
        }
    }

    Map<String, Object> calls() {
        ManagedChannel ch = channel();
        try {
            Map<String, Long> codes = new TreeMap<>();
            int n = i("n", 30);
            for (String tok : List.of("", "ok")) {
                DemoGrpc.DemoBlockingStub st = stub(ch, tok);
                for (int k = 0; k < n; k++) codes.merge((String) call(st).get("code"), 1L, Long::sum);
            }
            Map<String, Object> out = new LinkedHashMap<>();
            out.put("calls_without_token", n);
            out.put("calls_with_token", n);
            out.put("codes", codes);
            return out;
        } finally {
            ch.shutdownNow();
        }
    }

    Map<String, Object> check() throws Exception {
        ManagedChannel ch = channel();
        try {
            DemoGrpc.DemoBlockingStub st = DemoGrpc.newBlockingStub(ch).withDeadlineAfter(5, TimeUnit.SECONDS);
            Map<String, Object> obs = new TreeMap<>();
            for (String m : List.of("ok", "plain", "status", "details", "invalid", "unavailable")) {
                Map<String, Object> r;
                try {
                    st.unary(UnaryRequest.newBuilder().setMode(m).setKey("k").build());
                    r = result(null);
                } catch (StatusRuntimeException e) {
                    r = result(e);
                }
                obs.put("unary_" + m, List.of(r.get("code"), r.get("details_reason")));
            }
            List<Long> ids = new ArrayList<>();
            st.serverStream(StreamRequest.newBuilder().setCount(3).build()).forEachRemaining(it -> ids.add(it.getId()));
            obs.put("server_stream", ids);
            DemoGrpc.DemoStub async = DemoGrpc.newStub(ch);
            CountDownLatch done = new CountDownLatch(1);
            int[] sum = {0};
            StreamObserver<Item> cs = async.clientStream(new StreamObserver<>() {
                public void onNext(Summary s) { sum[0] = s.getCount(); }
                public void onError(Throwable t) { done.countDown(); }
                public void onCompleted() { done.countDown(); }
            });
            for (int k = 1; k <= 3; k++) cs.onNext(Item.newBuilder().setId(k).build());
            cs.onCompleted();
            done.await(5, TimeUnit.SECONDS);
            obs.put("client_stream", sum[0]);
            List<Long> echo = new ArrayList<>();
            CountDownLatch bd = new CountDownLatch(1);
            StreamObserver<Item> bs = async.bidi(new StreamObserver<>() {
                public void onNext(Item it) { synchronized (echo) { echo.add(it.getId()); } }
                public void onError(Throwable t) { bd.countDown(); }
                public void onCompleted() { bd.countDown(); }
            });
            for (int k = 1; k <= 3; k++) bs.onNext(Item.newBuilder().setId(k).build());
            bs.onCompleted();
            bd.await(5, TimeUnit.SECONDS);
            obs.put("bidi", echo);
            obs.put("chain_steps", st.chain(ChainRequest.newBuilder().setSteps(2).setStepMs(10).build()).getStepsDone());
            obs.put("_keys", obs.size());
            return obs;
        } finally {
            ch.shutdownNow();
        }
    }

    public static void main(String[] args) throws Exception {
        Client c = new Client();
        for (int k = 1; k < args.length; k++) {
            String a = args[k].replaceFirst("^-+", "");
            if (a.contains("=")) {
                c.f.put(a.substring(0, a.indexOf('=')), a.substring(a.indexOf('=') + 1));
            } else if (k + 1 < args.length && !args[k + 1].startsWith("-")) {
                c.f.put(a, args[++k]);
            } else {
                c.f.put(a, "true");
            }
        }
        Map<String, Object> out = switch (args[0]) {
            case "check" -> c.check();
            case "unary" -> c.unary();
            case "chain" -> c.chain();
            case "cancel" -> c.cancel();
            case "keepalive" -> c.keepalive();
            case "idle" -> c.idle();
            case "balance" -> c.balance();
            case "calls" -> c.calls();
            case "stream-retry" -> c.streamRetry();
            default -> throw new IllegalArgumentException("подкоманда: " + args[0]);
        };
        out.put("client", "java");
        System.out.println(Json.write(out));
        System.exit(0);
    }
}
