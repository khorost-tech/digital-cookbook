package tech.khorost.grpc;

import com.google.protobuf.ByteString;
import com.google.protobuf.Any;
import com.google.rpc.Code;
import com.google.rpc.ErrorInfo;
import com.sun.net.httpserver.HttpServer;
import io.grpc.Context;
import io.grpc.Contexts;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.ServerCall;
import io.grpc.ServerCallHandler;
import io.grpc.ServerInterceptor;
import io.grpc.ServerInterceptors;
import io.grpc.ServerTransportFilter;
import io.grpc.Status;
import io.grpc.StatusRuntimeException;
import io.grpc.netty.shaded.io.grpc.netty.NettyServerBuilder;
import io.grpc.protobuf.StatusProto;
import io.grpc.protobuf.services.HealthStatusManager;
import io.grpc.stub.ServerCallStreamObserver;
import io.grpc.stub.StreamObserver;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.TimeUnit;
import tech.khorost.grpc.demo.v1.ChainReply;
import tech.khorost.grpc.demo.v1.ChainRequest;
import tech.khorost.grpc.demo.v1.DemoGrpc;
import tech.khorost.grpc.demo.v1.Item;
import tech.khorost.grpc.demo.v1.StreamRequest;
import tech.khorost.grpc.demo.v1.Summary;
import tech.khorost.grpc.demo.v1.UnaryReply;
import tech.khorost.grpc.demo.v1.UnaryRequest;

/** Сервер стенда grpc на Java — тот же контракт и те же GRPC_*, что go/cmd/server. */
public final class Server {
    static String env(String name, String def) {
        String v = System.getenv(name);
        return v == null || v.isEmpty() ? def : v;
    }

    static final String INSTANCE = env("GRPC_INSTANCE", "java-1");

    /** Счётчики — те же имена, что у Go. */
    static final class Stats {
        long chainCalls, stepsDone, stepsAfterCancel, cancelSeen;
        long streamSent, streamSentAfterCancel, streamSendErrors, streamCancelSeen;
        long metricsCalls, logCalls, handlerCalls, conns, calls, streamCalls;
        long cancelHandlerCalls, cancelHandlerBeforeEnd;
        Map<String, Long> metricsByCode = new TreeMap<>();
        Map<String, Long> attempts = new TreeMap<>();
    }

    static Stats st = new Stats();

    static synchronized void reset() { st = new Stats(); }

    static synchronized String statsJson() {
        StringBuilder b = new StringBuilder("{");
        b.append("\"instance\":\"").append(INSTANCE).append("\",\"server\":\"java\"");
        Object[][] f = {{"chain_calls", st.chainCalls}, {"steps_done", st.stepsDone},
                {"steps_after_cancel", st.stepsAfterCancel}, {"cancel_seen", st.cancelSeen},
                {"stream_sent", st.streamSent}, {"stream_sent_after_cancel", st.streamSentAfterCancel},
                {"stream_send_errors", st.streamSendErrors}, {"stream_cancel_seen", st.streamCancelSeen},
                {"metrics_calls", st.metricsCalls}, {"log_calls", st.logCalls}, {"handler_calls", st.handlerCalls},
                {"conns", st.conns}, {"calls", st.calls}, {"stream_calls", st.streamCalls},
                {"cancel_handler_calls", st.cancelHandlerCalls}, {"cancel_handler_before_end", st.cancelHandlerBeforeEnd}};
        for (Object[] kv : f) b.append(",\"").append(kv[0]).append("\":").append(kv[1]);
        b.append(",\"metrics_by_code\":").append(map(st.metricsByCode));
        b.append(",\"attempts\":").append(map(st.attempts)).append("}");
        return b.toString();
    }

    static String map(Map<String, Long> m) {
        StringBuilder b = new StringBuilder("{");
        for (var e : m.entrySet()) {
            if (b.length() > 1) b.append(",");
            b.append("\"").append(e.getKey()).append("\":").append(e.getValue());
        }
        return b.append("}").toString();
    }

    static synchronized void inc(java.util.function.Consumer<Stats> f) { f.accept(st); }

    /** Номер попытки из grpc-previous-rpc-attempts — через Context, как в Go через metadata. */
    static final Context.Key<Integer> ATTEMPT = Context.key("attempt");
    static final Metadata.Key<String> PREV = Metadata.Key.of("grpc-previous-rpc-attempts",
            Metadata.ASCII_STRING_MARSHALLER);
    static final Metadata.Key<String> AUTH = Metadata.Key.of("authorization", Metadata.ASCII_STRING_MARSHALLER);

    static final class Demo extends DemoGrpc.DemoImplBase {
        final boolean checkCancel = env("GRPC_STREAM_CHECK_CANCEL", "0").equals("1");
        final int failAfter = Integer.parseInt(env("GRPC_STREAM_FAIL_AFTER", "-1"));
        // Где идёт цикл отправки: sync — внутри обработчика; async — в отдельной задаче
        // после возврата обработчика; *-handler — с setOnCancelHandler.
        final String send = env("GRPC_STREAM_SEND", "sync");
        final java.util.concurrent.ExecutorService pool = java.util.concurrent.Executors.newCachedThreadPool();
        final Map<String, DemoGrpc.DemoBlockingStub> clients = new ConcurrentHashMap<>();

        @Override
        public void unary(UnaryRequest r, StreamObserver<UnaryReply> out) {
            inc(s -> { s.handlerCalls++; s.calls++; });
            int attempt = ATTEMPT.get() == null ? 1 : ATTEMPT.get();
            switch (r.getMode()) {
                case "plain" -> throw new IllegalStateException("запись не найдена"); // обычное исключение
                case "status" -> { out.onError(Status.NOT_FOUND.withDescription("запись не найдена").asRuntimeException()); return; }
                case "details" -> {
                    com.google.rpc.Status s = com.google.rpc.Status.newBuilder().setCode(Code.NOT_FOUND_VALUE)
                            .setMessage("запись не найдена").addDetails(Any.pack(ErrorInfo.newBuilder()
                                    .setReason("RECORD_NOT_FOUND").setDomain("demo.khorost.tech")
                                    .putMetadata("key", r.getKey()).build())).build();
                    out.onError(StatusProto.toStatusRuntimeException(s));
                    return;
                }
                case "invalid" -> { out.onError(Status.INVALID_ARGUMENT.withDescription("ключ пуст").asRuntimeException()); return; }
                case "unavailable" -> { out.onError(Status.UNAVAILABLE.withDescription("зависимость недоступна").asRuntimeException()); return; }
                case "panic" -> { Map<String, Integer> m = null; m.put("x", 1); } // NPE — аналог паники
                case "fail" -> {
                    long n;
                    synchronized (Server.class) { n = st.attempts.merge(r.getKey(), 1L, Long::sum); }
                    if (n <= r.getFailTimes()) {
                        Status.Code c = Status.Code.valueOf(r.getFailCode());
                        out.onError(Status.fromCode(c).withDescription("отказ " + n + " из " + r.getFailTimes()).asRuntimeException());
                        return;
                    }
                }
                default -> { }
            }
            out.onNext(UnaryReply.newBuilder().setInstance(INSTANCE).setAttempt(attempt)
                    .setPayload(ByteString.copyFrom(new byte[r.getSize()])).build());
            out.onCompleted();
        }

        @Override
        public void serverStream(StreamRequest r, StreamObserver<Item> o) {
            ServerCallStreamObserver<Item> out = (ServerCallStreamObserver<Item>) o;
            inc(s -> s.streamCalls++);
            // Обработчик отмены — callback: grpc-java доставляет его тем же
            // последовательным исполнителем, что и вызов обработчика, поэтому при
            // цикле внутри обработчика он ждёт возврата. Он же отключает
            // исключение CANCELLED из onNext после отмены.
            java.util.concurrent.atomic.AtomicBoolean loopDone = new java.util.concurrent.atomic.AtomicBoolean();
            if (send.endsWith("-handler")) {
                out.setOnCancelHandler(() -> inc(s -> {
                    s.cancelHandlerCalls++;
                    if (!loopDone.get()) s.cancelHandlerBeforeEnd++;
                }));
            }
            Runnable loop = () -> {
                sendLoop(r, out);
                loopDone.set(true);
            };
            if (send.startsWith("async")) pool.execute(loop);
            else loop.run();
        }

        void sendLoop(StreamRequest r, ServerCallStreamObserver<Item> out) {
            boolean cancelled = false;
            for (int i = 1; i <= r.getCount(); i++) {
                if (failAfter >= 0 && i - 1 == failAfter) { // граница повторов (ось 5)
                    out.onError(Status.UNAVAILABLE.withDescription("отказ стрима").asRuntimeException());
                    return;
                }
                if (!cancelled && out.isCancelled()) {
                    cancelled = true;
                    inc(s -> s.streamCancelSeen++);
                }
                if (cancelled && checkCancel) return;
                try {
                    out.onNext(Item.newBuilder().setId(i).setText("item").build());
                    if (cancelled) inc(s -> s.streamSentAfterCancel++);
                    else inc(s -> s.streamSent++);
                } catch (StatusRuntimeException | IllegalStateException e) {
                    inc(s -> s.streamSendErrors++);
                }
                if (i < r.getCount()) sleep(r.getIntervalMs()); // пауза — между сообщениями
            }
            if (!out.isCancelled()) out.onCompleted();
        }

        @Override
        public StreamObserver<Item> clientStream(StreamObserver<Summary> out) {
            return new StreamObserver<>() {
                int n;
                public void onNext(Item it) { n++; }
                public void onError(Throwable t) { }
                public void onCompleted() { out.onNext(Summary.newBuilder().setCount(n).build()); out.onCompleted(); }
            };
        }

        @Override
        public StreamObserver<Item> bidi(StreamObserver<Item> out) {
            return new StreamObserver<>() {
                public void onNext(Item it) { out.onNext(it); }
                public void onError(Throwable t) { }
                public void onCompleted() { out.onCompleted(); }
            };
        }

        @Override
        public void chain(ChainRequest r, StreamObserver<ChainReply> out) {
            if (!r.getNext().isEmpty()) { // звено-посредник
                DemoGrpc.DemoBlockingStub next = clients.computeIfAbsent(r.getNext(), a -> DemoGrpc.newBlockingStub(
                        ManagedChannelBuilder.forTarget(a).usePlaintext().build()));
                ChainRequest down = ChainRequest.newBuilder().setSteps(r.getSteps()).setStepMs(r.getStepMs()).build();
                try {
                    // Передача — текущий Context (срок и отмена входящего вызова); ошибка
                    // опыта — Context.ROOT: нижнее звено не узнает, что клиент ушёл.
                    ChainReply rep = r.getPropagate() ? next.chain(down) : Context.ROOT.call(() -> next.chain(down));
                    out.onNext(rep);
                    out.onCompleted();
                } catch (StatusRuntimeException e) {
                    out.onError(e);
                } catch (Exception e) {
                    out.onError(Status.INTERNAL.withCause(e).asRuntimeException());
                }
                return;
            }
            inc(s -> s.chainCalls++); // исполнитель: работа шагами, отмена проверяется перед шагом
            Context ctx = Context.current();
            int done = 0;
            boolean seen = false;
            for (int i = 0; i < r.getSteps(); i++) {
                if (ctx.isCancelled()) {
                    seen = true;
                    inc(s -> s.cancelSeen++);
                    break;
                }
                sleep(r.getStepMs());
                done++;
                inc(s -> s.stepsDone++);
            }
            if (!seen && ctx.isCancelled()) inc(s -> s.cancelSeen++);
            out.onNext(ChainReply.newBuilder().setInstance(INSTANCE).setStepsDone(done).build());
            out.onCompleted();
        }
    }

    static void sleep(long ms) {
        try {
            Thread.sleep(ms);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    // --- интерсепторы ------------------------------------------------------------------

    /** Номер попытки в Context — до остальных интерсепторов. */
    static final ServerInterceptor attemptInterceptor = new ServerInterceptor() {
        public <Q, A> ServerCall.Listener<Q> interceptCall(ServerCall<Q, A> call, Metadata h, ServerCallHandler<Q, A> next) {
            String prev = h.get(PREV);
            int attempt = prev == null ? 1 : Integer.parseInt(prev) + 1;
            return Contexts.interceptCall(Context.current().withValue(ATTEMPT, attempt), call, h, next);
        }
    };

    /** Граница повторов (ось 5): заголовки ответа уходят до отказа стрима — вызов не trailers-only. */
    static final boolean HEADERS_FIRST = env("GRPC_STREAM_HEADERS_FIRST", "0").equals("1");
    static final ServerInterceptor headersFirst = new ServerInterceptor() {
        public <Q, A> ServerCall.Listener<Q> interceptCall(ServerCall<Q, A> call, Metadata h, ServerCallHandler<Q, A> next) {
            if (!HEADERS_FIRST || !call.getMethodDescriptor().getBareMethodName().equals("ServerStream")) {
                return next.startCall(call, h);
            }
            call.sendHeaders(new Metadata());
            return next.startCall(new io.grpc.ForwardingServerCall.SimpleForwardingServerCall<>(call) {
                @Override
                public void sendHeaders(Metadata m) { } // уже отправлены
            }, h);
        }
    };

    static final ServerInterceptor auth = new ServerInterceptor() {
        public <Q, A> ServerCall.Listener<Q> interceptCall(ServerCall<Q, A> call, Metadata h, ServerCallHandler<Q, A> next) {
            if (!"Bearer ok".equals(h.get(AUTH))) {
                call.close(Status.UNAUTHENTICATED.withDescription("нет токена"), new Metadata());
                return new ServerCall.Listener<>() { };
            }
            return next.startCall(call, h);
        }
    };

    static final ServerInterceptor metrics = new ServerInterceptor() {
        public <Q, A> ServerCall.Listener<Q> interceptCall(ServerCall<Q, A> call, Metadata h, ServerCallHandler<Q, A> next) {
            return next.startCall(new io.grpc.ForwardingServerCall.SimpleForwardingServerCall<>(call) {
                @Override
                public void close(Status s, Metadata t) {
                    inc(x -> { x.metricsCalls++; x.metricsByCode.merge(camel(s.getCode().name()), 1L, Long::sum); });
                    super.close(s, t);
                }
            }, h);
        }
    };

    static final ServerInterceptor logging = new ServerInterceptor() {
        public <Q, A> ServerCall.Listener<Q> interceptCall(ServerCall<Q, A> call, Metadata h, ServerCallHandler<Q, A> next) {
            inc(s -> s.logCalls++);
            return next.startCall(call, h);
        }
    };

    /** Исключение обработчика → INTERNAL (по умолчанию grpc-java отдаёт UNKNOWN и живёт дальше). */
    static final ServerInterceptor recover = new ServerInterceptor() {
        public <Q, A> ServerCall.Listener<Q> interceptCall(ServerCall<Q, A> call, Metadata h, ServerCallHandler<Q, A> next) {
            ServerCall.Listener<Q> l = next.startCall(call, h);
            return new io.grpc.ForwardingServerCallListener.SimpleForwardingServerCallListener<>(l) {
                @Override
                public void onHalfClose() {
                    try {
                        super.onHalfClose();
                    } catch (RuntimeException e) {
                        call.close(Status.INTERNAL.withDescription("исключение в обработчике: " + e), new Metadata());
                    }
                }
            };
        }
    };

    /** Имена кодов как у Go: NOT_FOUND → NotFound. */
    static String camel(String s) {
        if (s.equals("OK")) return "OK"; // у Go код OK пишется целиком заглавными
        StringBuilder b = new StringBuilder();
        for (String p : s.toLowerCase().split("_")) b.append(Character.toUpperCase(p.charAt(0))).append(p.substring(1));
        return b.toString();
    }

    public static void main(String[] args) throws Exception {
        // Порядок: ServerInterceptors.intercept применяет список так, что ПОСЛЕДНИЙ
        // в списке вызывается ПЕРВЫМ — список строится в обратном порядке.
        List<ServerInterceptor> order = new ArrayList<>();
        String list = env("GRPC_CHAIN", "");
        if (!list.isEmpty()) { // снаружи внутрь, как в Go
            Map<String, ServerInterceptor> byName = Map.of("recover", recover, "auth", auth, "metrics", metrics,
                    "log", logging);
            for (String n : list.split(",")) order.add(byName.get(n));
        } else {
            switch (env("GRPC_CHAIN_ORDER", "none")) {
                case "auth-first" -> order.addAll(List.of(auth, metrics, logging));
                case "metrics-first" -> order.addAll(List.of(metrics, logging, auth));
                default -> { }
            }
            if (env("GRPC_RECOVER", "0").equals("1")) order.add(0, recover);
        }
        order.add(0, headersFirst);
        order.add(0, attemptInterceptor);
        List<ServerInterceptor> reversed = new ArrayList<>(order);
        java.util.Collections.reverse(reversed);

        NettyServerBuilder b = NettyServerBuilder.forPort(Integer.parseInt(env("GRPC_PORT", "9090")))
                .addService(ServerInterceptors.intercept(new Demo(), reversed))
                .addService(new HealthStatusManager().getHealthService())
                .addTransportFilter(new ServerTransportFilter() {
                    @Override
                    public io.grpc.Attributes transportReady(io.grpc.Attributes a) {
                        inc(s -> s.conns++);
                        return a;
                    }
                });
        long ka = Long.parseLong(env("GRPC_KA_MIN_TIME_MS", "0"));
        if (ka > 0) b.permitKeepAliveTime(ka, TimeUnit.MILLISECONDS);
        if (env("GRPC_KA_PERMIT_NO_STREAM", "0").equals("1")) b.permitKeepAliveWithoutCalls(true);
        io.grpc.Server srv = b.build().start();

        HttpServer http = HttpServer.create(new InetSocketAddress(Integer.parseInt(env("GRPC_STATS_PORT", "8081"))), 0);
        http.createContext("/stats", ex -> {
            byte[] body = statsJson().getBytes(StandardCharsets.UTF_8);
            ex.getResponseHeaders().set("Content-Type", "application/json");
            ex.sendResponseHeaders(200, body.length);
            try (OutputStream os = ex.getResponseBody()) { os.write(body); }
        });
        http.createContext("/reset", ex -> { reset(); ex.sendResponseHeaders(204, -1); ex.close(); });
        http.start();
        System.out.printf("%s: gRPC :%s, интерсепторы %s, recover %s%n", INSTANCE, env("GRPC_PORT", "9090"),
                env("GRPC_CHAIN_ORDER", "none"), env("GRPC_RECOVER", "0"));
        srv.awaitTermination();
    }
}
