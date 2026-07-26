package tech.khorost.kafka.eos;

import java.util.HashMap;
import java.util.Map;

/**
 * Точка входа стенда #5 ("exactly-once и транзакции"). Зеркало
 * ../../go/eos/main.go — те же фазы через флаги вида {@code --key=value},
 * потому что consume-process-produce сценарий требует, чтобы host-скрипт
 * (../../ops/eos-kill.sh) убивал JVM МЕЖДУ produce и commitTransaction.
 *
 * <pre>
 * docker run --rm --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
 *   sh -c "mvn -q -pl eos -am package -DskipTests &amp;&amp; java -jar eos/target/eos.jar --scenario=txn-run"
 * </pre>
 */
public final class Main {
    private Main() {
    }

    public static void main(String[] args) {
        Map<String, String> flags = parseFlags(args);

        String scenario = flags.getOrDefault("scenario", "");
        String topic = flags.getOrDefault("topic", "demo-eos-txn");
        String inputTopic = flags.getOrDefault("input-topic", "demo-eos-cpp-input");
        String outputTopic = flags.getOrDefault("output-topic", "demo-eos-cpp-output");
        String group = flags.getOrDefault("group", "eos-cpp-group");
        String txnId = flags.getOrDefault("txn-id", "cookbook-eos-cpp-producer-java");
        int batchSize = Integer.parseInt(flags.getOrDefault("batch-size", "5"));
        int n = Integer.parseInt(flags.getOrDefault("n", "10"));
        String prefix = flags.getOrDefault("prefix", "cpp");
        long pauseMs = parseDurationMs(flags.getOrDefault("pause", "0s"));
        String readyMarker = flags.getOrDefault("ready-marker", "READY-TO-COMMIT");
        int partitions = Integer.parseInt(flags.getOrDefault("partitions", "3"));
        short rf = Short.parseShort(flags.getOrDefault("rf", "3"));
        int expectCommitted = Integer.parseInt(flags.getOrDefault("expect-committed", "0"));
        int expectPhysical = Integer.parseInt(flags.getOrDefault("expect-physical", "0"));
        String label = flags.getOrDefault("label", "");
        long expectOutput = Long.parseLong(flags.getOrDefault("expect-output-committed", "0"));
        long expectGroupOffset = Long.parseLong(flags.getOrDefault("expect-group-offset", "0"));

        switch (scenario) {
            case "txn-setup" -> Kafka.recreateTopic(topic, partitions, rf);
            case "txn-run" -> Txn.runTxnBatches(topic, batchSize);
            case "txn-verify" -> Txn.runTxnVerify(topic, expectCommitted, expectPhysical);
            case "cpp-setup" -> {
                Kafka.recreateTopic(inputTopic, partitions, rf);
                Kafka.recreateTopic(outputTopic, partitions, rf);
                Kafka.deleteGroup(group);
            }
            case "cpp-seed" -> Cpp.cppSeed(inputTopic, n, prefix);
            case "cpp-attempt" -> Cpp.cppAttempt(group, txnId, inputTopic, outputTopic, n, pauseMs, readyMarker);
            case "cpp-verify" -> Cpp.cppVerify(group, inputTopic, outputTopic, label, expectOutput, expectGroupOffset);
            default -> throw new IllegalArgumentException(
                    "неизвестный --scenario=" + scenario + " (txn-setup|txn-run|txn-verify|cpp-setup|cpp-seed|cpp-attempt|cpp-verify)");
        }
    }

    /** Парсит Go-style Duration-строки, которые реально приходят из ops/eos-kill.sh: "0", "60s". */
    private static long parseDurationMs(String s) {
        if (s.equals("0") || s.isEmpty()) return 0;
        if (s.endsWith("ms")) return Long.parseLong(s.substring(0, s.length() - 2));
        if (s.endsWith("s")) return Long.parseLong(s.substring(0, s.length() - 1)) * 1000;
        return Long.parseLong(s);
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
