package tech.khorost.kafka.consumergroups;

import org.apache.kafka.clients.consumer.ConsumerRebalanceListener;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.common.TopicPartition;
import org.apache.kafka.common.errors.WakeupException;

import java.time.Duration;
import java.util.ArrayList;
import java.util.Collection;
import java.util.List;
import java.util.Properties;
import java.util.Set;
import java.util.TreeSet;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Обёртка над KafkaConsumer — один участник consumer group. Аналог member
 * в Go-версии стенда (../../go/consumer-groups/common.go): свой поток с
 * циклом poll(), ConsumerRebalanceListener логирует и накапливает историю
 * assign/revoke/lost событий, доступную другим потокам через synchronized-методы.
 */
public final class Member {
    public final String id;
    public final AtomicLong consumed = new AtomicLong();

    private final KafkaConsumer<String, String> consumer;
    private final Thread thread;
    private volatile boolean stop = false;
    private volatile boolean closed = false;

    private final Object lock = new Object();
    private final Set<TopicPartition> assigned = new TreeSet<>(Member::cmp);
    private final List<Set<TopicPartition>> assignEvents = new ArrayList<>();
    private final List<RevokeEvent> revokeEvents = new ArrayList<>();
    private final List<Set<TopicPartition>> lostEvents = new ArrayList<>();

    private final CountDownLatch firstAssign = new CountDownLatch(1);

    @FunctionalInterface
    public interface Processor {
        void process(Member m, ConsumerRecords<String, String> records);
    }

    /**
     * Одно REVOKED-событие вместе с классификацией: full==true значит у
     * члена отозвали ВСЮ его текущую партицию (stop-the-world, eager);
     * full==false при непустом partitions значит отозвали ЧАСТЬ
     * (инкрементально, cooperative). Отличать по одному лишь "пусто/не
     * пусто" НЕЛЬЗЯ — cooperative-sticky может отозвать непустое, но не
     * полное множество (проверено живьём, см. README).
     */
    public record RevokeEvent(Set<TopicPartition> partitions, boolean full) {
    }

    private static int cmp(TopicPartition a, TopicPartition b) {
        return Integer.compare(a.partition(), b.partition());
    }

    public Member(String id, Properties props, Processor processor) {
        this.id = id;
        this.consumer = new KafkaConsumer<>(props);
        Processor p = processor != null ? processor : Member::defaultProcess;

        consumer.subscribe(List.of(Kafka.TOPIC), new ConsumerRebalanceListener() {
            @Override
            public void onPartitionsAssigned(Collection<TopicPartition> partitions) {
                Set<TopicPartition> ps = new TreeSet<>(Member::cmp);
                ps.addAll(partitions);
                String cur;
                synchronized (lock) {
                    assigned.addAll(ps);
                    assignEvents.add(ps);
                    cur = partsToString(assigned);
                }
                Log.f("%-22s ASSIGNED %-14s текущее назначение: %s", id, partsToString(ps), cur);
                firstAssign.countDown();
            }

            @Override
            public void onPartitionsRevoked(Collection<TopicPartition> partitions) {
                Set<TopicPartition> ps = new TreeSet<>(Member::cmp);
                ps.addAll(partitions);
                String cur;
                synchronized (lock) {
                    int priorSize = assigned.size();
                    assigned.removeAll(ps);
                    boolean full = priorSize > 0 && ps.size() == priorSize;
                    revokeEvents.add(new RevokeEvent(ps, full));
                    cur = partsToString(assigned);
                }
                if (ps.isEmpty()) {
                    Log.f("%-22s REVOKED  %-14s (пусто — инкрементальная ребалансировка ничего не забрала) текущее: %s", id, partsToString(ps), cur);
                } else {
                    Log.f("%-22s REVOKED  %-14s текущее назначение: %s", id, partsToString(ps), cur);
                }
            }

            @Override
            public void onPartitionsLost(Collection<TopicPartition> partitions) {
                Set<TopicPartition> ps = new TreeSet<>(Member::cmp);
                ps.addAll(partitions);
                synchronized (lock) {
                    assigned.removeAll(ps);
                    lostEvents.add(ps);
                }
                Log.f("%-22s LOST     %s (сессия истекла до ре-джойна)", id, partsToString(ps));
            }
        });

        this.thread = new Thread(() -> runLoop(p), id + "-thread");
        this.thread.setDaemon(true);
        this.thread.start();
    }

    private void runLoop(Processor p) {
        try {
            while (!stop) {
                ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(300));
                p.process(this, records);
            }
        } catch (WakeupException e) {
            // ожидаемо при close()
        } finally {
            consumer.close();
        }
    }

    private static void defaultProcess(Member m, ConsumerRecords<String, String> records) {
        records.forEach(r -> m.consumed.incrementAndGet());
    }

    public KafkaConsumer<String, String> rawConsumer() {
        return consumer;
    }

    public void waitFirstAssign(Duration timeout) throws InterruptedException {
        if (!firstAssign.await(timeout.toMillis(), TimeUnit.MILLISECONDS)) {
            throw new IllegalStateException(id + ": не получил начальное назначение партиций за " + timeout);
        }
    }

    public Set<TopicPartition> snapshot() {
        synchronized (lock) {
            // ВАЖНО: не new TreeSet<>(assigned) — при статическом типе Set<TopicPartition>
            // резолвится конструктор Collection (натуральный порядок), а TopicPartition
            // не Comparable -> ClassCastException. Копируем явно с тем же компаратором.
            Set<TopicPartition> copy = new TreeSet<>(Member::cmp);
            copy.addAll(assigned);
            return copy;
        }
    }

    /** [assignEvents, revokeEvents, lostEvents] */
    public int[] eventCounts() {
        synchronized (lock) {
            return new int[]{assignEvents.size(), revokeEvents.size(), lostEvents.size()};
        }
    }

    public List<RevokeEvent> revokeHistory() {
        synchronized (lock) {
            return new ArrayList<>(revokeEvents);
        }
    }

    /** graceful close: для динамических членов шлёт LeaveGroupRequest, для static (group.instance.id) — нет (KIP-345). */
    public void close() {
        if (closed) {
            return;
        }
        closed = true;
        stop = true;
        consumer.wakeup();
        try {
            thread.join(15_000);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    public static String partsToString(Collection<TopicPartition> ps) {
        List<Integer> xs = new ArrayList<>();
        for (TopicPartition p : ps) {
            xs.add(p.partition());
        }
        xs.sort(Integer::compareTo);
        StringBuilder sb = new StringBuilder("[");
        for (int i = 0; i < xs.size(); i++) {
            if (i > 0) {
                sb.append(" ");
            }
            sb.append(xs.get(i));
        }
        return sb.append("]").toString();
    }
}
