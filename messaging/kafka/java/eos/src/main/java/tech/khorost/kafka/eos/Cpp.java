package tech.khorost.kafka.eos;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.consumer.OffsetAndMetadata;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.TopicPartition;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;

import java.time.Duration;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;

/**
 * (3) Атомарный consume-process-produce — ядро EOS. Читаем из input-топика в
 * составе consumer-группы, обрабатываем, пишем в output + коммитим офсет
 * input в ОДНОЙ транзакции через явный
 * {@code producer.sendOffsetsToTransaction(offsets, consumer.groupMetadata())}
 * ПЕРЕД {@code commitTransaction()}.
 *
 * <p>⚠️ Отличие от franz-go (../../go/eos/cpp.go): в Java этот шаг ЯВНЫЙ —
 * нужно вручную собрать {@code Map<TopicPartition, OffsetAndMetadata>} из
 * последней прочитанной позиции по каждой партиции и передать вместе с
 * {@code consumer.groupMetadata()}. В franz-go {@code GroupTransactSession}
 * делает это НЕЯВНО: сессия сама помнит, что было вычитано с последнего
 * {@code Begin()} (через внутренние {@code CommittedOffsets()}/
 * {@code UncommittedOffsets()}), и коммитит именно эту дельту внутри одного
 * вызова {@code session.End(ctx, kgo.TryCommit)} — та же гарантия
 * (атомарность output+offset), другая форма API: Java требует явно
 * "рассказать" продюсеру, что именно коммитить; franz-go выводит это сам из
 * истории клиента.
 */
public final class Cpp {
    private Cpp() {
    }

    public static void cppSeed(String topic, int n, String prefix) {
        Properties props = new Properties();
        props.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.ACKS_CONFIG, "all");
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(props)) {
            for (int i = 0; i < n; i++) {
                producer.send(new ProducerRecord<>(topic, prefix + "-key-" + i, prefix + "-" + i));
            }
            producer.flush();
        }
        System.out.printf("[cpp-seed] topic=%s: засеяно %d записей%n", topic, n);
    }

    /**
     * ОДНА попытка consume-process-produce. pauseMs>0 — после producer.flush()
     * (записи УЖЕ физически на диске output-партиции под открытой
     * транзакцией), НО ДО sendOffsetsToTransaction+commitTransaction, печатает
     * readyMarker и спит pauseMs — окно, в которое host-скрипт
     * (../../ops/eos-kill.sh) убивает JVM SIGKILL, эмулируя крах ровно между
     * "записали" и "закоммитили".
     *
     * Один и тот же код — и для "убиваемой" попытки (pauseMs>0), и для
     * "успешного повтора" (pauseMs=0): тот же transactional.id — ПЕРВЫЙ вызов
     * producer.initTransactions() в повторе фенсит/абортит зависшую
     * транзакцию предыдущего процесса (KIP-98) ДО начала чтения; та же
     * consumer group — раз офсет предыдущая попытка не закоммитила, повтор
     * читает ТЕ ЖЕ САМЫЕ записи заново.
     */
    public static void cppAttempt(String group, String txnId, String inputTopic, String outputTopic,
                                   int n, long pauseMs, String readyMarker) {
        Properties consumerProps = new Properties();
        consumerProps.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        consumerProps.put(ConsumerConfig.GROUP_ID_CONFIG, group);
        consumerProps.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        consumerProps.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
        consumerProps.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        // ⚠️ enable.auto.commit=false — В ОТЛИЧИЕ от franz-go, где
        // autocommitDisable форсируется автоматически, как только задан
        // TransactionalID (см. content-note в go/eos/cpp.go), в Java это
        // нужно выставить руками: если оставить дефолт (true), фоновый
        // авто-коммит может продвинуть офсет группы МИМО транзакции — это
        // реальный footgun Java-API, которого нет в franz-go.
        consumerProps.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "false");

        Properties producerProps = new Properties();
        producerProps.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        producerProps.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        producerProps.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        producerProps.put(ProducerConfig.TRANSACTIONAL_ID_CONFIG, txnId);
        producerProps.put(ProducerConfig.ACKS_CONFIG, "all");

        try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(consumerProps);
             KafkaProducer<String, String> producer = new KafkaProducer<>(producerProps)) {
            consumer.subscribe(List.of(inputTopic));
            // ⚠️ initTransactions() ЗДЕСЬ — именно этот вызов фенсит/абортит
            // зависшую транзакцию предыдущего процесса с тем же txnId (если
            // он был убит до commitTransaction), см. класс-javadoc.
            producer.initTransactions();

            List<ConsumerRecord<String, String>> input = new ArrayList<>();
            long deadline = System.currentTimeMillis() + 30_000;
            while (input.size() < n && System.currentTimeMillis() < deadline) {
                ConsumerRecords<String, String> recs = consumer.poll(Duration.ofMillis(1000));
                recs.forEach(input::add);
            }
            if (input.size() < n) {
                throw new IllegalStateException("[cpp-attempt] не набрал ожидаемое число входных записей: "
                        + input.size() + " из " + n + " (за 30с) — группа=" + group + " топик=" + inputTopic);
            }
            System.out.printf("[cpp-attempt txn=%s] вычитано из %s: %d записей%n", txnId, inputTopic, input.size());

            producer.beginTransaction();
            for (ConsumerRecord<String, String> r : input) {
                producer.send(new ProducerRecord<>(outputTopic, r.key(), "processed:" + r.value()));
            }
            producer.flush();
            System.out.printf("[cpp-attempt txn=%s] обработано и записано в %s: %d записей (ФИЗИЧЕСКИ на диске, транзакция ещё ОТКРЫТА — офсет %s ещё НЕ закоммичен)%n",
                    txnId, outputTopic, input.size(), inputTopic);

            if (pauseMs > 0) {
                System.out.printf("%s n=%d%n", readyMarker, input.size());
                Kafka.sleep(pauseMs);
            }

            Map<TopicPartition, OffsetAndMetadata> offsets = new HashMap<>();
            for (ConsumerRecord<String, String> r : input) {
                TopicPartition tp = new TopicPartition(r.topic(), r.partition());
                long next = r.offset() + 1;
                offsets.merge(tp, new OffsetAndMetadata(next),
                        (a, b) -> a.offset() > b.offset() ? a : b);
            }
            // ⚠️ Явный аналог того, что franz-go делает неявно внутри
            // session.End() — см. класс-javadoc.
            producer.sendOffsetsToTransaction(offsets, consumer.groupMetadata());
            producer.commitTransaction();
            System.out.printf("[cpp-attempt txn=%s] sendOffsetsToTransaction+commitTransaction -> output И committed-офсет input продвинуты АТОМАРНО одной транзакцией%n", txnId);
        }
    }

    public static void cppVerify(String group, String inputTopic, String outputTopic, String label,
                                  long expectCommittedOutput, long expectGroupOffset) {
        long committedCount = Txn.consumeIsolation(outputTopic, "read_committed", 5000);
        long uncommittedCount = Txn.consumeIsolation(outputTopic, "read_uncommitted", 5000);
        long groupOffset = Kafka.groupCommittedTotal(group, inputTopic);

        System.out.printf("[cpp-verify %s] output read_committed=%d read_uncommitted=%d(физически) input-group(%s) committed-offset=%d%n",
                label, committedCount, uncommittedCount, group, groupOffset);

        if (committedCount != expectCommittedOutput) {
            throw new IllegalStateException("[cpp-verify " + label + "] РАСХОЖДЕНИЕ: output read_committed=" + committedCount + ", ожидалось " + expectCommittedOutput);
        }
        if (groupOffset != expectGroupOffset) {
            throw new IllegalStateException("[cpp-verify " + label + "] РАСХОЖДЕНИЕ: committed-offset группы " + group + " на " + inputTopic
                    + " = " + groupOffset + ", ожидалось " + expectGroupOffset + " — обнаружено ЧАСТИЧНОЕ состояние (нарушена атомарность consume-process-produce)");
        }
        System.out.printf("[cpp-verify %s] OK: output read_committed == ожидалось (%d), committed-offset группы == ожидалось (%d) — частичного состояния нет%n",
                label, committedCount, groupOffset);
    }
}
