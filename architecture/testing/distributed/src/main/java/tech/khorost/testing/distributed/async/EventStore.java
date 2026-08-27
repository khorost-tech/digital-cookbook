package tech.khorost.testing.distributed.async;

import java.util.Map;
import java.util.Optional;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Простое потокобезопасное хранилище-эффект: сюда консюмер кладёт результат
 * обработки события. Тест наблюдает за ним через Awaitility, а не через sleep.
 */
public final class EventStore {

    private final Map<String, String> byKey = new ConcurrentHashMap<>();

    public void put(String key, String value) {
        byKey.put(key, value);
    }

    public Optional<String> get(String key) {
        return Optional.ofNullable(byKey.get(key));
    }

    public int size() {
        return byKey.size();
    }
}
