package tech.khorost.kafka.consumergroups;

import org.apache.kafka.common.TopicPartition;

import java.util.HashMap;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;

public final class Assertions {
    private Assertions() {
    }

    /**
     * Ждёт, пока объединение назначений members не покроет все партиции без
     * пересечений, или до истечения timeout. Кооперативный ребаланс
     * (CooperativeStickyAssignor) может сходиться НЕСКОЛЬКИМИ раундами
     * join/sync (часть партиций временно "повисает" ничьей, пока прежний
     * владелец не отзовёт их явно) — фиксированная пауза перед
     * assertPartitioned ненадёжна, реально ждём сходимости.
     */
    public static boolean waitUntilStable(java.time.Duration timeout, Member... members) {
        long deadline = System.currentTimeMillis() + timeout.toMillis();
        while (System.currentTimeMillis() < deadline) {
            Map<Integer, String> seen = new HashMap<>();
            boolean overlap = false;
            for (Member m : members) {
                for (TopicPartition tp : m.snapshot()) {
                    int p = tp.partition();
                    if (seen.containsKey(p)) {
                        overlap = true;
                    }
                    seen.put(p, m.id);
                }
            }
            if (!overlap && seen.size() == Kafka.PARTITIONS) {
                return true;
            }
            Kafka.sleep(200);
        }
        return false;
    }

    /** Падает (RuntimeException), если партиции пересекаются между членами или покрыты не полностью. */
    public static void assertPartitioned(String label, Member... members) {
        Map<Integer, String> seen = new HashMap<>();
        Set<Integer> total = new TreeSet<>();
        for (Member m : members) {
            for (TopicPartition tp : m.snapshot()) {
                int p = tp.partition();
                if (seen.containsKey(p)) {
                    throw new IllegalStateException(String.format(
                            "[assert] FAIL (%s): партиция %d одновременно у %s и %s", label, p, seen.get(p), m.id));
                }
                seen.put(p, m.id);
                total.add(p);
            }
        }
        if (total.size() != Kafka.PARTITIONS) {
            throw new IllegalStateException(String.format(
                    "[assert] FAIL (%s): покрыто %d партиций из %d: %s", label, total.size(), Kafka.PARTITIONS, total));
        }
        Log.f("[assert] OK (%s): все %d партиций покрыты ровно одним консьюмером, пересечений нет", label, Kafka.PARTITIONS);
    }

    public static boolean equalSets(Set<TopicPartition> a, Set<TopicPartition> b) {
        if (a.size() != b.size()) {
            return false;
        }
        Set<Integer> pa = new TreeSet<>();
        for (TopicPartition t : a) {
            pa.add(t.partition());
        }
        Set<Integer> pb = new TreeSet<>();
        for (TopicPartition t : b) {
            pb.add(t.partition());
        }
        return pa.equals(pb);
    }
}
