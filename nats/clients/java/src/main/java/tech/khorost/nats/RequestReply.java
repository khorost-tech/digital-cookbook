package tech.khorost.nats;

import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.nats.client.Message;
import io.nats.client.Nats;
import io.nats.client.Options;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.OffsetDateTime;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

/**
 * RequestReply демонстрирует встроенный request/reply jnats: сервис-
 * responder (подписывается через queue group — так несколько экземпляров
 * сервиса делят входящие запросы) и клиент-requester с таймаутом через
 * {@link CompletableFuture#get(long, TimeUnit)}.
 */
public final class RequestReply {

    private static final String SUBJECT = "service.time";

    public static void main(String[] args) throws Exception {
        Options options = NatsConn.options("request-reply");

        try (Connection nc = Nats.connect(options)) {
            startResponder(nc);

            // Небольшая пауза, чтобы подписка responder'а гарантированно
            // долетела до сервера до первого запроса — в бою запросы просто
            // ретраятся, здесь это для стабильности однократного прогона.
            TimeUnit.MILLISECONDS.sleep(100);

            requestOnce(nc);

            nc.drain(Duration.ofSeconds(5)).get(10, TimeUnit.SECONDS);
        }
    }

    /**
     * startResponder поднимает "сервис", отвечающий на {@value #SUBJECT}.
     * {@code Dispatcher.subscribe(subject, queue)} — тот же приём, что и в
     * 00-core/nats reply: несколько экземпляров сервиса можно запустить
     * параллельно, и NATS сам распределит запросы между ними.
     */
    private static void startResponder(Connection nc) {
        Dispatcher dispatcher = nc.createDispatcher(msg -> {
            String now = OffsetDateTime.now().toString();
            nc.publish(msg.getReplyTo(), now.getBytes(StandardCharsets.UTF_8));
        });
        dispatcher.subscribe(SUBJECT, "time-service");
        System.out.printf("responder: слушаю %s (queue group time-service)%n", SUBJECT);
    }

    /**
     * requestOnce делает один запрос с явным таймаутом.
     * {@code Connection.request(subject, body)} возвращает
     * {@link CompletableFuture}&lt;{@link Message}&gt; — таймаут задаётся при
     * разборе future через {@code get(timeout, unit)}, а не аргументом
     * самого вызова; это тот же принцип, что {@code RequestWithContext} в
     * nats.go, только явный таймаут навязан на стороне вызывающего кода,
     * а не контекстом.
     */
    private static void requestOnce(Connection nc) throws InterruptedException {
        CompletableFuture<Message> future = nc.request(SUBJECT, null);
        try {
            Message msg = future.get(2, TimeUnit.SECONDS);
            System.out.printf("requester: получен ответ: %s%n",
                    new String(msg.getData(), StandardCharsets.UTF_8));
        } catch (TimeoutException e) {
            System.out.println("requester: нет ответа за 2s — responder недоступен или перегружен");
        } catch (ExecutionException e) {
            System.out.printf("requester: request завершился ошибкой: %s%n", e.getCause());
        }
    }

    private RequestReply() {
    }
}
