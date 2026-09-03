import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.apache.kafka.common.serialization.Serdes;
import org.apache.kafka.streams.*;
import org.apache.kafka.streams.errors.StreamsUncaughtExceptionHandler;
import org.apache.kafka.streams.kstream.*;
import org.apache.kafka.streams.state.Stores;

import java.util.List;
import java.util.Properties;

/**
 * Kafka Streams: агрегация заказов по клиенту.
 *
 * Смысл демо — не сама агрегация, а модель: обработка живёт БИБЛИОТЕКОЙ ВНУТРИ сервиса,
 * без отдельного кластера. Состояние лежит локально (RocksDB) и дублируется в
 * changelog-топик, из которого восстанавливается после падения.
 *
 * EXACTLY_ONCE_V2 покрывает read-process-write ВНУТРИ Kafka. Запись в ClickHouse
 * (Go-консьюмер) уже вне этой транзакции — там дедуп обязателен (см. статью #3).
 */
public class StreamsApp {
    static final String IN = "orders.events";
    static final String OUT = "customer.totals";
    static final String STORE = "customer-totals-store";
    private static final ObjectMapper M = new ObjectMapper();

    public static void main(String[] args) {
        Properties p = new Properties();
        p.put(StreamsConfig.APPLICATION_ID_CONFIG, "ds-streams");
        p.put(StreamsConfig.BOOTSTRAP_SERVERS_CONFIG, env("KAFKA", "localhost:9096"));
        p.put(StreamsConfig.DEFAULT_KEY_SERDE_CLASS_CONFIG, Serdes.String().getClass());
        p.put(StreamsConfig.DEFAULT_VALUE_SERDE_CLASS_CONFIG, Serdes.String().getClass());
        p.put(StreamsConfig.PROCESSING_GUARANTEE_CONFIG, StreamsConfig.EXACTLY_ONCE_V2);
        p.put(StreamsConfig.STATE_DIR_CONFIG, env("STATE_DIR", "/tmp/ds-streams"));

        KafkaStreams streams = new KafkaStreams(buildTopology(), p);

        // Поведение при необработанной ошибке stream-треда должно быть осознанным
        // решением, а не умолчанием библиотеки. Политика здесь одна и БЕЗУСЛОВНАЯ:
        // любая необработанная ошибка останавливает клиента (fail-fast).
        // REPLACE_THREAD не используется нигде, поэтому и разбора типов исключений
        // здесь нет — ответ не зависит от причины.
        //
        // Почему не REPLACE_THREAD:
        // - Повреждённая запись стора (CorruptedStateStoreException, см.
        //   TotalsProcessor.process()) — причина ДЕТЕРМИНИРОВАННАЯ: новый поток
        //   прочитает тот же неизменившийся prev и бросит то же исключение снова,
        //   то есть бесконечный краш-луп вместо восстановления. SHUTDOWN_CLIENT —
        //   однократная чистая остановка: оператор видит причину в логе и чинит
        //   стор (пересоздание + replay из changelog либо миграция формата значения).
        // - Любая ещё не классифицированная причина не безопаснее: неизвестное
        //   исключение с той же вероятностью окажется детерминированным багом
        //   (NullPointerException на конкретном входе), а не транзиентным сбоем
        //   инфраструктуры — перезапуск воспроизвёл бы ту же ошибку на том же входе.
        //
        // REPLACE_THREAD был бы уместен только как ОСОЗНАННЫЙ opt-in для причин,
        // явно классифицированных как транзиентные (скажем, отдельное исключение
        // для временной недоступности брокера). Код этого стенда таких причин не
        // порождает — вводить классификацию, у которой все ветки дают один и тот же
        // ответ, значило бы имитировать выбор, которого нет.
        streams.setUncaughtExceptionHandler(throwable -> {
            System.err.println("необработанная ошибка stream-треда: " + throwable);
            return StreamsUncaughtExceptionHandler.StreamThreadExceptionResponse.SHUTDOWN_CLIENT;
        });

        Runtime.getRuntime().addShutdownHook(new Thread(streams::close));
        streams.start();
        System.out.println("streams запущен: " + IN + " -> " + OUT + " (store=" + STORE + ")");
    }

    /**
     * Строит топологию (repartition + Processor API + state store) без побочных
     * эффектов запуска (без Properties брокера, без KafkaStreams/start()). Вынесено
     * из main() отдельным методом ради тестируемости — src/test/java/StreamsAppTest.java
     * прогоняет ЭТУ ЖЕ топологию через TopologyTestDriver (без живого кластера Kafka),
     * а не копию логики: расхождение между "что реально работает" и "что покрыто
     * тестом" здесь структурно невозможно.
     */
    static Topology buildTopology() {
        StreamsBuilder b = new StreamsBuilder();
        b.addStateStore(Stores.keyValueStoreBuilder(
                Stores.persistentKeyValueStore(STORE), Serdes.String(), Serdes.String()));

        b.<String, String>stream(IN, Consumed.with(Serdes.String(), Serdes.String()))
         // Ключ orders.events — order_id (Debezium: aggregateid outbox-события).
         // TotalsProcessor ниже агрегирует по customer_id в локальном state store.
         // Processor API САМ ПО СЕБЕ не меняет партиционирование: addStateStore()
         // просто прикрепляет стор к тем же task'ам, что читают входные партиции.
         // Пока orders.events была 1 партиция, все заказы физически проходили через
         // единственный task — баг маскировался. При нескольких партициях заказы
         // одного клиента с разными order_id разъедутся по разным task'ам с
         // ИЗОЛИРОВАННЫМИ сторами, и агрегация по клиенту станет молча неверной
         // (несколько частичных сумм на одного клиента вместо одной полной).
         // Поэтому переключаем ключ на customer_id и ЯВНО репартиционируем: Kafka
         // Streams создаст промежуточный repartition-топик и перетасует записи так,
         // чтобы все события одного клиента шли в один и тот же task/партицию/стор
         // (co-partitioning). DSL-агрегация (groupBy) сделала бы это неявно сама —
         // здесь это делаем руками, потому что тема статьи — сырой Processor API
         // со state store.
         .flatMap((orderId, json) -> {
             // Единая точка разбора и валидации: customer_id и amount проверяются
             // ОДИН раз здесь (см. parseEvent). Ниже по конвейеру (TotalsProcessor)
             // JSON больше не парсится вообще — приходит уже готовое число (amount),
             // а customer_id живёт в ключе записи. Это устраняет источник регрессии
             // из прошлого фикса: TotalsProcessor.process() парсил amount из
             // rec.value() заново и мог получить необработанное исключение (там же,
             // отдельно, Long.parseLong(cust) на невалидированной строке ронял
             // stream-тред в бесконечный REPLACE_THREAD-цикл, потому что "битый"
             // customer_id проходил мимо проверки в flatMap — она смотрела только
             // на null/отсутствие поля, не на численность).
             ParsedEvent ev = parseEvent(json);
             if (ev == null) {
                 System.err.println("пропущено битое событие (order=" + orderId + "): невалидный JSON, нечисловой/отсутствующий customer_id или amount, либо amount не конечен (NaN/Infinity)");
                 return List.<KeyValue<String, String>>of();
             }
             return List.of(KeyValue.pair(Long.toString(ev.customerId()), Double.toString(ev.amount())));
         })
         // Число партиций repartition-топика ниже НЕ задано явно (нет
         // .withNumberOfPartitions(N)) — Kafka Streams наследует его от апстрима,
         // то есть от фактического числа партиций orders.events на момент первого
         // запуска приложения. Это неявная зависимость: пересоздание orders.events
         // с другим числом партиций молча меняет и число партиций
         // ds-streams-customer-totals-repartition вместе с ним. Для канонического
         // примера читателю стоит это знать; если нужно зафиксировать число партиций
         // независимо от orders.events — используйте .withNumberOfPartitions(N).
         .repartition(Repartitioned.<String, String>as("customer-totals")
                 .withKeySerde(Serdes.String())
                 .withValueSerde(Serdes.String()))
         .process(TotalsProcessor::new, STORE)
         .to(OUT, Produced.with(Serdes.String(), Serdes.String()));

        return b.build();
    }

    /** Типизированный результат разбора события: customer_id и amount уже проверены. */
    record ParsedEvent(long customerId, double amount) {}

    /**
     * Единая точка разбора и валидации входного события. Возвращает null, если JSON
     * битый, customer_id отсутствует/не является числом или amount отсутствует/не
     * является числом. Это единственное место в конвейере, где парсится входной JSON —
     * дальше (TotalsProcessor) идут уже готовые типизированные значения, повторного
     * разбора нет и, соответственно, нет второго места, где валидация может разойтись
     * с первым (как это уже один раз случилось: customer_id проверялся здесь только на
     * null, а численность неявно предполагалась только в process()).
     */
    // package-private (не private) намеренно: разбор и валидация одного события —
    // чистая функция без Kafka, и она покрыта прямыми юнит-тестами (см.
    // StreamsAppTest: "NaN"/"Infinity"/"-Infinity" и прочие невалидные входы).
    static ParsedEvent parseEvent(String json) {
        try {
            JsonNode ev = M.readTree(json);
            JsonNode custNode = ev.get("customer_id");
            JsonNode amtNode = ev.get("amount");
            if (custNode == null || custNode.isNull() || amtNode == null || amtNode.isNull()) {
                return null;
            }

            long customerId;
            if (custNode.isIntegralNumber()) {
                // canConvertToLong() обязателен: isIntegralNumber() истинен и для
                // BigInteger, не влезающего в long, а asLong() в этом случае молча
                // УСЕКАЕТ значение. Например, customer_id=18446744073709551617
                // (2^64+1) даёт asLong()==1 — событие чужого/битого идентификатора
                // приписалось бы РЕАЛЬНОМУ клиенту 1 и исказило его сумму. Это
                // тихое смешивание клиентов, а не отказ, поэтому проверка здесь.
                if (!custNode.canConvertToLong()) {
                    return null;
                }
                customerId = custNode.asLong();
            } else if (custNode.isTextual()) {
                // Long.parseLong может бросить NumberFormatException (например,
                // customer_id="abc") — она ловится общим catch ниже и превращает
                // событие в штатно отброшенное, а не в необработанное исключение,
                // всплывающее до UncaughtExceptionHandler.
                customerId = Long.parseLong(custNode.asText().trim());
            } else {
                return null;
            }

            double amount;
            if (amtNode.isNumber()) {
                amount = amtNode.asDouble();
            } else if (amtNode.isTextual()) {
                // ВНИМАНИЕ: Double.parseDouble принимает "NaN", "Infinity" и
                // "-Infinity" — это валидные литералы Java, а не ошибка разбора,
                // NumberFormatException на них НЕ бросается. Без явной проверки
                // ниже такое значение прошло бы дальше как обычное число.
                amount = Double.parseDouble(amtNode.asText().trim());
            } else {
                return null;
            }

            // Non-finite amount (NaN/±Infinity) — битое событие, а не число:
            // молча просочившись в агрегат, NaN обнулил бы его на округлении
            // (Math.round(NaN)==0), а Infinity превратил бы в
            // Math.round(Inf)/100 == 9.223372036854776E16. То есть это не отказ,
            // который кто-то заметит, а ТИХОЕ искажение суммы. Отбрасываем тем же
            // путём, что и остальные невалидные события (см. flatMap выше).
            if (!Double.isFinite(amount)) {
                return null;
            }

            return new ParsedEvent(customerId, amount);
        } catch (Exception e) {
            return null;
        }
    }

    /**
     * Повреждённая/неполная запись state store (см. TotalsProcessor.process() ниже —
     * единственное место, где бросается). Означает, что доверять текущему значению
     * стора для агрегации нельзя. НЕ ловится и не глушится внутри process() —
     * всплывает как uncaught exception stream-треда, где setUncaughtExceptionHandler
     * (см. main()) реагирует на неё SHUTDOWN_CLIENT: чистой однократной остановкой,
     * а не REPLACE_THREAD (тот привёл бы к краш-лупу на той же повреждённой записи)
     * и не тихим сбросом состояния клиента.
     */
    static final class CorruptedStateStoreException extends RuntimeException {
        CorruptedStateStoreException(String message) {
            super(message);
        }
    }

    /**
     * Накапливает по клиенту число заказов и сумму; состояние — в state store.
     * На вход приходят уже репартиционированные записи: ключ — customer_id.
     */
    static class TotalsProcessor implements org.apache.kafka.streams.processor.api.Processor<String, String, String, String> {
        private org.apache.kafka.streams.state.KeyValueStore<String, String> store;
        private org.apache.kafka.streams.processor.api.ProcessorContext<String, String> ctx;

        @Override
        public void init(org.apache.kafka.streams.processor.api.ProcessorContext<String, String> context) {
            this.ctx = context;
            this.store = context.getStateStore(STORE);
        }

        @Override
        public void process(org.apache.kafka.streams.processor.api.Record<String, String> rec) {
            String cust = rec.key();
            // Никакого разбора JSON здесь больше нет: customer_id и amount уже проверены
            // и превращены в типизированные значения в parseEvent() (см. flatMap выше) —
            // это единственная точка разбора/валидации входного события во всём
            // конвейере. rec.value() тут — просто Double.toString(amount) из flatMap,
            // гарантированно валидное число; Double.parseDouble на нём не может бросить
            // NumberFormatException. cust (ключ) — гарантированно Long.toString от уже
            // провалидированного customer_id, поэтому Long.parseLong(cust) ниже по коду
            // тоже безопасен.
            // amount/total — double: осознанное упрощение ради краткости канонического
            // примера (частично компенсируется округлением до копеек ниже при записи).
            // В проде деньги считают в целых копейках (long) или BigDecimal, не double.
            double amt = Double.parseDouble(rec.value());

            // store.get() падает только на сбое самого стора (RocksDB/сериализация) —
            // это инфраструктурная ошибка, её не глушим, пусть всплывает как есть.
            String prev = store.get(cust);
            long orders = 1;
            double total = amt;
            if (prev != null) {
                // Собственные данные стора читаем так же защищённо, как входное событие
                // в parseEvent(), а не наивно: JsonNode.get(field) возвращает Java null
                // (не NullNode), если поля нет, а .asLong()/.asDouble() на null бросают
                // NullPointerException — её catch(JsonProcessingException) НЕ ловит,
                // поэтому оба случая (невалидный JSON и отсутствующие/нечисловые поля)
                // проверяются явно ниже.
                //
                // Правильное поведение на повреждённую/неполную запись стора —
                // ГРОМКАЯ ОСТАНОВКА (fail-fast), а не тихий сброс состояния клиента на
                // orders=1. Предыдущая редакция этого кода трактовала повреждённую
                // запись как ОТСУТСТВУЮЩЕЕ состояние и начинала накопление заново —
                // это останавливало краш-луп ценой ДАННЫХ: накопленная история клиента
                // (все прежние orders/total) терялась НАВСЕГДА, заниженное значение
                // уходило в store.put() → changelog-топик → customer.totals →
                // ClickHouse, и ни один последующий шаг конвейера это не замечал и не
                // чинил. Запускаемый учебный код не должен молча публиковать испорченный
                // агрегат — поэтому ниже вместо сброса бросается
                // CorruptedStateStoreException: безусловный обработчик в main()
                // (setUncaughtExceptionHandler) отвечает SHUTDOWN_CLIENT —
                // чистой однократной остановкой. REPLACE_THREAD нигде в этом коде не
                // используется (ни для этой причины, ни как дефолт для любой другой,
                // см. развёрнутое обоснование у обработчика в main()) — именно
                // REPLACE_THREAD в цикле на одной и той же причине и есть краш-луп,
                // которого избегает и основной путь — Long.parseLong(cust) в
                // parseEvent(), см. flatMap выше, — не давая невалидному customer_id
                // вообще дойти до необработанного исключения.
                // Оператор видит причину (какой customer, что именно не так) в логе и
                // чинит стор: пересоздание + replay из changelog-топика (истина уже
                // там, просто локальный RocksDB её потерял/испортил), либо явная
                // миграция формата значения, если повреждение вызвано изменившейся
                // схемой, а не физическим сбоем.
                //
                // Этот путь НЕДОСТИЖИМ в этом стенде: один и тот же код (process()
                // ниже) и пишет, и читает формат стора — расхождению формата, которое
                // единственно и могло бы породить эту ветку, здесь взяться неоткуда
                // (в проде это реальный сценарий: стор пережил деплой, схема значения
                // эволюционировала). Поэтому fail-fast НЕ влияет ни на одно число
                // демонстрации (см. FIXTURES.md) — он делает код корректным ПО
                // УМОЛЧАНИЮ на случай будущего изменения формата, а не меняет
                // наблюдаемое поведение сегодня.
                try {
                    JsonNode p = M.readTree(prev);
                    JsonNode ordersNode = p.get("orders");
                    JsonNode totalNode = p.get("total");
                    // canConvertToLong() — как и при разборе customer_id выше:
                    // isIntegralNumber() истинен и для BigInteger вне диапазона long,
                    // а asLong() молча усёк бы значение.
                    if (ordersNode != null && ordersNode.isIntegralNumber() && ordersNode.canConvertToLong()
                            && totalNode != null && totalNode.isNumber()) {
                        long prevOrders = ordersNode.asLong();
                        // Реальный агрегат никогда не имеет orders<1 (первая запись
                        // клиента создаётся с orders=1). Отрицательное или нулевое
                        // значение в сторе — признак повреждения, а не данных.
                        if (prevOrders < 1) {
                            throw new CorruptedStateStoreException("повреждённая запись стора (customer=" + cust
                                    + "): orders=" + prevOrders + " < 1, значение='" + prev + "'");
                        }
                        try {
                            // Math.addExact вместо "+1": на orders=Long.MAX_VALUE
                            // обычное сложение молча даёт Long.MIN_VALUE, и в стор с
                            // changelog уехало бы ОТРИЦАТЕЛЬНОЕ число заказов.
                            orders = Math.addExact(prevOrders, 1);
                        } catch (ArithmeticException e) {
                            throw new CorruptedStateStoreException("повреждённая запись стора (customer=" + cust
                                    + "): переполнение счётчика заказов (orders=" + prevOrders
                                    + "), значение='" + prev + "'");
                        }
                        total = totalNode.asDouble() + amt;
                    } else {
                        throw new CorruptedStateStoreException("повреждённая запись стора (customer=" + cust
                                + "): поля orders/total отсутствуют, не число или вне диапазона long, значение='" + prev + "'");
                    }
                } catch (com.fasterxml.jackson.core.JsonProcessingException e) {
                    throw new CorruptedStateStoreException("повреждённая запись стора (customer=" + cust
                            + "): невалидный JSON (" + e.getMessage() + "), значение='" + prev + "'");
                }
            }

            // Вторая линия обороны после проверки amount в parseEvent(): даже из
            // конечных слагаемых сумма может выйти за пределы double (переполнение
            // до ±Infinity) при экстремальных накопленных значениях. Писать такой
            // агрегат нельзя: Math.round(±Inf)/100 ниже дал бы 9.223372036854776E16,
            // то есть ТИХО искажённое число вместо явного отказа. Останавливаемся
            // громко — агрегат в этом состоянии непредставим и починить его
            // продолжением обработки невозможно (обработчик отвечает
            // SHUTDOWN_CLIENT, см. безусловный обработчик в main()).
            if (!Double.isFinite(total)) {
                throw new IllegalStateException("непредставимый агрегат (customer=" + cust
                        + "): сумма вышла за пределы double (total=" + total
                        + "), обработка остановлена вместо тихого искажения");
            }

            ObjectNode out = M.createObjectNode();
            out.put("customer_id", Long.parseLong(cust));
            out.put("orders", orders);
            out.put("total", Math.round(total * 100.0) / 100.0);
            String json;
            try {
                // Сериализация ЗАВЕДОМО валидного ObjectNode из примитивов практически
                // не может провалиться — но writeValueAsString() объявляет checked
                // JsonProcessingException, поэтому оборачиваем в unchecked и намеренно
                // НЕ глушим: это была бы инфраструктурная аномалия, а не проблема данных.
                json = M.writeValueAsString(out);
            } catch (com.fasterxml.jackson.core.JsonProcessingException e) {
                throw new RuntimeException("не удалось сериализовать агрегат для customer=" + cust, e);
            }

            store.put(cust, json);                       // → локальный RocksDB + changelog
            ctx.forward(rec.withKey(cust).withValue(json));
        }
    }

    static String env(String k, String d) {
        String v = System.getenv(k);
        return v == null || v.isEmpty() ? d : v;
    }
}
