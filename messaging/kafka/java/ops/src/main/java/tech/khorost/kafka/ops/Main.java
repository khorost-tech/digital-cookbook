package tech.khorost.kafka.ops;

import java.util.HashMap;
import java.util.Map;

/**
 * Точка входа стенда #6 ("эксплуатация: lag, тюнинг, quotas"). Зеркало
 * ../../go/ops/main.go — те же сценарии через флаги вида {@code --key=value}.
 *
 * <pre>
 * docker run --rm --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
 *   sh -c "mvn -q -pl ops -am package -DskipTests &amp;&amp; java -jar ops/target/ops.jar --scenario=seed --topic=demo-ops-lag --n=2000"
 * </pre>
 */
public final class Main {
    private Main() {
    }

    public static void main(String[] args) {
        Map<String, String> flags = parseFlags(args);

        String scenario = flags.getOrDefault("scenario", "");
        String topic = flags.getOrDefault("topic", "demo-ops");
        String group = flags.getOrDefault("group", "ops-group");
        int partitions = Integer.parseInt(flags.getOrDefault("partitions", "3"));
        short rf = Short.parseShort(flags.getOrDefault("rf", "3"));
        String minIsr = flags.getOrDefault("minisr", "2");
        boolean recreate = Boolean.parseBoolean(flags.getOrDefault("recreate", "false"));
        int n = Integer.parseInt(flags.getOrDefault("n", "1000"));
        int valueBytes = Integer.parseInt(flags.getOrDefault("value-bytes", "300"));
        String prefix = flags.getOrDefault("prefix", "seed");
        int rate = Integer.parseInt(flags.getOrDefault("rate", "20"));
        long durationMs = Long.parseLong(flags.getOrDefault("duration-ms", "30000"));
        int slowCount = Integer.parseInt(flags.getOrDefault("slow-count", "0"));
        long slowDelayMs = Long.parseLong(flags.getOrDefault("slow-delay-ms", "0"));
        long runForMs = Long.parseLong(flags.getOrDefault("run-for-ms", "60000"));
        long idleMs = Long.parseLong(flags.getOrDefault("idle-ms", "8000"));
        String clientId = flags.getOrDefault("client-id", "ops-client");
        int batchBytes = Integer.parseInt(flags.getOrDefault("batch-bytes", "16384"));
        int lingerMs = Integer.parseInt(flags.getOrDefault("linger-ms", "0"));
        String compression = flags.getOrDefault("compression", "none");
        int fetchMinBytes = Integer.parseInt(flags.getOrDefault("fetch-min-bytes", "1"));
        int fetchMaxWaitMs = Integer.parseInt(flags.getOrDefault("fetch-max-wait-ms", "500"));
        int maxPollRecords = Integer.parseInt(flags.getOrDefault("max-poll-records", "500"));
        String label = flags.getOrDefault("label", "");

        if (recreate) {
            Map<String, String> configs = new HashMap<>();
            if (!minIsr.isEmpty()) configs.put("min.insync.replicas", minIsr);
            Kafka.recreateTopic(topic, partitions, rf, configs);
            Kafka.waitTopicReady(topic, partitions, 30_000);
        }

        switch (scenario) {
            case "seed" -> Producer.seedFast(topic, n, valueBytes, prefix);
            case "seed-continuous" -> Producer.seedContinuous(topic, durationMs, rate, valueBytes);
            case "lag-consume" -> Consumer.lagConsume(topic, group, slowCount, slowDelayMs, runForMs, idleMs);
            case "tuning-producer" -> {
                String lbl = label.isEmpty() ? "batch-bytes=" + batchBytes + " linger-ms=" + lingerMs + " compression=" + compression : label;
                Producer.runTuningProducer(topic, n, valueBytes, batchBytes, lingerMs, compression, clientId, lbl);
            }
            case "tuning-consumer" -> {
                String lbl = label.isEmpty() ? "fetch-min-bytes=" + fetchMinBytes + " fetch-max-wait-ms=" + fetchMaxWaitMs + " max-poll-records=" + maxPollRecords : label;
                Consumer.tuningConsume(topic, group, fetchMinBytes, fetchMaxWaitMs, maxPollRecords, idleMs, lbl);
            }
            case "quota-produce" -> Producer.quotaProduce(topic, clientId, durationMs, valueBytes);
            default -> throw new IllegalArgumentException(
                    "неизвестный --scenario=" + scenario + " (seed|seed-continuous|lag-consume|tuning-producer|tuning-consumer|quota-produce)");
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
