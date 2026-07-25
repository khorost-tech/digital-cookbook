package tech.khorost.dataaccess;

/**
 * Конфигурация подключения через env vars — единственный источник (общая конвенция стенда).
 * По умолчанию — хостовый доступ (localhost:5455, порт из docker/compose.yml). Внутри
 * контейнера на сети compose переопределяется на postgres:5432 (имя сервиса), см. run.sh.
 */
public final class Db {
    public static final String URL = env("JDBC_URL", "jdbc:postgresql://localhost:5455/jdd");
    public static final String USER = env("JDBC_USER", "jdd");
    public static final String PASSWORD = env("JDBC_PASSWORD", "jdd");

    private Db() {
    }

    private static String env(String key, String def) {
        String v = System.getenv(key);
        return (v == null || v.isEmpty()) ? def : v;
    }
}
