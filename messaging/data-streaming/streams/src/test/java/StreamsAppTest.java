import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.apache.kafka.common.serialization.Serdes;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;
import org.apache.kafka.streams.StreamsConfig;
import org.apache.kafka.streams.TestInputTopic;
import org.apache.kafka.streams.TestOutputTopic;
import org.apache.kafka.streams.TopologyTestDriver;
import org.apache.kafka.streams.state.KeyValueStore;
import org.apache.kafka.streams.test.TestRecord;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * TopologyTestDriver прогоняет РЕАЛЬНУЮ топологию StreamsApp.buildTopology()
 * (repartition + Processor API + state store) без живого кластера Kafka —
 * без mock'ов внутренней логики: тот же код, что и в main(), см. комментарий
 * у buildTopology().
 */
class StreamsAppTest {
    private TopologyTestDriver driver;
    private TestInputTopic<String, String> in;
    private TestOutputTopic<String, String> out;
    private final ObjectMapper mapper = new ObjectMapper();

    @BeforeEach
    void setUp() {
        Properties props = new Properties();
        props.put(StreamsConfig.APPLICATION_ID_CONFIG, "ds-streams-test");
        props.put(StreamsConfig.BOOTSTRAP_SERVERS_CONFIG, "dummy:0000");
        props.put(StreamsConfig.DEFAULT_KEY_SERDE_CLASS_CONFIG, Serdes.String().getClass());
        props.put(StreamsConfig.DEFAULT_VALUE_SERDE_CLASS_CONFIG, Serdes.String().getClass());
        props.put(StreamsConfig.STATE_DIR_CONFIG, "target/kafka-streams-test-" + System.nanoTime());

        driver = new TopologyTestDriver(StreamsApp.buildTopology(), props);
        in = driver.createInputTopic(StreamsApp.IN, new StringSerializer(), new StringSerializer());
        out = driver.createOutputTopic(StreamsApp.OUT, new StringDeserializer(), new StringDeserializer());
    }

    @AfterEach
    void tearDown() {
        driver.close();
    }

    /**
     * (1) Корректность агрегации: несколько заказов разных клиентов на вход
     * orders.events — на выходе customer.totals ПОСЛЕДНИЙ снимок каждого
     * клиента должен быть суммой ЕГО заказов (заказы customer_id=1 и
     * customer_id=2 сходятся в СВОИ, а не в общие, накопления) и state store
     * (накопление, а не последний заказ побеждает). Смягчённая формулировка
     * (было — "косвенно проверяет repartition"): TopologyTestDriver прогоняет
     * топологию БЕЗ живого кластера Kafka — в частности, БЕЗ нескольких
     * физических stream-task'ов и их распределения по партициям. Он проверяет
     * КОРРЕКТНОСТЬ АГРЕГАЦИИ (что repartition-шаг ключует по customer_id и
     * downstream видит правильную сумму на этом ключе), а не само
     * co-partitioning при нескольких партициях. Полноценное подтверждение
     * co-partitioning даёт интеграционный прогон на живом стенде с несколькими
     * партициями orders.events (см. FIXTURES.md §1/§2 — стенд поднят с 3
     * партициями).
     */
    @Test
    void aggregatesOrdersPerCustomer() throws Exception {
        in.pipeInput("order-1", "{\"customer_id\":1,\"amount\":10.0}");
        in.pipeInput("order-2", "{\"customer_id\":2,\"amount\":20.0}");
        in.pipeInput("order-3", "{\"customer_id\":1,\"amount\":5.0}");
        in.pipeInput("order-4", "{\"customer_id\":2,\"amount\":2.5}");
        in.pipeInput("order-5", "{\"customer_id\":1,\"amount\":1.0}");

        List<TestRecord<String, String>> records = out.readRecordsToList();
        // Каждое обновление состояния клиента — отдельная запись в customer.totals
        // (changelog снимков, см. FIXTURES.md §2); последняя запись на ключ — это
        // актуальное состояние клиента, ровно как читает Go-консьюмер через
        // ReplacingMergeTree(version)/FINAL в ClickHouse (см. sql/02-clickhouse.sql).
        Map<String, String> lastByKey = new LinkedHashMap<>();
        for (TestRecord<String, String> r : records) {
            lastByKey.put(r.key(), r.value());
        }

        JsonNode customer1 = mapper.readTree(lastByKey.get("1"));
        assertEquals(3, customer1.get("orders").asInt(), "customer_id=1: 3 заказа (10.0+5.0+1.0)");
        assertEquals(16.0, customer1.get("total").asDouble(), 0.001);

        JsonNode customer2 = mapper.readTree(lastByKey.get("2"));
        assertEquals(2, customer2.get("orders").asInt(), "customer_id=2: 2 заказа (20.0+2.5)");
        assertEquals(22.5, customer2.get("total").asDouble(), 0.001);
    }

    /**
     * Ищет в цепочке причин (getCause()) исключение нужного типа и возвращает
     * его. Обход цепочки нужен именно в ТЕСТЕ: Kafka Streams оборачивает
     * исключение, брошенное из Processor.process(), в StreamsException, поэтому
     * верхний throwable из pipeInput() — не то, что бросил наш код. В
     * production-обработчике разбора типов нет: там политика безусловная
     * (см. setUncaughtExceptionHandler в StreamsApp.main()).
     */
    private static <T extends Throwable> T causeOfType(Throwable thrown, Class<T> type) {
        for (Throwable c = thrown; c != null; c = c.getCause()) {
            if (type.isInstance(c)) {
                return type.cast(c);
            }
        }
        throw new AssertionError("в цепочке причин нет " + type.getSimpleName() + ": " + thrown, thrown);
    }

    /**
     * (2) Fail-fast сквозь РЕАЛЬНЫЙ конвейер: повреждённая запись стора (см.
     * TotalsProcessor.process()) должна довести до pipeInput исключение,
     * в цепочке причин которого лежит именно CorruptedStateStoreException — с
     * сообщением, называющим клиента и характер повреждения. Проверяется тип и
     * сообщение, а не ответ обработчика: ответ безусловен (SHUTDOWN_CLIENT для
     * чего угодно), поэтому его проверка ничего бы не доказывала.
     *
     * Стор портится НАПРЯМУЮ через driver.getKeyValueStore(...), в обход
     * обычного process()-пути, — это и есть модель повреждения, для которой
     * писался fail-fast (реальный сценарий в проде: стор пережил деплой, а не
     * прошёл через process() этой версии кода; в самом стенде путь недостижим).
     */
    @Test
    void corruptedStoreRecordTriggersFailFast() {
        KeyValueStore<String, String> store = driver.getKeyValueStore(StreamsApp.STORE);
        store.put("3", "{\"orders\":\"not-a-number\",\"total\":10}");

        Throwable thrown = assertThrows(Throwable.class,
                () -> in.pipeInput("order-corrupt", "{\"customer_id\":3,\"amount\":1.0}"));

        StreamsApp.CorruptedStateStoreException cause =
                causeOfType(thrown, StreamsApp.CorruptedStateStoreException.class);
        assertTrue(cause.getMessage().contains("customer=3"),
                "сообщение должно называть клиента: " + cause.getMessage());
    }

    /**
     * (3) Non-finite amount отбрасывается на разборе. Double.parseDouble
     * принимает "NaN"/"Infinity"/"-Infinity" как ВАЛИДНЫЕ литералы — без явной
     * проверки isFinite такое значение прошло бы в агрегат и ТИХО исказило его
     * на округлении (Math.round(NaN)==0 → 0.0; Math.round(Inf)/100 →
     * 9.223372036854776E16). Проверяем чистую функцию разбора напрямую.
     */
    @Test
    void parseEventRejectsNonFiniteAmount() {
        assertNull(StreamsApp.parseEvent("{\"customer_id\":1,\"amount\":\"NaN\"}"), "NaN должен отбрасываться");
        assertNull(StreamsApp.parseEvent("{\"customer_id\":1,\"amount\":\"Infinity\"}"), "Infinity должен отбрасываться");
        assertNull(StreamsApp.parseEvent("{\"customer_id\":1,\"amount\":\"-Infinity\"}"), "-Infinity должен отбрасываться");

        StreamsApp.ParsedEvent ok = StreamsApp.parseEvent("{\"customer_id\":1,\"amount\":\"10.5\"}");
        assertEquals(1L, ok.customerId());
        assertEquals(10.5, ok.amount());
    }

    /**
     * (4) Тот же вход через РЕАЛЬНУЮ топологию: событие с amount="NaN" не должно
     * породить ни одной выходной записи (отбрасывается как битое), а не
     * обнулить агрегат клиента.
     */
    @Test
    void nonFiniteAmountEventIsDroppedNotAggregated() {
        in.pipeInput("order-nan", "{\"customer_id\":1,\"amount\":\"NaN\"}");
        assertEquals(List.of(), out.readRecordsToList(), "битое событие не должно давать выход");
    }

    /**
     * (5) Переполнение суммы: слагаемые конечны по отдельности, но накопленный
     * total выходит за пределы double. Писать ±Infinity в агрегат нельзя —
     * Math.round(Inf)/100 дал бы 9.223372036854776E16, то есть тихо искажённое
     * число вместо отказа. Проверяем, что обработка останавливается громко:
     * в цепочке причин лежит IllegalStateException с внятным сообщением.
     */
    @Test
    void totalOverflowTriggersFailFast() {
        KeyValueStore<String, String> store = driver.getKeyValueStore(StreamsApp.STORE);
        store.put("4", "{\"orders\":1,\"total\":1.7976931348623157E308}");

        Throwable thrown = assertThrows(Throwable.class,
                () -> in.pipeInput("order-overflow", "{\"customer_id\":4,\"amount\":1.7976931348623157E308}"));

        IllegalStateException cause = causeOfType(thrown, IllegalStateException.class);
        assertTrue(cause.getMessage().contains("непредставимый агрегат"),
                "сообщение должно называть причину — непредставимую сумму: " + cause.getMessage());
    }

    /**
     * (6) customer_id вне диапазона long НЕ должен «сползать» в реального клиента.
     * isIntegralNumber() истинен и для BigInteger, а asLong() молча усекает:
     * 18446744073709551617 (2^64+1) даёт asLong()==1. Без canConvertToLong()
     * событие чужого идентификатора приписалось бы клиенту 1 и тихо исказило его
     * сумму — смешивание клиентов, худший вид порчи (число выглядит правдоподобно).
     */
    @Test
    void parseEventRejectsCustomerIdOutsideLongRange() {
        assertNull(StreamsApp.parseEvent("{\"customer_id\":18446744073709551617,\"amount\":10.0}"),
                "customer_id вне диапазона long должен отбрасываться, а не усекаться до 1");
        // Контроль: тот же идентификатор строкой — Long.parseLong бросит
        // NumberFormatException, событие тоже отбрасывается (штатно, не исключением наружу).
        assertNull(StreamsApp.parseEvent("{\"customer_id\":\"18446744073709551617\",\"amount\":10.0}"),
                "тот же идентификатор строкой должен отбрасываться");
    }

    /**
     * (7) Переполнение счётчика заказов в сторе: orders=Long.MAX_VALUE, обычное
     * "+1" молча дало бы Long.MIN_VALUE и записало ОТРИЦАТЕЛЬНОЕ число заказов в
     * changelog. Math.addExact ловит это, и обработка останавливается громко.
     */
    @Test
    void ordersCounterOverflowTriggersFailFast() {
        KeyValueStore<String, String> store = driver.getKeyValueStore(StreamsApp.STORE);
        store.put("2", "{\"orders\":" + Long.MAX_VALUE + ",\"total\":10.0}");

        Throwable thrown = assertThrows(Throwable.class,
                () -> in.pipeInput("order-overflow-orders", "{\"customer_id\":2,\"amount\":1.0}"));

        StreamsApp.CorruptedStateStoreException cause =
                causeOfType(thrown, StreamsApp.CorruptedStateStoreException.class);
        assertTrue(cause.getMessage().contains("переполнение счётчика заказов"),
                "сообщение должно называть переполнение счётчика: " + cause.getMessage());
    }

    /**
     * (8) Отрицательный orders в сторе — заведомо невозможное состояние
     * (первая запись клиента создаётся с orders=1): признак повреждения, а не
     * данных, поэтому fail-fast, а не молчаливое продолжение накопления.
     */
    @Test
    void negativeOrdersInStoreTriggersFailFast() {
        KeyValueStore<String, String> store = driver.getKeyValueStore(StreamsApp.STORE);
        store.put("5", "{\"orders\":-5,\"total\":10.0}");

        Throwable thrown = assertThrows(Throwable.class,
                () -> in.pipeInput("order-negative", "{\"customer_id\":5,\"amount\":1.0}"));

        StreamsApp.CorruptedStateStoreException cause =
                causeOfType(thrown, StreamsApp.CorruptedStateStoreException.class);
        assertTrue(cause.getMessage().contains("orders=-5"),
                "сообщение должно называть некорректное значение orders: " + cause.getMessage());
    }
}
