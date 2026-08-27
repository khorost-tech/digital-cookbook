package tech.khorost.across;

import java.util.List;

/** Граница нашего кода: хранилище заказов. Реализации — Postgres и фейк. */
public interface Store {
    void save(Order o);

    List<Order> byUser(String userId);
}
