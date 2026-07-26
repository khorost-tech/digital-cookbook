package tech.khorost.kafka.logbasics;

/**
 * Точка входа стенда #1. Запуск (из контейнера на сети kafka-cookbook-net,
 * см. ../../../README.md):
 *
 * <pre>
 * docker run --rm --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
 *   sh -c "mvn -q -pl log-basics -am package -DskipTests &amp;&amp; java -jar log-basics/target/log-basics.jar"
 * </pre>
 */
public final class Main {
    private Main() {
    }

    public static void main(String[] args) throws Exception {
        LogBasics.run();
    }
}
