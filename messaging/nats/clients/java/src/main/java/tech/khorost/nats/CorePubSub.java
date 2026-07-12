package tech.khorost.nats;

import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.nats.client.Nats;
import io.nats.client.Options;
import io.nats.client.Subscription;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.concurrent.TimeUnit;

/**
 * CorePubSub демонстрирует Core NATS на jnats: асинхронную подписку через
 * {@link Dispatcher}, синхронную подписку через {@link Connection#subscribe},
 * и queue group ({@code Dispatcher.subscribe(subject, queue)}).
 * Запускать против nats/00-core, nats/01-jetstream или nats/02-cluster —
 * см. README.md в корне clients/java.
 */
public final class CorePubSub {

    public static void main(String[] args) throws Exception {
        Options options = NatsConn.options("core-pubsub");

        try (Connection nc = Nats.connect(options)) {
            // Async subscribe через Dispatcher — сервер вызывает обработчик
            // в потоке диспетчера на каждое входящее сообщение. Один
            // Dispatcher может обслуживать несколько подписок на общем
            // потоке — обычный выбор для долгоживущего подписчика.
            Dispatcher dispatcher = nc.createDispatcher(msg ->
                    System.out.printf("[async] %s: %s%n", msg.getSubject(),
                            new String(msg.getData(), StandardCharsets.UTF_8)));
            dispatcher.subscribe("orders.>");

            // Sync subscribe — клиент сам забирает сообщение вызовом
            // nextMessage(timeout), без фонового обработчика. Полезно, когда
            // обработка должна идти строго последовательно в известном месте
            // кода (тестовый сценарий, CLI-утилита), а не в фоне.
            Subscription subSync = nc.subscribe("orders.sync.>");

            // Queue subscribe — несколько подписчиков с одинаковым именем
            // группы делят входящий поток: каждое сообщение достаётся ровно
            // одному участнику. Поднимаем двух воркеров на одном Dispatcher,
            // чтобы демо было видно одним запуском; на практике это разные
            // экземпляры сервиса.
            String queue = "workers";
            for (int i = 1; i <= 2; i++) {
                int workerId = i;
                dispatcher.subscribe("jobs.*", queue, msg ->
                        System.out.printf("[queue worker-%d] %s: %s%n", workerId, msg.getSubject(),
                                new String(msg.getData(), StandardCharsets.UTF_8)));
            }

            System.out.println("core-pubsub: подписки активны, публикуем демо-сообщения");

            nc.publish("orders.eu.new", "{\"id\":1}".getBytes(StandardCharsets.UTF_8));
            nc.publish("orders.sync.new", "{\"id\":2}".getBytes(StandardCharsets.UTF_8));

            var syncMsg = subSync.nextMessage(Duration.ofSeconds(2));
            if (syncMsg != null) {
                System.out.printf("[sync] %s: %s%n", syncMsg.getSubject(),
                        new String(syncMsg.getData(), StandardCharsets.UTF_8));
            } else {
                System.out.println("[sync] сообщение не пришло за 2s");
            }

            for (int i = 0; i < 3; i++) {
                nc.publish("jobs.a", "task".getBytes(StandardCharsets.UTF_8));
            }

            // flush — дождаться, что все publish() выше реально ушли на
            // сервер, прежде чем дать обработчикам время отработать.
            // publish() буферизует запись на клиенте; без flush (или паузы
            // ниже) короткоживущий процесс может завершиться раньше, чем
            // данные покинут буфер.
            nc.flush(Duration.ofSeconds(2));

            TimeUnit.MILLISECONDS.sleep(500);

            System.out.println("core-pubsub: демо завершено, дренируем соединение (drain, не close) и выходим");
            nc.drain(Duration.ofSeconds(5)).get(10, TimeUnit.SECONDS);
        }
    }

    private CorePubSub() {
    }
}
