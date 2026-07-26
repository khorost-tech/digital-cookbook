package tech.khorost.kafka.replication;

import java.util.HashMap;
import java.util.Map;

/**
 * Точка входа стенда #3 ("репликация и надёжность"). Зеркало
 * ../../go/replication/main.go — те же фазы через флаги вида
 * {@code --key=value}, потому что часть сценариев требует, чтобы host-скрипт
 * (../../ops/broker-kill.sh) убивал/поднимал брокеров МЕЖДУ вызовами фаз.
 *
 * <pre>
 * docker run --rm --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
 *   sh -c "mvn -q -pl replication -am package -DskipTests &amp;&amp; java -jar replication/target/replication.jar --scenario=describe --topic=demo-repl"
 * </pre>
 */
public final class Main {
    private Main() {
    }

    public static void main(String[] args) {
        Map<String, String> flags = parseFlags(args);

        String scenario = flags.getOrDefault("scenario", "");
        String topic = flags.getOrDefault("topic", "demo-repl");
        int partitions = Integer.parseInt(flags.getOrDefault("partitions", "1"));
        short rf = Short.parseShort(flags.getOrDefault("rf", "3"));
        String minIsr = flags.getOrDefault("minisr", "2");
        String unclean = flags.getOrDefault("unclean", "");
        int n = Integer.parseInt(flags.getOrDefault("n", "20"));
        String acks = flags.getOrDefault("acks", "all");
        boolean idempotent = Boolean.parseBoolean(flags.getOrDefault("idempotent", "true"));
        int retries = Integer.parseInt(flags.getOrDefault("retries", "-1"));
        int reqTimeoutMs = Integer.parseInt(flags.getOrDefault("req-timeout-ms", "10000"));
        String prefix = flags.getOrDefault("prefix", "msg");
        int delayMs = Integer.parseInt(flags.getOrDefault("delay-ms", "0"));
        int expect = Integer.parseInt(flags.getOrDefault("expect", "0"));
        int idleMs = Integer.parseInt(flags.getOrDefault("idle-ms", "5000"));
        boolean soft = Boolean.parseBoolean(flags.getOrDefault("soft", "false"));
        boolean checkdup = Boolean.parseBoolean(flags.getOrDefault("checkdup", "false"));
        boolean printIndices = Boolean.parseBoolean(flags.getOrDefault("print-indices", "false"));

        switch (scenario) {
            case "setup" -> {
                Map<String, String> configs = new HashMap<>();
                if (!minIsr.isEmpty()) configs.put("min.insync.replicas", minIsr);
                if (!unclean.isEmpty()) configs.put("unclean.leader.election.enable", unclean);
                Kafka.recreateTopic(topic, partitions, rf, configs);
                Kafka.printPartitionState("после setup", Kafka.waitForLeader(topic, 30_000));
            }
            case "describe" -> Kafka.printPartitionState("текущее состояние", Kafka.describePartition(topic));
            case "set-unclean" -> Kafka.alterTopicConfig(topic, "unclean.leader.election.enable", unclean);
            case "acks-bench" -> Replication.runAcksBench(topic);
            case "produce" -> Replication.runProduce(topic, n, acks, idempotent, retries, reqTimeoutMs, prefix, delayMs);
            case "verify" -> Replication.runVerify(topic, expect, idleMs, soft, checkdup, printIndices);
            case "minisr-produce" -> Replication.runMinISRProduce(topic, acks);
            default -> throw new IllegalArgumentException(
                    "неизвестный --scenario=" + scenario + " (setup|describe|set-unclean|acks-bench|produce|verify|minisr-produce)");
        }
    }

    private static Map<String, String> parseFlags(String[] args) {
        Map<String, String> out = new HashMap<>();
        for (String a : args) {
            if (!a.startsWith("--")) continue;
            String body = a.substring(2);
            int eq = body.indexOf('=');
            if (eq < 0) {
                out.put(body, "true");
            } else {
                out.put(body.substring(0, eq), body.substring(eq + 1));
            }
        }
        return out;
    }
}
