package tech.khorost.kafka.consumergroups;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.RangeAssignor;
import org.apache.kafka.common.serialization.StringDeserializer;

import java.time.Duration;
import java.util.List;
import java.util.Properties;

/**
 * Базовый сценарий: consumer-1 в одиночку получает все партиции; подключаем
 * consumer-2, затем consumer-3 — ребаланс, перераспределение; отключаем
 * consumer-3, затем consumer-2 — обратный ребаланс. Стратегия зафиксирована
 * на RangeAssignor (классический eager-протокол) — самый простой случай для
 * первого знакомства с revoke/assign. Сравнение стратегий, включая
 * cooperative-sticky, — сценарий StrategiesDemo.
 */
public final class RebalanceDemo {
    private static final String GROUP_ID = "cg-rebalance-demo";

    private RebalanceDemo() {
    }

    public static void run() throws InterruptedException {
        Member c1 = newMember("consumer-1");
        c1.waitFirstAssign(Duration.ofSeconds(15));
        Log.f("--- consumer-1 один в группе: %s ---", Member.partsToString(c1.snapshot()));
        Assertions.assertPartitioned("consumer-1 один", c1);

        // Продюсить начинаем ТОЛЬКО теперь (после стабильного назначения) и
        // продолжаем непрерывно весь сценарий — см. content-note в README.
        ContinuousProducer producer = new ContinuousProducer(150);

        Member c2 = newMember("consumer-2");
        c2.waitFirstAssign(Duration.ofSeconds(20));
        Assertions.waitUntilStable(Duration.ofSeconds(15), c1, c2);
        Log.f("--- после подключения consumer-2: c1=%s c2=%s ---",
                Member.partsToString(c1.snapshot()), Member.partsToString(c2.snapshot()));
        Assertions.assertPartitioned("consumer-1+2", c1, c2);

        Member c3 = newMember("consumer-3");
        c3.waitFirstAssign(Duration.ofSeconds(20));
        Assertions.waitUntilStable(Duration.ofSeconds(15), c1, c2, c3);
        Log.f("--- после подключения consumer-3: c1=%s c2=%s c3=%s ---",
                Member.partsToString(c1.snapshot()), Member.partsToString(c2.snapshot()), Member.partsToString(c3.snapshot()));
        Assertions.assertPartitioned("consumer-1+2+3", c1, c2, c3);

        Log.f("--- отключаем consumer-3 ---");
        c3.close();
        Assertions.waitUntilStable(Duration.ofSeconds(15), c1, c2);
        Log.f("--- после отключения consumer-3: c1=%s c2=%s ---",
                Member.partsToString(c1.snapshot()), Member.partsToString(c2.snapshot()));
        Assertions.assertPartitioned("consumer-1+2 (после ухода 3)", c1, c2);

        Log.f("--- отключаем consumer-2 ---");
        c2.close();
        Assertions.waitUntilStable(Duration.ofSeconds(15), c1);
        Log.f("--- после отключения consumer-2: c1=%s ---", Member.partsToString(c1.snapshot()));
        Assertions.assertPartitioned("consumer-1 (после ухода 2 и 3)", c1);

        producer.stop();
        c1.close();
        Log.f("[producer] за сценарий rebalance отправлено: %d, суммарно получено (c1+c2+c3): %d",
                producer.sent.get(), c1.consumed.get() + c2.consumed.get() + c3.consumed.get());
    }

    private static Member newMember(String id) {
        Properties props = new Properties();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, GROUP_ID);
        props.put(ConsumerConfig.CLIENT_ID_CONFIG, id);
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "latest");
        props.put(ConsumerConfig.PARTITION_ASSIGNMENT_STRATEGY_CONFIG, List.of(RangeAssignor.class.getName()));
        return new Member(id, props, null);
    }
}
