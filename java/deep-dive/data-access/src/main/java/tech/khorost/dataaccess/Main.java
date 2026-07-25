package tech.khorost.dataaccess;

import jakarta.persistence.EntityManagerFactory;

import java.util.List;
import java.util.Map;

/**
 * Оркестратор стенда "JDBC vs JPA/Hibernate vs jOOQ" (статья №6, java-deep-dive).
 * JDBC_URL/JDBC_USER/JDBC_PASSWORD — см. {@link Db}. Схема authors 1:N books.
 */
public class Main {
    public static void main(String[] args) throws Exception {
        System.out.println("=== data-access: JDBC vs JPA/Hibernate vs jOOQ + HikariCP ===");
        System.out.println("JDBC_URL=" + Db.URL);
        System.out.println();

        System.out.println("--- (0) Схема + сид ---");
        SchemaInit.run(HikariManager.dataSource());
        System.out.println();

        System.out.println("--- (1) Один и тот же доступ тремя способами ---");
        Map<String, List<String>> jdbcResult = JdbcDemo.authorsWithBooks(HikariManager.dataSource());
        Map<String, List<String>> jooqResult = JooqDemo.authorsWithBooks(HikariManager.dataSource());

        EntityManagerFactory emf = JpaDemo.createEmf();
        Map<String, List<String>> jpaResult;
        try {
            jpaResult = JpaDemo.authorsWithBooks(emf);

            boolean jdbcEqJooq = jdbcResult.equals(jooqResult);
            boolean jooqEqJpa = jooqResult.equals(jpaResult);
            System.out.printf("Эквивалентность результатов: JDBC==jOOQ: %s, jOOQ==JPA: %s%n",
                    jdbcEqJooq, jooqEqJpa);
            if (!jdbcEqJooq || !jooqEqJpa) {
                throw new IllegalStateException("Результаты трёх подходов РАСХОДЯТСЯ — стенд сломан");
            }
            System.out.println();

            System.out.println("--- (2) N+1 на JPA: воспроизведение и устранение ---");
            long n1Queries = JpaDemo.n1Demo(emf);
            long fetchJoinQueries = JpaDemo.fetchJoinDemo(emf);
            long entityGraphQueries = JpaDemo.entityGraphDemo(emf);

            System.out.printf(
                    "ИТОГ: N+1 = %d запросов (1 + %d авторов), JOIN FETCH = %d запрос(а), @EntityGraph = %d запрос(а)%n",
                    n1Queries, SchemaInit.AUTHORS, fetchJoinQueries, entityGraphQueries);

            if (n1Queries != SchemaInit.AUTHORS + 1) {
                throw new IllegalStateException(
                        "N+1 не воспроизвёлся как ожидалось: " + n1Queries + " != " + (SchemaInit.AUTHORS + 1));
            }
            if (fetchJoinQueries > 2 || entityGraphQueries > 2) {
                throw new IllegalStateException("Фикс не сработал: JOIN FETCH/EntityGraph дали больше 2 запросов");
            }
            System.out.println("АССЕРТ OK: N+1 воспроизведён и устранён.");
        } finally {
            emf.close();
        }
        System.out.println();

        System.out.println("--- (3) HikariCP: пул с метриками под нагрузкой ---");
        HikariManager.printMetricsUnderLoad();

        HikariManager.close();
        System.out.println();
        System.out.println("=== готово ===");
    }
}
