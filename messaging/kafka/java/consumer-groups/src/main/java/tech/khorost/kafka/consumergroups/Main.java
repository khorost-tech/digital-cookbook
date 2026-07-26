package tech.khorost.kafka.consumergroups;

/**
 * Точка входа стенда #2. Режим — первый аргумент: rebalance | strategies |
 * static | commits | all (по умолчанию). Запуск (из контейнера на сети
 * kafka-cookbook-net, см. ../../../README.md):
 *
 * <pre>
 * docker run --rm --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
 *   sh -c "mvn -q -pl consumer-groups -am package -DskipTests &amp;&amp; java -jar consumer-groups/target/consumer-groups.jar all"
 * </pre>
 */
public final class Main {
    private Main() {
    }

    @FunctionalInterface
    private interface ThrowingRunnable {
        void run() throws Exception;
    }

    public static void main(String[] args) throws Exception {
        String scenario = args.length > 0 ? args[0] : "all";
        Kafka.ensureTopic();

        switch (scenario) {
            case "rebalance" -> run("rebalance (join/leave)", RebalanceDemo::run);
            case "strategies" -> run("strategies (range/roundrobin/sticky/cooperative-sticky)", StrategiesDemo::run);
            case "static" -> run("static membership", StaticMembershipDemo::run);
            case "commits" -> run("commit offset (auto vs manual)", CommitDemo::run);
            case "all" -> {
                run("rebalance (join/leave)", RebalanceDemo::run);
                run("strategies (range/roundrobin/sticky/cooperative-sticky)", StrategiesDemo::run);
                run("static membership", StaticMembershipDemo::run);
                run("commit offset (auto vs manual)", CommitDemo::run);
            }
            default -> throw new IllegalArgumentException(
                    "неизвестный сценарий: " + scenario + " (ожидается rebalance|strategies|static|commits|all)");
        }

        System.out.println("\n[assert] все сценарии пройдены");
    }

    private static void run(String name, ThrowingRunnable r) throws Exception {
        System.out.printf("%n================ сценарий: %s ================%n", name);
        Log.startClock();
        r.run();
    }
}
