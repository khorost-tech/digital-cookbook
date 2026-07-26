package tech.khorost.kafka.consumergroups;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.common.TopicPartition;
import org.apache.kafka.common.serialization.StringDeserializer;

import java.time.Duration;
import java.util.Properties;
import java.util.Set;

/**
 * Static membership (group.instance.id): рестарт статического члена В
 * ПРЕДЕЛАХ session.timeout НЕ триггерит ребаланс всей группы и возвращает
 * те же партиции (KIP-345 — при graceful close() с заданным
 * group.instance.id KafkaConsumer НЕ шлёт LeaveGroupRequest). Для контраста
 * — та же процедура БЕЗ instance-id: триггерит ребаланс немедленно.
 */
public final class StaticMembershipDemo {
    private static final String GROUP_ID = "cg-static-demo";
    private static final int SESSION_TIMEOUT_MS = 12_000;

    private StaticMembershipDemo() {
    }

    public static void run() throws InterruptedException {
        ContinuousProducer producer = new ContinuousProducer(150);

        // --- Часть A: статические члены ---
        Member a = newMember("static-a", GROUP_ID, "static-a");
        Member b = newMember("static-b", GROUP_ID, "static-b");
        a.waitFirstAssign(Duration.ofSeconds(20));
        b.waitFirstAssign(Duration.ofSeconds(20));
        Assertions.waitUntilStable(Duration.ofSeconds(15), a, b);

        Set<TopicPartition> beforeA = a.snapshot();
        Set<TopicPartition> beforeB = b.snapshot();
        int[] bBefore = b.eventCounts();
        Log.f("[static] начально: static-a=%s static-b=%s", Member.partsToString(beforeA), Member.partsToString(beforeB));
        Assertions.assertPartitioned("static: до рестарта", a, b);

        Log.f("[static] graceful close static-a (group.instance.id сохранён) — по KIP-345 LeaveGroupRequest НЕ отправляется");
        long closeStart = System.nanoTime();
        a.close();

        Member a2 = newMember("static-a-restarted", GROUP_ID, "static-a");
        a2.waitFirstAssign(Duration.ofSeconds(20));
        long restartGapMs = (System.nanoTime() - closeStart) / 1_000_000;
        Assertions.waitUntilStable(Duration.ofSeconds(15), a2, b);

        Set<TopicPartition> afterA2 = a2.snapshot();
        Set<TopicPartition> afterB = b.snapshot();
        int[] bAfter = b.eventCounts();

        Log.f("[static] рестарт static-a занял %dms (< session.timeout=%dms)", restartGapMs, SESSION_TIMEOUT_MS);
        Log.f("[static] после рестарта: static-a-restarted=%s (было у static-a: %s) static-b=%s",
                Member.partsToString(afterA2), Member.partsToString(beforeA), Member.partsToString(afterB));

        if (!Assertions.equalSets(beforeA, afterA2)) {
            throw new IllegalStateException(String.format(
                    "[assert] FAIL (static): рестартовавший статический член получил другие партиции: было %s, стало %s",
                    Member.partsToString(beforeA), Member.partsToString(afterA2)));
        }
        Log.f("[assert] OK (static): static-a-restarted вернул СЕБЕ ровно те же партиции: %s", Member.partsToString(afterA2));

        if (bAfter[0] != bBefore[0] || bAfter[1] != bBefore[1]) {
            throw new IllegalStateException(String.format(
                    "[assert] FAIL (static): static-b пережил лишний revoke/assign во время рестарта static-a (assign %d->%d, revoke %d->%d)",
                    bBefore[0], bAfter[0], bBefore[1], bAfter[1]));
        }
        Log.f("[assert] OK (static): static-b НЕ получил ни одного revoke/assign во время рестарта static-a (группа не ребалансировалась)");

        a2.close();
        b.close();

        // --- Часть B: контраст — динамические (обычные) члены, та же процедура ---
        Log.f("[static] контраст: та же процедура, но БЕЗ group.instance.id (динамическое членство)");
        String dynGroup = GROUP_ID + "-dyn";
        Member dc = newMember("dynamic-c", dynGroup, null);
        Member dd = newMember("dynamic-d", dynGroup, null);
        dc.waitFirstAssign(Duration.ofSeconds(20));
        dd.waitFirstAssign(Duration.ofSeconds(20));
        Assertions.waitUntilStable(Duration.ofSeconds(15), dc, dd);

        int[] ddBefore = dd.eventCounts();
        Log.f("[dynamic] начально: dynamic-c=%s dynamic-d=%s", Member.partsToString(dc.snapshot()), Member.partsToString(dd.snapshot()));

        dc.close(); // без group.instance.id Close() ШЛЁТ LeaveGroupRequest -> немедленный ребаланс у dynamic-d
        Member dc2 = newMember("dynamic-c-restarted", dynGroup, null);
        dc2.waitFirstAssign(Duration.ofSeconds(20));
        Assertions.waitUntilStable(Duration.ofSeconds(15), dc2, dd);

        int[] ddAfter = dd.eventCounts();
        Log.f("[dynamic] после рестарта dynamic-c: dynamic-c-restarted=%s dynamic-d=%s",
                Member.partsToString(dc2.snapshot()), Member.partsToString(dd.snapshot()));

        if (ddAfter[0] == ddBefore[0] && ddAfter[1] == ddBefore[1]) {
            throw new IllegalStateException(
                    "[assert] FAIL (dynamic-контраст): ожидался ребаланс у dynamic-d при рестарте dynamic-c, но событий не было");
        }
        Log.f("[assert] OK (dynamic-контраст): dynamic-d получил revoke/assign при рестарте dynamic-c (assign %d->%d, revoke %d->%d) — в отличие от static-b выше",
                ddBefore[0], ddAfter[0], ddBefore[1], ddAfter[1]);

        dc2.close();
        dd.close();
        producer.stop();

        System.out.println("[static] сценарий завершён");
    }

    private static Member newMember(String id, String groupId, String instanceId) {
        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, groupId);
        props.put(ConsumerConfig.CLIENT_ID_CONFIG, id);
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "latest");
        props.put(ConsumerConfig.SESSION_TIMEOUT_MS_CONFIG, SESSION_TIMEOUT_MS);
        if (instanceId != null) {
            props.put(ConsumerConfig.GROUP_INSTANCE_ID_CONFIG, instanceId);
        }
        return new Member(id, props, null);
    }
}
