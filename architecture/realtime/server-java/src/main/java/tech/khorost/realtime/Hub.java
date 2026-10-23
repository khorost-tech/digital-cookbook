package tech.khorost.realtime;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicLong;
import reactor.core.publisher.Flux;
import reactor.core.publisher.FluxSink;
import reactor.core.publisher.Mono;
import reactor.core.publisher.Sinks;

/**
 * Подписчики инстанса по темам и недавние события для опроса — то же, что hub.go.
 * Очередь соединения своя, а не оператор Reactor: политика и счётчики должны
 * совпадать с Go-сервером, иначе ось 5 сравнивала бы библиотеки, а не политики.
 */
final class Hub {
    record Event(long id, String topic, String payload) {}

    static final String UNBOUNDED = "unbounded", DROP_OLDEST = "drop-oldest", DISCONNECT = "disconnect";
    private static final int RECENT_KEEP = 4096;

    static final AtomicLong conns = new AtomicLong(), delivered = new AtomicLong(), dropped = new AtomicLong(),
            disconnected = new AtomicLong(), maxQueue = new AtomicLong();

    private final Map<String, Set<Subscriber>> subs = new ConcurrentHashMap<>();
    private final Map<String, List<Event>> recent = new ConcurrentHashMap<>();
    private final Map<String, Sinks.Empty<Void>> waiters = new ConcurrentHashMap<>();

    /** Очередь одного соединения. Сеть просит событий (request) — отдаём; не просит — копим здесь. */
    final class Subscriber {
        private final ArrayDeque<Event> items = new ArrayDeque<>();
        private final String policy;
        private final int limit;
        private long requested;
        private int maxLen;
        private long droppedHere;
        private FluxSink<Event> sink;
        private boolean closed;

        Subscriber(String policy, int limit) {
            this.policy = policy;
            this.limit = limit;
        }

        synchronized void push(Event e) {
            if (closed) return;
            if (sink != null && requested > 0 && items.isEmpty()) {
                requested--;
                sink.next(e);
                return;
            }
            if (!policy.equals(UNBOUNDED) && items.size() >= limit) {
                if (policy.equals(DROP_OLDEST)) {
                    items.pollFirst();
                    droppedHere++;
                } else {
                    closed = true;
                    disconnected.incrementAndGet();
                    if (sink != null) sink.complete(); // соединение закрывается
                    return;
                }
            }
            items.addLast(e);
            if (items.size() > maxLen) maxLen = items.size();
        }

        private synchronized void request(long n) {
            requested = (requested + n < 0) ? Long.MAX_VALUE : requested + n;
            while (requested > 0 && !items.isEmpty()) {
                requested--;
                sink.next(items.pollFirst());
            }
        }

        synchronized int maxLen() { return maxLen; }
        synchronized long dropped() { return droppedHere; }
    }

    /**
     * Поток событий темы для соединения. Подписчик встаёт в хаб сразу, при вызове,
     * а не при подписке на Flux: пока SSE дочитывает историю, живые события копятся
     * в его очереди — стык без пропуска, как в hub.go. Отписка — при отмене или закрытии.
     */
    Flux<Event> stream(String topic, String policy, int limit) {
        return open(topic, policy, limit).flux();
    }

    /** Поток и явное освобождение регистрации — для отмены до подписки на поток. */
    record Stream(Flux<Event> flux, Runnable release) {}

    /**
     * Регистрация в хабе сразу, при вызове. Снять её обязан тот, кто вызвал: если
     * на поток так и не подпишутся (клиент ушёл, пока читалась история), onDispose
     * не сработает, и подписчик остался бы в хабе со своей очередью. release
     * можно звать сколько угодно раз.
     */
    Stream open(String topic, String policy, int limit) {
        Subscriber s = new Subscriber(policy, limit);
        subs.computeIfAbsent(topic, k -> ConcurrentHashMap.newKeySet()).add(s);
        conns.incrementAndGet();
        Flux<Event> flux = Flux.<Event>create(sink -> {
            synchronized (s) {
                s.sink = sink;
                if (s.closed) sink.complete();
            }
            sink.onRequest(s::request);
            sink.onDispose(() -> unsubscribe(topic, s));
        }, FluxSink.OverflowStrategy.ERROR);
        return new Stream(flux, () -> unsubscribe(topic, s));
    }

    private void unsubscribe(String topic, Subscriber s) {
        Set<Subscriber> set = subs.get(topic);
        if (set != null && set.remove(s)) conns.decrementAndGet();
        synchronized (s) {
            s.closed = true;
            maxQueue.accumulateAndGet(s.maxLen, Math::max);
            dropped.addAndGet(s.droppedHere);
            s.droppedHere = 0;
        }
    }

    void deliver(Event e) {
        List<Event> r = recent.computeIfAbsent(e.topic(), k -> new ArrayList<>());
        synchronized (r) {
            r.add(e);
            if (r.size() > RECENT_KEEP) r.subList(0, r.size() - RECENT_KEEP).clear();
        }
        Sinks.Empty<Void> w = waiters.remove(e.topic());
        if (w != null) w.tryEmitEmpty();
        delivered.incrementAndGet();
        Set<Subscriber> set = subs.get(e.topic());
        if (set != null) for (Subscriber s : set) s.push(e);
    }

    List<Event> after(String topic, long id) {
        List<Event> r = recent.get(topic);
        List<Event> out = new ArrayList<>();
        if (r == null) return out;
        synchronized (r) {
            for (Event e : r) if (e.id() > id) out.add(e);
        }
        return out;
    }

    /** Сигнал «в теме появилось событие» — для long polling. */
    Mono<Void> changed(String topic) {
        return waiters.computeIfAbsent(topic, k -> Sinks.empty()).asMono();
    }

    long latest(String topic) {
        List<Event> r = recent.get(topic);
        if (r == null) return 0;
        synchronized (r) {
            return r.isEmpty() ? 0 : r.get(r.size() - 1).id();
        }
    }

    long[] liveMaxQueueAndDropped() {
        long max = 0, drop = 0;
        for (Set<Subscriber> set : subs.values())
            for (Subscriber s : set) {
                max = Math.max(max, s.maxLen());
                drop += s.dropped();
            }
        return new long[] {max, drop};
    }
}
