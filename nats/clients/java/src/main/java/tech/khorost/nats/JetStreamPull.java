package tech.khorost.nats;

import io.nats.client.Connection;
import io.nats.client.ConsumerContext;
import io.nats.client.FetchConsumer;
import io.nats.client.JetStream;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import io.nats.client.Message;
import io.nats.client.Nats;
import io.nats.client.Options;
import io.nats.client.PublishOptions;
import io.nats.client.StreamContext;
import io.nats.client.api.AckPolicy;
import io.nats.client.api.ConsumerConfiguration;
import io.nats.client.api.PublishAck;
import io.nats.client.api.StorageType;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.api.StreamInfo;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.concurrent.TimeUnit;

/**
 * JetStreamPull демонстрирует JetStream на jnats: идемпотентное объявление
 * stream и durable pull-consumer через {@link JetStreamManagement}, publish
 * с {@code Nats-Msg-Id} (дедупликация) через {@link PublishOptions}, чтение
 * через {@link ConsumerContext#fetchMessages(int)} и ручной {@code ack()}.
 * Запускать против nats/01-jetstream или nats/02-cluster.
 */
public final class JetStreamPull {

    private static final String STREAM_NAME = "ORDERS";
    private static final String STREAM_SUBJECT = "orders.>";
    private static final String PUB_SUBJECT = "orders.new";
    private static final String CONSUMER_NAME = "proc";

    public static void main(String[] args) throws Exception {
        Options options = NatsConn.options("jetstream-pull");

        try (Connection nc = Nats.connect(options)) {
            JetStreamManagement jsm = nc.jetStreamManagement();
            JetStream js = nc.jetStream();

            StreamInfo stream = createOrGetStream(jsm);
            System.out.printf("stream %s готов (subjects: %s)%n", STREAM_NAME,
                    stream.getConfiguration().getSubjects());

            jsm.addOrUpdateConsumer(STREAM_NAME, ConsumerConfiguration.builder()
                    .durable(CONSUMER_NAME)
                    .ackPolicy(AckPolicy.Explicit)
                    // ackWait — сколько сервер ждёт ack, прежде чем считать
                    // сообщение недоставленным и переслать его снова
                    // (redelivery). maxDeliver — сколько раз повторять,
                    // прежде чем сдаться и оставить сообщение в stream без
                    // дальнейших попыток.
                    .ackWait(Duration.ofSeconds(10))
                    .maxDeliver(5)
                    .filterSubject(STREAM_SUBJECT)
                    .build());
            System.out.printf("pull-consumer %s готов (ack: explicit, ackWait: 10s, maxDeliver: 5)%n", CONSUMER_NAME);

            publishWithDedup(js);

            StreamContext streamContext = js.getStreamContext(STREAM_NAME);
            ConsumerContext consumerContext = streamContext.getConsumerContext(CONSUMER_NAME);
            consumeAndAck(consumerContext);

            nc.drain(Duration.ofSeconds(5)).get(10, TimeUnit.SECONDS);
        }
    }

    /**
     * У {@link JetStreamManagement} нет единого идемпотентного
     * "create-or-update" для stream (в отличие от consumer'ов —
     * {@code addOrUpdateConsumer} — и в отличие от Go-пакета jetstream,
     * где {@code CreateOrUpdateStream} есть сразу). Идемпотентность
     * получаем вручную: если stream уже существует, {@code addStream}
     * бросает {@link JetStreamApiException} с кодом 400/10058
     * ("stream name already in use"), и в этом случае просто читаем
     * текущую конфигурацию через {@code getStreamInfo}.
     */
    private static StreamInfo createOrGetStream(JetStreamManagement jsm) throws Exception {
        try {
            return jsm.addStream(StreamConfiguration.builder()
                    .name(STREAM_NAME)
                    .subjects(STREAM_SUBJECT)
                    .storageType(StorageType.File)
                    .build());
        } catch (JetStreamApiException e) {
            if (e.getApiErrorCode() == 10058) { // stream name already in use
                return jsm.getStreamInfo(STREAM_NAME);
            }
            throw e;
        }
    }

    /**
     * publishWithDedup публикует одно и то же сообщение дважды с одинаковым
     * messageId. Второй publish не создаёт вторую запись в stream —
     * JetStream отбрасывает дубликат в пределах окна дедупликации (Duplicate
     * Window, по умолчанию 2 минуты). Это защита от повторной публикации при
     * обрыве подтверждения на стороне publisher'а (отправили, не дождались
     * ack по сети, повторили) — не путать с идемпотентностью обработки на
     * стороне consumer.
     */
    private static void publishWithDedup(JetStream js) throws Exception {
        byte[] payload = "{\"id\":1}".getBytes(StandardCharsets.UTF_8);
        PublishOptions pubOpts = PublishOptions.builder().messageId("order-1").build();

        PublishAck ack1 = js.publish(PUB_SUBJECT, payload, pubOpts);
        System.out.printf("publish #1: seq=%d, duplicate=%b%n", ack1.getSeqno(), ack1.isDuplicate());

        PublishAck ack2 = js.publish(PUB_SUBJECT, payload, pubOpts);
        System.out.printf("publish #2 (тот же messageId): seq=%d, duplicate=%b%n", ack2.getSeqno(), ack2.isDuplicate());

        if (!ack2.isDuplicate()) {
            System.out.println("предупреждение: сервер не пометил повтор как duplicate — проверьте окно дедупа стрима");
        }
    }

    /**
     * consumeAndAck читает через pull-consumer с {@code fetchMessages} —
     * забирает фиксированный батч и обрабатывает его синхронно. Для
     * долгоживущего воркера уместнее {@code consume(handler)} (см.
     * комментарий ниже), но fetch проще для одноразового демо-прогона и явно
     * показывает, что клиент сам управляет темпом чтения — в этом и разница
     * pull vs push.
     */
    private static void consumeAndAck(ConsumerContext consumerContext) throws Exception {
        int received = 0;
        try (FetchConsumer fetchConsumer = consumerContext.fetchMessages(1)) {
            Message msg;
            while ((msg = fetchConsumer.nextMessage()) != null) {
                received++;
                var meta = msg.metaData();
                System.out.printf("получено: %s: %s (попытка доставки: %d)%n",
                        msg.getSubject(), new String(msg.getData(), StandardCharsets.UTF_8),
                        meta.deliveredCount());

                // ack() (а не ackSync()) достаточно в обычном пути: клиент
                // не ждёт подтверждения от сервера, что ack принят.
                // ackSync() пригодился бы, если приложению критично
                // убедиться, что именно этот ack долетел (например, перед
                // необратимым побочным эффектом).
                msg.ack();
            }
        }
        if (received == 0) {
            throw new IllegalStateException("не получено ни одного сообщения за таймаут fetch");
        }

        // consume(handler) — альтернатива для долгоживущего сервиса:
        // регистрирует колбэк, который сервер вызывает на каждое новое
        // сообщение, с автоматическим управлением pull-запросами под
        // капотом (клиент сам приходит за новым батчем, приложению это не
        // видно):
        //
        //   MessageConsumer mc = consumerContext.consume(msg -> {
        //       // обработка + msg.ack();
        //   });
        //   mc.stop();  // не начинать новые pull-запросы
        //   mc.close(); // отписаться
        //
        // В этом демо не используется, чтобы процесс завершался предсказуемо
        // после одного сообщения, а не работал бесконечно. msg.nak() /
        // msg.nakWithDelay(d) — явный отказ (сообщение придёт снова, не
        // раньше чем через d); msg.inProgress() продлевает ackWait для
        // длинной обработки; msg.term() — прекратить redelivery без успеха,
        // даже если maxDeliver не исчерпан.
    }

    private JetStreamPull() {
    }
}
