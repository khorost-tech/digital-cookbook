package tech.khorost.kafka.storage;

import java.time.Duration;
import java.util.Arrays;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * Точка входа стенда #4 ("хранение: retention, log compaction, компрессия").
 * Зеркало ../../go/storage/main.go — те же фазы через флаги вида
 * {@code --key=value}, потому что часть сценариев требует, чтобы host-скрипт
 * (../../ops/inspect-segments.sh) инспектировал реальные файлы сегментов и
 * менял dynamic broker-конфиги МЕЖДУ вызовами фаз.
 *
 * <pre>
 * docker run --rm --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
 *   sh -c "mvn -q -pl storage -am package -DskipTests &amp;&amp; java -jar storage/target/storage.jar --scenario=offsets --topic=demo-storage-retention"
 * </pre>
 */
public final class Main {
    private Main() {
    }

    public static void main(String[] args) {
        Map<String, String> flags = parseFlags(args);

        String scenario = flags.getOrDefault("scenario", "");
        String topicFlag = flags.get("topic");
        int n = Integer.parseInt(flags.getOrDefault("n", "150"));
        int padBytes = Integer.parseInt(flags.getOrDefault("pad-bytes", "120"));
        int retentionMs = Integer.parseInt(flags.getOrDefault("retention-ms", "6000"));
        // 1048576 (1MiB) — минимум, разрешённый брокером, см. Go-версию за живое подтверждение (INVALID_CONFIG на меньших значениях).
        long segmentBytes = Long.parseLong(flags.getOrDefault("segment-bytes", "1048576"));
        List<String> keys = Arrays.asList(flags.getOrDefault("keys",
                "biz-key-1,biz-key-2,biz-key-3,biz-key-4,biz-key-5,biz-key-6,biz-key-7,biz-key-8").split(","));
        int rounds = Integer.parseInt(flags.getOrDefault("rounds", "4"));
        List<String> tombstoneKeys = Arrays.asList(flags.getOrDefault("tombstone-keys", "biz-key-7,biz-key-8").split(","));
        int fillerN = Integer.parseInt(flags.getOrDefault("filler-n", "24"));
        int fillerStart = Integer.parseInt(flags.getOrDefault("filler-start", "0"));
        // ops/inspect-segments.sh передаёт ОДНО значение флага --idle для обоих
        // клиентов; Go-версия использует flag.Duration (требует суффикс, "3s"),
        // поэтому канонический формат в скрипте — с суффиксом "s". Здесь просто
        // отбрасываем суффикс, если он есть, — принимаем и "3", и "3s".
        int idleSec = Integer.parseInt(flags.getOrDefault("idle", "5").replaceAll("[sS]$", ""));
        String label = flags.getOrDefault("label", "");
        boolean assertFlag = Boolean.parseBoolean(flags.getOrDefault("assert", "false"));
        long minEarliestGt = Long.parseLong(flags.getOrDefault("min-earliest-gt", "-1"));
        String codec = flags.getOrDefault("codec", "none");
        int partitions = Integer.parseInt(flags.getOrDefault("partitions", "1"));
        short rf = Short.parseShort(flags.getOrDefault("rf", "3"));

        switch (scenario) {
            case "retention-setup" -> {
                String t = defTopic(topicFlag, "demo-storage-retention");
                Map<String, String> configs = new HashMap<>();
                configs.put("cleanup.policy", "delete");
                configs.put("segment.bytes", Long.toString(segmentBytes));
                configs.put("segment.ms", "3600000"); // намеренно большой — roll управляется ТОЛЬКО размером
                configs.put("retention.ms", Integer.toString(retentionMs));
                configs.put("retention.bytes", "-1");
                Kafka.recreateTopic(t, partitions, rf, configs);
                Kafka.waitForLeader(t, 30_000);
            }
            case "retention-produce" -> {
                String t = defTopic(topicFlag, "demo-storage-retention");
                Storage.produceUnkeyedSequential(t, n, padBytes);
            }
            case "offsets" -> {
                String t = defTopic(topicFlag, "demo-storage-retention");
                Kafka.Offsets o = Kafka.reportOffsets(t, label);
                if (minEarliestGt >= 0) {
                    if (o.earliest() <= minEarliestGt) {
                        throw new AssertionError("[assert] FAIL: earliest=" + o.earliest() + " НЕ больше " + minEarliestGt + " — retention не сдвинул earliest offset");
                    }
                    System.out.printf("[assert] OK: earliest=%d > %d — retention сдвинул earliest offset вперёд%n", o.earliest(), minEarliestGt);
                }
            }
            case "compact-setup" -> {
                String t = defTopic(topicFlag, "demo-storage-compact");
                Map<String, String> configs = new HashMap<>();
                configs.put("cleanup.policy", "compact");
                configs.put("segment.bytes", Long.toString(segmentBytes));
                configs.put("segment.ms", "3600000");
                configs.put("min.cleanable.dirty.ratio", "0.01");
                configs.put("delete.retention.ms", "100");
                configs.put("min.compaction.lag.ms", "0");
                configs.put("max.compaction.lag.ms", "8000");
                Kafka.recreateTopic(t, partitions, rf, configs);
                Kafka.waitForLeader(t, 30_000);
            }
            case "compact-produce" -> {
                String t = defTopic(topicFlag, "demo-storage-compact");
                Storage.produceKeyedUpdates(t, keys, rounds, padBytes);
                Storage.produceTombstones(t, tombstoneKeys);
                Storage.produceFiller(t, fillerN, padBytes, fillerStart);
            }
            case "compact-produce-business" -> {
                String t = defTopic(topicFlag, "demo-storage-compact");
                Storage.produceKeyedUpdates(t, keys, rounds, padBytes);
                Storage.produceTombstones(t, tombstoneKeys);
            }
            case "compact-produce-filler" -> {
                String t = defTopic(topicFlag, "demo-storage-compact");
                Storage.produceFiller(t, fillerN, padBytes, fillerStart);
            }
            case "compact-consume" -> {
                String t = defTopic(topicFlag, "demo-storage-compact");
                List<Storage.Recv> recv = Storage.consumeAllFromStart(t, Duration.ofSeconds(idleSec));
                Storage.printCompactState(label, recv);
                if (assertFlag) {
                    Storage.assertCompacted(recv, keys, tombstoneKeys, rounds - 1);
                }
            }
            case "compress-setup" -> {
                String t = defTopic(topicFlag, "demo-storage-compress-" + codec);
                Map<String, String> configs = Map.of("cleanup.policy", "delete");
                Kafka.recreateTopic(t, partitions, rf, configs);
                Kafka.waitForLeader(t, 30_000);
            }
            case "compress-produce" -> {
                String t = defTopic(topicFlag, "demo-storage-compress-" + codec);
                Storage.produceBatchedAsync(t, n, padBytes, codec);
            }
            default -> throw new IllegalArgumentException(
                    "неизвестный --scenario=" + scenario + " (retention-setup|retention-produce|offsets|compact-setup|compact-produce|compact-produce-business|compact-produce-filler|compact-consume|compress-setup|compress-produce)");
        }
    }

    private static String defTopic(String flagVal, String def) {
        return (flagVal == null || flagVal.isEmpty()) ? def : flagVal;
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
