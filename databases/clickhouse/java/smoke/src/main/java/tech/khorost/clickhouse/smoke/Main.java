package tech.khorost.clickhouse.smoke;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;

/**
 * Task 1 каркаса серии "ClickHouse: глубокое погружение": минимальная живая
 * проверка коннекта к ClickHouse из Java через clickhouse-jdbc (HTTP-интерфейс).
 *
 * <p>Запуск (из контейнера maven:3.9-eclipse-temurin-25 на сети
 * clickhouse-cookbook-net, см. ../../../README.md):
 *
 * <pre>
 * docker run --rm --network clickhouse-cookbook-net -v "$(pwd)/java:/app" -w /app/smoke \
 *   maven:3.9-eclipse-temurin-25 sh -c "mvn -q -f ../pom.xml -pl smoke -am package -DskipTests &amp;&amp; \
 *   java -cp target/smoke.jar tech.khorost.clickhouse.smoke.Main jdbc:ch://clickhouse:8123/demo"
 * </pre>
 */
public final class Main {

    private Main() {
    }

    public static void main(String[] args) throws Exception {
        String url = args.length > 0 ? args[0] : "jdbc:ch://clickhouse:8123/demo";

        try (Connection conn = DriverManager.getConnection(url, "default", "");
             Statement stmt = conn.createStatement()) {

            try (ResultSet rs = stmt.executeQuery("SELECT 1")) {
                if (!rs.next()) {
                    throw new IllegalStateException("SELECT 1 returned no rows");
                }
                int one = rs.getInt(1);
                if (one != 1) {
                    throw new IllegalStateException("SELECT 1 returned unexpected value: " + one);
                }

                try (ResultSet rsVersion = stmt.executeQuery("SELECT version()")) {
                    rsVersion.next();
                    String version = rsVersion.getString(1);
                    System.out.printf(
                            "smoke OK: connected to %s, SELECT 1 = %d, server version = %s%n",
                            url, one, version);
                }
            }
        }
    }
}
