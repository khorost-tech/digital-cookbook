package tech.khorost.graph;

import java.util.List;

/**
 * Демо Java-клиента: гоняет пять графовых вопросов на выбранном угле (или на всех
 * трёх — GRAPH_BACKEND=all) и печатает результаты. Java-клиент существует для
 * паритета экосистем: он подтверждает, что те же ответы получаются и из JVM.
 */
public final class App {

    public static void main(String[] args) {
        String backend = System.getenv().getOrDefault("GRAPH_BACKEND", "all");
        Config cfg = Config.fromEnv();
        List<String> backends = "all".equals(backend)
                ? List.of("neo4j", "age", "baseline")
                : List.of(backend);

        for (String b : backends) {
            try (GraphQueries q = Config.create(b, cfg)) {
                runDemo(q);
            } catch (Exception e) {
                System.err.printf("[%s] ошибка: %s%n", b, e.getMessage());
            }
        }
    }

    private static void runDemo(GraphQueries q) {
        String b = q.backend();
        System.out.printf("%n=== backend: %s ===%n", b);

        List<Long> access = q.accessibleResources(1);
        report(b, "accessibleResources(user=1)", access);

        List<Long> cyc = q.cyclicServices(15);
        report(b, "cyclicServices(maxLen=15)", cyc);

        if (!cyc.isEmpty()) {
            List<Long> impact = q.impactOf(cyc.get(0), 10);
            report(b, "impactOf(service=" + cyc.get(0) + ", depth=10)", impact);
        }

        long dist = q.distance(1, 2, 6);
        System.out.printf("[%s] distance(1,2, maxLen=6) = %d%n", b, dist);

        List<Long> ring = q.ringMembers(4);
        report(b, "ringMembers(length=4)", ring);
    }

    private static void report(String backend, String name, List<Long> ids) {
        List<Long> sample = ids.size() > 5 ? ids.subList(0, 5) : ids;
        System.out.printf("[%s] %-34s → %d шт., пример %s%n", backend, name, ids.size(), sample);
    }

    private App() {
    }
}
