package tech.khorost.dataaccess;

import jakarta.persistence.EntityGraph;
import jakarta.persistence.EntityManager;
import jakarta.persistence.EntityManagerFactory;
import jakarta.persistence.Persistence;
import jakarta.persistence.TypedQuery;
import org.hibernate.SessionFactory;
import org.hibernate.stat.Statistics;
import tech.khorost.dataaccess.model.Author;
import tech.khorost.dataaccess.model.Book;

import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;

/**
 * (2) JPA/Hibernate: EntityManager, объектная модель вместо ResultSet.
 * Показывает и болезнь (N+1 при наивном обходе lazy-коллекции), и лечение
 * (JOIN FETCH / EntityGraph). Счётчик запросов — {@code Statistics#getPrepareStatementCount()},
 * реальный счётчик подготовленных JDBC statement'ов внутри Hibernate, а не разбор
 * текстового лога (лог хорошо дополняет качественно, но не годится для точного счёта).
 */
public final class JpaDemo {

    private JpaDemo() {
    }

    public static EntityManagerFactory createEmf() {
        Map<String, Object> overrides = new HashMap<>();
        overrides.put("jakarta.persistence.jdbc.url", Db.URL);
        overrides.put("jakarta.persistence.jdbc.user", Db.USER);
        overrides.put("jakarta.persistence.jdbc.password", Db.PASSWORD);
        return Persistence.createEntityManagerFactory("dataaccess", overrides);
    }

    private static Statistics statistics(EntityManagerFactory emf) {
        return emf.unwrap(SessionFactory.class).getStatistics();
    }

    /**
     * Наивный обход: список из {@link SchemaInit#AUTHORS} авторов, затем для КАЖДОГО
     * отдельное обращение к лениво загружаемой коллекции books — Hibernate шлёт по
     * одному SELECT на автора. Итог: 1 (список авторов) + N (по книге на автора) запросов.
     */
    public static long n1Demo(EntityManagerFactory emf) {
        Statistics stats = statistics(emf);
        stats.clear();

        EntityManager em = emf.createEntityManager();
        try {
            TypedQuery<Author> q = em.createQuery("SELECT a FROM Author a ORDER BY a.id", Author.class);
            List<Author> authors = q.getResultList(); // запрос №1

            long totalBooks = 0;
            for (Author a : authors) {
                totalBooks += a.getBooks().size(); // лениво триггерит запрос №(1+i)
            }
            long queries = stats.getPrepareStatementCount();
            System.out.printf(
                    "JPA N+1: authors=%d, prepareStatementCount=%d (ожидание: 1 + %d = %d), totalBooks=%d%n",
                    authors.size(), queries, authors.size(), authors.size() + 1, totalBooks);
            return queries;
        } finally {
            em.close();
        }
    }

    /** Фикс №1: JOIN FETCH в JPQL — Hibernate тянет authors+books одним SQL. */
    public static long fetchJoinDemo(EntityManagerFactory emf) {
        Statistics stats = statistics(emf);
        stats.clear();

        EntityManager em = emf.createEntityManager();
        try {
            TypedQuery<Author> q = em.createQuery(
                    "SELECT DISTINCT a FROM Author a JOIN FETCH a.books ORDER BY a.id", Author.class);
            List<Author> authors = q.getResultList();

            long totalBooks = authors.stream().mapToLong(a -> a.getBooks().size()).sum();
            long queries = stats.getPrepareStatementCount();
            System.out.printf(
                    "JPA JOIN FETCH: authors=%d, prepareStatementCount=%d (ожидание: 1), totalBooks=%d%n",
                    authors.size(), queries, totalBooks);
            return queries;
        } finally {
            em.close();
        }
    }

    /** Фикс №2: EntityGraph (jakarta.persistence.fetchgraph hint) — тот же эффект, другой API. */
    public static long entityGraphDemo(EntityManagerFactory emf) {
        Statistics stats = statistics(emf);
        stats.clear();

        EntityManager em = emf.createEntityManager();
        try {
            EntityGraph<Author> graph = em.createEntityGraph(Author.class);
            graph.addAttributeNodes("books");

            TypedQuery<Author> q = em.createQuery(
                    "SELECT DISTINCT a FROM Author a ORDER BY a.id", Author.class);
            q.setHint("jakarta.persistence.fetchgraph", graph);
            List<Author> authors = q.getResultList();

            long totalBooks = authors.stream().mapToLong(a -> a.getBooks().size()).sum();
            long queries = stats.getPrepareStatementCount();
            System.out.printf(
                    "JPA @EntityGraph: authors=%d, prepareStatementCount=%d (ожидание: 1), totalBooks=%d%n",
                    authors.size(), queries, totalBooks);
            return queries;
        } finally {
            em.close();
        }
    }

    /** Тот же канонический результат (authorName -> список title), что и JDBC/jOOQ — для сверки. */
    public static Map<String, List<String>> authorsWithBooks(EntityManagerFactory emf) {
        EntityManager em = emf.createEntityManager();
        try {
            TypedQuery<Author> q = em.createQuery(
                    "SELECT DISTINCT a FROM Author a JOIN FETCH a.books ORDER BY a.id", Author.class);
            List<Author> authors = q.getResultList();

            Map<String, List<String>> result = new LinkedHashMap<>();
            for (Author a : authors) {
                List<String> titles = a.getBooks().stream()
                        .sorted((b1, b2) -> Long.compare(b1.getId(), b2.getId()))
                        .map(Book::getTitle)
                        .toList();
                result.put(a.getName(), titles);
            }
            return new TreeMap<>(result);
        } finally {
            em.close();
        }
    }
}
