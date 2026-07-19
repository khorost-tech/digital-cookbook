// Java (Hibernate/JPA) — ORM-эффекты поверх PG-стенда «Индексы в базах данных»
// (events, 2M строк, idx_events_user). Companion к статье 5 серии «Индексы в базах данных».
//
//   cd db-indexes/postgres && docker compose up -d
//   ./run.sh sql/00-schema.sql >/dev/null && ./run.sh sql/01-scan.sql >/dev/null
//   cd ../orm/java && mvn -q compile exec:java
//
// hibernate.show_sql=true печатает каждый сгенерированный SQL — по логу видно N+1
// (N однотипных SELECT count(*)) против одного JPQL с IN + GROUP BY.

import jakarta.persistence.EntityManager;
import jakarta.persistence.EntityManagerFactory;
import jakarta.persistence.Persistence;
import jakarta.persistence.Query;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

public class Main {
    static String env(String k, String def) {
        String v = System.getenv(k);
        return v == null || v.isEmpty() ? def : v;
    }

    public static void main(String[] args) {
        // JDBC_URL позволяет переопределить persistence.xml (localhost:5433 по умолчанию),
        // если код гоняется внутри docker-сети стенда (postgres:5432).
        Map<String, Object> overrides = new HashMap<>();
        String jdbcUrl = System.getenv("JDBC_URL");
        if (jdbcUrl != null && !jdbcUrl.isEmpty()) {
            overrides.put("jakarta.persistence.jdbc.url", jdbcUrl);
        }
        String jdbcUser = System.getenv("JDBC_USER");
        if (jdbcUser != null && !jdbcUser.isEmpty()) {
            overrides.put("jakarta.persistence.jdbc.user", jdbcUser);
        }
        String jdbcPassword = System.getenv("JDBC_PASSWORD");
        if (jdbcPassword != null && !jdbcPassword.isEmpty()) {
            overrides.put("jakarta.persistence.jdbc.password", jdbcPassword);
        }

        EntityManagerFactory emf = Persistence.createEntityManagerFactory("idxdemo", overrides);
        try {
            EntityManager em = emf.createEntityManager();
            try {
                System.out.println("=== (1) N+1: 20 пользователей, по каждому отдельный SELECT count(*) ===");
                n1Demo(em);

                System.out.println();
                System.out.println("=== (2) Батч: один JPQL с IN + GROUP BY — 1 запрос ===");
                batchDemo(em);
            } finally {
                em.close();
            }
        } finally {
            emf.close();
        }
    }

    // Типичный ORM-паттерн: сначала список "родителей" (пользователей), затем в цикле —
    // по каждому отдельный запрос агрегата (эквивалент lazy-коллекции orders.size()).
    // В логе Hibernate это видно как N однотипных `select count(*) ... where user_id=?`.
    @SuppressWarnings("unchecked")
    static void n1Demo(EntityManager em) {
        Query idsQuery = em.createNativeQuery("SELECT DISTINCT user_id FROM events ORDER BY user_id LIMIT 20");
        List<Number> ids = idsQuery.getResultList();

        int n1 = 0;
        for (Number id : ids) {
            long userId = id.longValue();
            Query countQuery = em.createQuery(
                    "select count(e) from Event e where e.userId = :uid");
            countQuery.setParameter("uid", userId);
            long c = (long) countQuery.getSingleResult();
            n1++;
        }
        System.out.printf("N+1: выполнено %d отдельных запросов (по одному на user_id)%n", n1);
    }

    // Правильно: один JPQL с IN(...) и GROUP BY — Hibernate генерирует 1 SQL-запрос,
    // возвращающий агрегат сразу по всем user_id.
    @SuppressWarnings("unchecked")
    static void batchDemo(EntityManager em) {
        Query idsQuery = em.createNativeQuery("SELECT DISTINCT user_id FROM events ORDER BY user_id LIMIT 20");
        List<Number> ids = idsQuery.getResultList();
        List<Long> userIds = ids.stream().map(Number::longValue).toList();

        Query batchQuery = em.createQuery(
                "select e.userId, count(e) from Event e where e.userId in :uids group by e.userId");
        batchQuery.setParameter("uids", userIds);
        List<Object[]> rows = batchQuery.getResultList();

        long total = rows.stream().mapToLong(r -> (Long) r[1]).sum();
        System.out.printf("Батч: 1 запрос, %d групп, total=%d (сумма по тем же %d user_id)%n",
                rows.size(), total, userIds.size());
    }
}
