package tech.khorost.kafka.consumergroups;

import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.serialization.StringSerializer;

import java.util.Properties;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Непрерывно шлёт сообщения в demo-groups, пока не вызван stop(), чередуя
 * ключи part-0..part-{PARTITIONS-1}. Продюсинг во время работы консьюмеров
 * — осознанный выбор: см. содержательный content-note в README про то,
 * почему это важно для наглядности живого ребаланса (иначе один консьюмер
 * успевает дочитать всё до подключения второго — урок из
 * java-deep-dive/messaging).
 */
public final class ContinuousProducer {
    public final AtomicLong sent = new AtomicLong();
    private final AtomicBoolean stopFlag = new AtomicBoolean(false);
    private final Thread thread;
    private final KafkaProducer<String, String> producer;

    public ContinuousProducer(long intervalMs) {
        Properties props = new Properties();
        props.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, Kafka.BOOTSTRAP);
        props.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        props.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class.getName());
        this.producer = new KafkaProducer<>(props);

        this.thread = new Thread(() -> {
            int i = 0;
            while (!stopFlag.get()) {
                String key = "part-" + (i % Kafka.PARTITIONS);
                String val = "live-" + i;
                i++;
                producer.send(new ProducerRecord<>(Kafka.TOPIC, key, val), (meta, err) -> {
                    if (err == null) {
                        sent.incrementAndGet();
                    }
                });
                Kafka.sleep(intervalMs);
            }
        }, "continuous-producer");
        this.thread.setDaemon(true);
        this.thread.start();
    }

    public void stop() {
        stopFlag.set(true);
        try {
            thread.join(5_000);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        producer.close();
    }
}
