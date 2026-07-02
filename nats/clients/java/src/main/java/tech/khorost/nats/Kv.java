package tech.khorost.nats;

import io.nats.client.Connection;
import io.nats.client.KeyValue;
import io.nats.client.KeyValueManagement;
import io.nats.client.Nats;
import io.nats.client.Options;
import io.nats.client.api.KeyValueConfiguration;
import io.nats.client.api.KeyValueEntry;
import io.nats.client.api.KeyValueWatcher;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

/**
 * Kv демонстрирует KV API jnats: put/get/watch поверх bucket'а,
 * материализованного как stream {@code KV_<bucket>}. В отличие от Go
 * ({@code jetstream.KeyValueWatcher.Updates()} — канал), в jnats watch —
 * callback-интерфейс {@link io.nats.client.KeyValueWatcher}, вызываемый в
 * отдельном потоке. Запускать против nats/01-jetstream или nats/02-cluster.
 */
public final class Kv {

    private static final String BUCKET_NAME = "config";
    private static final String KEY = "feature.flag";

    public static void main(String[] args) throws Exception {
        Options options = NatsConn.options("kv");

        try (Connection nc = Nats.connect(options)) {
            KeyValueManagement kvm = nc.keyValueManagement();
            createOrGetBucket(kvm);
            System.out.printf("kv bucket %s готов (материализован как stream KV_%s)%n", BUCKET_NAME, BUCKET_NAME);

            KeyValue kv = nc.keyValue(BUCKET_NAME);

            // Ждём двух событий: начальное значение (watch всегда присылает
            // его первым) и следующее обновление. KeyValueWatcher — не
            // функциональный интерфейс (два абстрактных метода: watch и
            // endOfData), поэтому лямбда не годится — только анонимный
            // класс. endOfData() зовётся один раз, когда история
            // "довычитана" и начинаются живые события — аналог nil-записи
            // в канале watcher.Updates() у nats.go.
            CountDownLatch seenTwice = new CountDownLatch(2);
            var watchSub = kv.watch(KEY, new KeyValueWatcher() {
                @Override
                public void watch(KeyValueEntry entry) {
                    System.out.printf("watch: %s = %s (revision %d)%n",
                            entry.getKey(), entry.getValueAsString(), entry.getRevision());
                    seenTwice.countDown();
                }

                @Override
                public void endOfData() {
                    // История довычитана — здесь не разбирается отдельно,
                    // чтобы не усложнять демо (см. комментарий выше).
                }
            });

            // Небольшая пауза, чтобы watcher успел подписаться до первого
            // put — иначе можно пропустить самое первое обновление.
            TimeUnit.MILLISECONDS.sleep(200);

            long rev = kv.put(KEY, "true".getBytes(StandardCharsets.UTF_8));
            System.out.printf("put %s=true (revision %d)%n", KEY, rev);

            KeyValueEntry entry = kv.get(KEY);
            System.out.printf("get %s: %s @ revision %d%n", KEY, entry.getValueAsString(), entry.getRevision());

            // Второй put — чтобы watcher увидел обновление, а не только
            // начальное состояние.
            kv.put(KEY, "false".getBytes(StandardCharsets.UTF_8));
            System.out.printf("put %s=false%n", KEY);

            if (!seenTwice.await(3, TimeUnit.SECONDS)) {
                System.out.println("watch: не дождались второго обновления за таймаут");
            }
            watchSub.close();

            nc.drain(Duration.ofSeconds(5)).get(10, TimeUnit.SECONDS);
        }
    }

    private static void createOrGetBucket(KeyValueManagement kvm) throws Exception {
        try {
            kvm.create(KeyValueConfiguration.builder()
                    .name(BUCKET_NAME)
                    .build());
        } catch (Exception e) {
            // Bucket уже существует (создан предыдущим прогоном демо) —
            // KeyValueManagement, как и JetStreamManagement, не даёт единого
            // идемпотентного create-or-update; в этом демо это не критично,
            // достаточно продолжить работу с уже существующим bucket'ом.
        }
    }

    private Kv() {
    }
}
