package tech.khorost.nats;

import io.nats.client.Connection;
import io.nats.client.ConnectionListener;
import io.nats.client.ErrorListener;
import io.nats.client.Options;

import java.time.Duration;

/**
 * NatsConn — общие опции подключения для всех примеров clients/java.
 *
 * <p>Инкапсулирует то, что в продакшн-коде иначе пришлось бы копировать в
 * каждый пример: reconnect без ограничения попыток, коллбэки на переходы
 * состояния соединения ({@link ConnectionListener}) и на ошибки
 * ({@link ErrorListener}).
 *
 * <p>Адрес по умолчанию — {@code nats://127.0.0.1:4222}, нода {@code n1}
 * стенда {@code nats/02-cluster} (для {@code nats/01-jetstream}, один узел,
 * адрес совпадает). Переопределяется переменной окружения {@code NATS_URL} —
 * пригождается, когда демо запускается не с хоста напрямую, а из контейнера
 * в той же docker-сети, что и стенд (см. README.md).
 */
final class NatsConn {

    static final String DEFAULT_URL = System.getenv().getOrDefault("NATS_URL", Options.DEFAULT_URL);

    /**
     * Собирает {@link Options} с включённым автопереподключением и
     * логированием переходов состояния. {@code name} — видимое имя клиента
     * (появляется в мониторинге сервера, "connz"), удобно для отладки, когда
     * одновременно подключено несколько демо-процессов.
     */
    static Options options(String name) {
        return new Options.Builder()
                .server(DEFAULT_URL)
                .connectionName(name)

                // maxReconnects(-1) — не ограничивать число попыток
                // переподключения. По умолчанию клиент после исчерпания
                // лимита попыток считает соединение окончательно потерянным
                // и переходит в CLOSED — для долгоживущего сервиса это
                // обычно не то поведение, которое нужно.
                .maxReconnects(-1)
                .reconnectWait(Duration.ofSeconds(1))

                // connectionListener получает переходы состояния одним
                // методом: CONNECTED, DISCONNECTED, RECONNECTED, CLOSED и
                // ряд менее очевидных (RESUBSCRIBED, DISCOVERED_SERVERS,
                // LAME_DUCK). Без него процесс молчит о разрыве связи —
                // в проде это неприемлемо.
                .connectionListener((conn, type) ->
                        System.out.printf("[%s] connection event: %s%n", name, type))

                // errorListener — отдельный канал для ошибок протокола и
                // подписок: slow consumer, ошибки диспетчера, discard
                // сообщений и т.п. Комбинация connectionListener +
                // errorListener покрывает то, что в nats.go покрывают три
                // раздельных Handler'а (Disconnect/Reconnect/Closed) плюс
                // AsyncErrorHandler.
                .errorListener(new ErrorListener() {
                    @Override
                    public void errorOccurred(Connection conn, String error) {
                        System.out.printf("[%s] error: %s%n", name, error);
                    }

                    @Override
                    public void exceptionOccurred(Connection conn, Exception exp) {
                        System.out.printf("[%s] exception: %s%n", name, exp);
                    }
                })
                .build();
    }

    private NatsConn() {
    }
}
