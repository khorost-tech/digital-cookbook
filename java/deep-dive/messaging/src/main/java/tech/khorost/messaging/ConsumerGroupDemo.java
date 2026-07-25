package tech.khorost.messaging;

import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRebalanceListener;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.TopicPartition;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;

import java.time.Duration;
import java.util.Collection;
import java.util.List;
import java.util.Properties;
import java.util.Set;
import java.util.TreeSet;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * (2) Consumer groups: 2 консьюмера в одной группе на топике с 4 партициями.
 * Сперва запускается один консьюмер (получает все 4 партиции), затем второй —
 * это триггерит ребаланс, залогированный через {@link ConsumerRebalanceListener}.
 * После оседания группы партиции должны быть распределены между обоими без
 * пересечений.
 */
public final class ConsumerGroupDemo {
    public static final String TOPIC = "demo-groups";
    public static final int PARTITIONS = 4;
    public static final String GROUP_ID = "group-demo";

    private ConsumerGroupDemo() {
    }

    public static final class Result {
        final Set<TopicPartition> assignment1 = new TreeSet<>(ConsumerGroupDemo::comparePartitions);
        final Set<TopicPartition> assignment2 = new TreeSet<>(ConsumerGroupDemo::comparePartitions);
        int consumed1;
        int consumed2;
    }

    private static int comparePartitions(TopicPartition a, TopicPartition b) {
        return Integer.compare(a.partition(), b.partition());
    }

    public static Result run(int messageCount) throws InterruptedException {
        Kafka.ensureTopic(TOPIC, PARTITIONS, (short) 1);
        produce(messageCount);

        Result result = new Result();
        AtomicBoolean stop = new AtomicBoolean(false);

        Worker worker1 = new Worker("consumer-1", stop);
        Thread t1 = new Thread(worker1, "consumer-1-thread");
        t1.start();

        // Ждём, пока первый консьюмер в одиночку заберёт все партиции топика.
        if (!worker1.firstAssignment.await(15, TimeUnit.SECONDS)) {
            throw new IllegalStateException("consumer-1 не получил начальное назначение партиций");
        }
        System.out.printf("  [group] consumer-1 (в одиночку) assigned=%s%n", worker1.lastAssignment);

        Worker worker2 = new Worker("consumer-2", stop);
        Thread t2 = new Thread(worker2, "consumer-2-thread");
        t2.start();

        // Ждём, пока второй консьюмер тоже получит назначение (ребаланс завершён).
        if (!worker2.firstAssignment.await(20, TimeUnit.SECONDS)) {
            throw new IllegalStateException("consumer-2 не получил назначение партиций после ребаланса");
        }
        System.out.printf("  [group] после подключения consumer-2 (ребаланс): consumer-1 assigned=%s, consumer-2 assigned=%s%n",
                worker1.lastAssignment, worker2.lastAssignment);

        // Даём группе устояться и дочитать сообщения обоими консьюмерами.
        long deadline = System.currentTimeMillis() + 15_000;
        while (System.currentTimeMillis() < deadline
                && worker1.consumed.get() + worker2.consumed.get() < messageCount) {
            Kafka.sleep(300);
        }

        stop.set(true);
        t1.join(10_000);
        t2.join(10_000);

        result.assignment1.addAll(worker1.lastAssignment);
        result.assignment2.addAll(worker2.lastAssignment);
        result.consumed1 = worker1.consumed.get();
        result.consumed2 = worker2.consumed.get();

        System.out.printf("  [group] финал: consumer-1 partitions=%s consumed=%d, consumer-2 partitions=%s consumed=%d%n",
                result.assignment1, result.consumed1, result.assignment2, result.consumed2);
        return result;
    }

    private static void produce(int messageCount) {
        Properties producerProps = new Properties();
        producerProps.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        producerProps.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        producerProps.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        producerProps.put(ProducerConfig.ACKS_CONFIG, "all");
        try (KafkaProducer<String, String> producer = new KafkaProducer<>(producerProps)) {
            for (int i = 0; i < messageCount; i++) {
                // ключ = номер партиции -> гарантированно засеваем ВСЕ партиции сообщениями
                String key = "part-" + (i % PARTITIONS);
                producer.send(new ProducerRecord<>(TOPIC, key, "group-msg-" + i));
            }
        }
        System.out.printf("  [group] засеяно %d сообщений в %d партиций%n", messageCount, PARTITIONS);
    }

    private static final class Worker implements Runnable {
        final String id;
        final AtomicBoolean stop;
        final AtomicInteger consumed = new AtomicInteger();
        final CountDownLatch firstAssignment = new CountDownLatch(1);
        volatile Set<TopicPartition> lastAssignment = Set.of();

        Worker(String id, AtomicBoolean stop) {
            this.id = id;
            this.stop = stop;
        }

        @Override
        public void run() {
            Properties consumerProps = new Properties();
            consumerProps.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
            consumerProps.put(ConsumerConfig.GROUP_ID_CONFIG, GROUP_ID);
            consumerProps.put(ConsumerConfig.CLIENT_ID_CONFIG, id);
            consumerProps.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
            consumerProps.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class.getName());
            consumerProps.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
            consumerProps.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "true");

            try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(consumerProps)) {
                consumer.subscribe(List.of(TOPIC), new ConsumerRebalanceListener() {
                    @Override
                    public void onPartitionsRevoked(Collection<TopicPartition> partitions) {
                        if (!partitions.isEmpty()) {
                            System.out.printf("  [rebalance] %s: revoked %s%n", id, partitions);
                        }
                    }

                    @Override
                    public void onPartitionsAssigned(Collection<TopicPartition> partitions) {
                        lastAssignment = Set.copyOf(partitions);
                        System.out.printf("  [rebalance] %s: assigned %s%n", id, partitions);
                        firstAssignment.countDown();
                    }
                });

                while (!stop.get()) {
                    ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(300));
                    for (ConsumerRecord<String, String> r : records) {
                        consumed.incrementAndGet();
                    }
                }
            }
        }
    }
}
