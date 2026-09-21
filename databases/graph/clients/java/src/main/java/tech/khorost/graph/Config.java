package tech.khorost.graph;

/** Точки подключения к обоим движкам с дефолтами под локальный запуск. */
public record Config(String neo4jUri, String neo4jUser, String neo4jPass, String pgUrl) {

    public static Config fromEnv() {
        return new Config(
                env("NEO4J_URI", "bolt://localhost:7687"),
                env("NEO4J_USER", "neo4j"),
                env("NEO4J_PASS", "graphlab-pass"),
                // JDBC-URL; PG_JDBC переопределяет, иначе собираем из PG_DSN-подобных дефолтов
                env("PG_JDBC", "jdbc:postgresql://localhost:5432/graphlab?user=graph&password=graph"));
    }

    private static String env(String key, String def) {
        String v = System.getenv(key);
        return (v == null || v.isEmpty()) ? def : v;
    }

    public static GraphQueries create(String backend, Config cfg) {
        return switch (backend) {
            case "neo4j" -> new Neo4jQueries(cfg);
            case "age" -> new AgeQueries(cfg);
            case "baseline" -> new BaselineQueries(cfg);
            default -> throw new IllegalArgumentException(
                    "неизвестный backend '" + backend + "' (ожидается neo4j|age|baseline)");
        };
    }
}
