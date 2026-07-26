package tech.khorost.kafka.consumergroups;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.CooperativeStickyAssignor;
import org.apache.kafka.clients.consumer.RangeAssignor;
import org.apache.kafka.clients.consumer.RoundRobinAssignor;
import org.apache.kafka.clients.consumer.StickyAssignor;
import org.apache.kafka.common.serialization.StringDeserializer;

import java.time.Duration;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;

/**
 * Сравнивает 4 стратегии назначения партиций (kafka-clients: RangeAssignor,
 * RoundRobinAssignor, StickyAssignor — eager-протокол; CooperativeStickyAssignor
 * — incremental cooperative). Один и тот же сценарий на все 4: 3 консьюмера
 * стабилизируют назначение, затем подключается четвёртый — смотрим, что
 * именно каждая стратегия отзывает у уже работающих членов. Eager обязан
 * отозвать ВСЁ текущее назначение перед повторным join (stop-the-world);
 * cooperative-sticky отзывает только реально переезжающие партиции.
 */
public final class StrategiesDemo {
    private record Spec(String name, String assignorClass) {
    }

    private static final List<Spec> SPECS = List.of(
            new Spec("range", RangeAssignor.class.getName()),
            new Spec("roundrobin", RoundRobinAssignor.class.getName()),
            new Spec("sticky", StickyAssignor.class.getName()),
            new Spec("cooperative-sticky", CooperativeStickyAssignor.class.getName())
    );

    private StrategiesDemo() {
    }

    public static void run() throws InterruptedException {
        for (Spec spec : SPECS) {
            System.out.printf("%n--- стратегия: %s ---%n", spec.name());
            runStrategy(spec);
        }
    }

    private static void runStrategy(Spec spec) throws InterruptedException {
        String groupId = "cg-strategy-" + spec.name();

        Member a = newMember("consumer-a", groupId, spec.assignorClass());
        Member b = newMember("consumer-b", groupId, spec.assignorClass());
        Member c = newMember("consumer-c", groupId, spec.assignorClass());
        Member[] initial = {a, b, c};
        for (Member m : initial) {
            m.waitFirstAssign(Duration.ofSeconds(20));
        }
        // Кооперативный ребаланс может сходиться несколькими раундами —
        // реально ждём сходимости, а не гадаем с фиксированной паузой.
        Assertions.waitUntilStable(Duration.ofSeconds(15), initial);
        Log.f("[%s] начальное распределение (3 консьюмера): a=%s b=%s c=%s", spec.name(),
                Member.partsToString(a.snapshot()), Member.partsToString(b.snapshot()), Member.partsToString(c.snapshot()));
        Assertions.assertPartitioned(spec.name() + ": 3 консьюмера", a, b, c);

        ContinuousProducer producer = new ContinuousProducer(150);

        Map<String, List<Member.RevokeEvent>> beforeRevokes = new HashMap<>();
        for (Member m : initial) {
            beforeRevokes.put(m.id, m.revokeHistory());
        }

        Log.f("[%s] подключаем consumer-d -> ребаланс", spec.name());
        Member d = newMember("consumer-d", groupId, spec.assignorClass());
        d.waitFirstAssign(Duration.ofSeconds(20));
        Member[] all = {a, b, c, d};
        Assertions.waitUntilStable(Duration.ofSeconds(15), all);
        Log.f("[%s] после подключения consumer-d: a=%s b=%s c=%s d=%s", spec.name(),
                Member.partsToString(a.snapshot()), Member.partsToString(b.snapshot()),
                Member.partsToString(c.snapshot()), Member.partsToString(d.snapshot()));
        Assertions.assertPartitioned(spec.name() + ": 4 консьюмера", all);

        boolean isCooperative = spec.assignorClass().equals(CooperativeStickyAssignor.class.getName());
        for (Member m : initial) {
            List<Member.RevokeEvent> after = m.revokeHistory();
            List<Member.RevokeEvent> before = beforeRevokes.get(m.id);
            if (after.size() == before.size()) {
                Log.f("[%s] %s: НИ ОДНОГО revoke-события при вступлении consumer-d (партиции не тронуты вовсе)", spec.name(), m.id);
                continue;
            }
            boolean hasFull = false;
            for (int i = before.size(); i < after.size(); i++) {
                Member.RevokeEvent ev = after.get(i);
                if (ev.partitions().isEmpty()) {
                    Log.f("[%s] %s revoked при вступлении consumer-d: %s — ПУСТО (инкрементально, партиции не переезжали)",
                            spec.name(), m.id, Member.partsToString(ev.partitions()));
                } else if (ev.full()) {
                    Log.f("[%s] %s revoked при вступлении consumer-d: %s — ВСЁ текущее назначение (stop-the-world, eager)",
                            spec.name(), m.id, Member.partsToString(ev.partitions()));
                    hasFull = true;
                } else {
                    Log.f("[%s] %s revoked при вступлении consumer-d: %s — ЧАСТЬ назначения (инкрементально, часть партиций сохранена — cooperative)",
                            spec.name(), m.id, Member.partsToString(ev.partitions()));
                }
            }
            // Ассерт — не просто лог: разница eager/cooperative обязана
            // проявляться механически, иначе регресс (не тот assignor, смена
            // поведения клиента) пройдёт незамеченным.
            if (isCooperative && hasFull) {
                throw new IllegalStateException(String.format(
                        "[assert] FAIL (%s): %s — cooperative-sticky отозвал ПОЛНОЕ текущее назначение (full revoke), ожидался только пустой/частичный revoke",
                        spec.name(), m.id));
            }
            if (!isCooperative && !hasFull) {
                throw new IllegalStateException(String.format(
                        "[assert] FAIL (%s): %s — eager-стратегия НЕ отозвала полное текущее назначение (full revoke) при вступлении consumer-d, ожидался stop-the-world",
                        spec.name(), m.id));
            }
        }
        if (isCooperative) {
            Log.f("[assert] OK (%s): ни одного full-revoke — incremental cooperative подтверждён", spec.name());
        } else {
            Log.f("[assert] OK (%s): у каждого прежнего члена был full-revoke — stop-the-world (eager) подтверждён", spec.name());
        }

        producer.stop();
        for (Member m : all) {
            m.close();
        }
    }

    private static Member newMember(String id, String groupId, String assignorClass) {
        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, groupId);
        props.put(ConsumerConfig.CLIENT_ID_CONFIG, id);
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "latest");
        props.put(ConsumerConfig.PARTITION_ASSIGNMENT_STRATEGY_CONFIG, List.of(assignorClass));
        return new Member(id, props, null);
    }
}
