package tech.khorost.graph;

import java.util.List;

/**
 * Общий контракт трёх backend-ов (Neo4j, Apache AGE, honest baseline на CTE).
 * Методы возвращают множества id или скаляр — подмножество, одинаково выразимое
 * во всех трёх движках. Порт того же контракта, что в Go-клиенте.
 */
public interface GraphQueries extends AutoCloseable {

    /** Ресурсы, доступные пользователю через прямые роли и членство в командах. */
    List<Long> accessibleResources(long userId);

    /** Сервисы, транзитивно зависящие от заданного, в пределах maxDepth. */
    List<Long> impactOf(long serviceId, int maxDepth);

    /** Сервисы на цикле DEPENDS_ON в пределах maxLen. */
    List<Long> cyclicServices(int maxLen);

    /** Длина кратчайшей цепочки COLLABORATES; -1, если пути нет в пределах maxLen. */
    long distance(long aId, long bId, int maxLen);

    /** Пользователи на кольце COLLABORATES ровно заданной длины. */
    List<Long> ringMembers(int length);

    String backend();

    @Override
    void close();
}
