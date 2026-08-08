package tech.khorost.scylla;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.CqlSessionBuilder;
import com.datastax.oss.driver.api.core.DefaultConsistencyLevel;
import com.datastax.oss.driver.api.core.config.DefaultDriverOption;
import com.datastax.oss.driver.api.core.config.DriverConfigLoader;
import com.datastax.oss.driver.api.core.cql.BoundStatement;
import com.datastax.oss.driver.api.core.cql.PreparedStatement;
import com.datastax.oss.driver.api.core.cql.ResultSet;
import com.datastax.oss.driver.api.core.cql.Row;

import java.net.InetSocketAddress;
import java.time.LocalDate;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Locale;
import java.util.Random;
import java.util.stream.Collectors;

/**
 * Стенд #6 (Java-зеркало Go-бенча {@code ../go}) серии "ScyllaDB: глубокое
 * погружение": shard-awareness ВКЛ vs ВЫКЛ ОДНОГО драйвера
 * {@code com.scylladb:java-driver-core} (shard-aware форк DataStax OSS Java
 * driver 4.x), не двух разных драйверов.
 *
 * <p><b>ПЕРЕРАБОТКА ДИЗАЙНА</b> (см. README «Стенд #6» и task-7-brief.md):
 * исходная идея — apstream {@code com.datastax.oss:java-driver-core} и форк
 * {@code com.scylladb:java-driver-core} на одном classpath под разными
 * координатами Maven — физически невозможна. Живая проверка (2026-07-11,
 * {@code unzip -l java-driver-core-4.19.2.0.jar}) подтвердила: ЕДИНСТВЕННЫЙ
 * top-level Java-пакет внутри jar форка — {@code com/datastax/...}, ни
 * одного класса под {@code com/scylladb/...}. Форк ScyllaDB — это патченный
 * апстримный драйвер, публикуемый под своими Maven-координатами, но с ТЕМИ
 * ЖЕ именами классов/пакетов ({@code com.datastax.oss.driver.api.core.CqlSession}
 * и т.д.). Положить оба jar на один classpath — гарантированный конфликт
 * дублирующихся классов (какой jar реально загрузится для
 * {@code CqlSession} — зависит от порядка classpath, не детерминировано).
 * Та же причина, по которой у {@code datastax-java-driver} остаётся ИМЕННО
 * ЭТО имя корня конфигурации даже в форке (см. {@code reference.conf} внутри
 * jar форка: {@code datastax-java-driver { ... }}) — конфиг-неймспейс тоже
 * общий, апстримный и форк-конфиги слились бы через Typesafe Config resource
 * merging при попытке положить оба на classpath.
 *
 * <p>Поэтому контраст — ВНУТРИ одного драйвера, через реальный конфиг-
 * тумблер {@code advanced.connection.advanced-shard-awareness.enabled}
 * (найден живьём в распакованном {@code reference.conf} форка,
 * {@code datastax-java-driver.advanced.connection.advanced-shard-awareness}
 * — секция ScyllaDB добавила ПОВЕРХ апстримного дерева опций, "Overridable
 * in a profile: no" — тумблер общий на сессию, поэтому ВКЛ/ВЫКЛ — это ДВЕ
 * разные {@link CqlSession}, не два профиля одной сессии).
 *
 * <p><b>ПРАВКА РЕВЬЮ (2026-07-11).</b> Предыдущая редакция этого javadoc
 * утверждала, что у ключа "нет typed-константы в
 * {@code TypedDriverOption}/{@code DefaultDriverOption}". Это было ложное
 * утверждение — живая дизассемблировка {@code java-driver-core-4.19.2.0.jar}
 * (javap по распакованным {@code .class}) показала, что typed-константа ЕСТЬ
 * в ОБОИХ местах: {@link DefaultDriverOption#CONNECTION_ADVANCED_SHARD_AWARENESS_ENABLED}
 * и одноимённая {@code TypedDriverOption<Boolean>} — тот же путь
 * {@code advanced.connection.advanced-shard-awareness.enabled}. Переключение
 * теперь идёт через типобезопасный программный билдер
 * {@link DriverConfigLoader#programmaticBuilder()}{@code
 * .withBoolean(DefaultDriverOption.CONNECTION_ADVANCED_SHARD_AWARENESS_ENABLED,
 * enabled).build()} — без сырых HOCON-строк и без риска опечатки в пути.
 *
 * <p>Нагрузка идентична Go-бенчу ({@code ../go/main.go}): N точечных чтений
 * случайных партиций {@code telemetry.readings} ({@code WHERE device_id=?
 * AND day=? LIMIT 1}, полный partition key good-модели, Стенд #1),
 * параметры датасета Task 1 (devices=500, days=14, refDate=2026-07-01),
 * ОДИНАКОВЫЙ consistency level {@code QUORUM} (Go-бенч: {@code
 * cluster.Consistency = gocql.Quorum}; этот бенч ставит
 * {@code DefaultConsistencyLevel.QUORUM} на каждый {@link BoundStatement} —
 * без этого драйвер молча наследовал бы дефолт {@code LOCAL_ONE}, и
 * заявление "нагрузка идентична" было бы неточным). Только чтение,
 * {@code readings} не мутируется.
 */
public final class DriversBench {

    private static final int NUM_DEVICES = 500;
    private static final int NUM_DAYS = 14;
    private static final LocalDate REF_DATE = LocalDate.of(2026, 7, 1);
    private static final String LOCAL_DC = "datacenter1";
    private static final String POINT_READ_CQL =
            "SELECT event_time, value FROM readings WHERE device_id = ? AND day = ? LIMIT 1";

    private DriversBench() {
    }

    public static void main(String[] args) throws Exception {
        List<String> hosts = parseHosts(envOr("SCYLLA_HOSTS", "127.0.0.1:9042"));
        int n = intArg(args, "-n", 20000);
        int warmup = intArg(args, "-warmup", 500);
        long seed = longArg(args, "-seed", 42);
        // -mode on|off  -- ИЗОЛИРОВАННЫЙ прогон одного режима в СВОЁМ процессе
        //   (java -jar ... -mode on, отдельно java -jar ... -mode off) -- как
        //   у Go-бенча (../go/main.go, -mode aware|naive, два docker run).
        //   Это КАНОНИЧЕСКИЙ способ получить честное число для README.
        // -mode both (default)  -- ОБА режима в ОДНОМ процессе подряд, как в
        //   первой версии этого бенча. Оставлено намеренно: сравнение both
        //   vs on/off живьём вскрыло JIT/JVM-прогрев-bias (см. README «Стенд
        //   #6», секция «Честная находка про порядок прогонов в Java») --
        //   второй прогон внутри ОДНОГО процесса систематически быстрее
        //   ПЕРВОГО независимо от режима (JIT дозревает, коннекшны/буферы
        //   уже прогреты), поэтому both НЕ используется для итоговых чисел
        //   README -- только как демонстрация самой находки. -swap меняет
        //   порядок ON/OFF местами внутри both -- тот же диагностический смысл.
        String mode = strArg(args, "-mode", "both");
        boolean swap = boolArg(args, "-swap");

        System.out.println("=== Стенд #6: drivers (Java) — shard-awareness ВКЛ vs ВЫКЛ ===");
        System.out.println("driver: com.scylladb:java-driver-core 4.19.2.0 (fork, ОДИН драйвер, ОДИН classpath)");
        System.out.println("hosts: " + hosts + ", keyspace=telemetry, local-dc=" + LOCAL_DC + ", consistency=QUORUM (== Go bench)");
        System.out.println("mode: " + mode + (mode.equals("both") ? (swap ? " (order: OFF затем ON, -swap)" : " (order: ON затем OFF)") : ""));
        System.out.println();

        switch (mode) {
            case "on": {
                Result on = runOnce("ON  (дефолт форка, advanced-shard-awareness.enabled=true)",
                        hosts, buildConfigLoader(true), n, warmup, seed);
                printResult(on);
                if (on.errors > 0) {
                    System.exit(1);
                }
                return;
            }
            case "off": {
                Result off = runOnce("OFF (advanced.connection.advanced-shard-awareness.enabled=false)",
                        hosts, buildConfigLoader(false), n, warmup, seed);
                printResult(off);
                if (off.errors > 0) {
                    System.exit(1);
                }
                return;
            }
            case "both":
                break;
            default:
                System.err.println("unknown -mode " + mode + " (expected on|off|both)");
                System.exit(2);
                return;
        }

        Result on, off;
        if (!swap) {
            on = runOnce("ON  (дефолт форка, advanced-shard-awareness.enabled=true)",
                    hosts, buildConfigLoader(true), n, warmup, seed);
            printResult(on);
            off = runOnce("OFF (advanced.connection.advanced-shard-awareness.enabled=false)",
                    hosts, buildConfigLoader(false), n, warmup, seed);
            printResult(off);
        } else {
            off = runOnce("OFF (advanced.connection.advanced-shard-awareness.enabled=false)",
                    hosts, buildConfigLoader(false), n, warmup, seed);
            printResult(off);
            on = runOnce("ON  (дефолт форка, advanced-shard-awareness.enabled=true)",
                    hosts, buildConfigLoader(true), n, warmup, seed);
            printResult(on);
        }

        System.out.println("-- Сравнение ON vs OFF (ОДИН процесс, СМ. ОГОВОРКУ ВЫШЕ про -mode both) --");
        System.out.printf(Locale.ROOT, "%-6s %12s %10s %10s%n", "mode", "throughput", "p50", "p99");
        System.out.printf(Locale.ROOT, "%-6s %9.1f r/s %8dus %8dus%n", "ON", on.throughputRowsPerSec, on.p50Micros, on.p99Micros);
        System.out.printf(Locale.ROOT, "%-6s %9.1f r/s %8dus %8dus%n", "OFF", off.throughputRowsPerSec, off.p50Micros, off.p99Micros);
        double ratio = off.throughputRowsPerSec > 0 ? on.throughputRowsPerSec / off.throughputRowsPerSec : Double.NaN;
        System.out.printf(Locale.ROOT, "ratio throughput[ON]/throughput[OFF] = %.4f (В ОДНОМ процессе -- см. оговорку про order-bias, для честного числа см. -mode on / -mode off отдельными процессами)%n%n", ratio);

        if (on.errors > 0 || off.errors > 0) {
            System.out.printf(Locale.ROOT, "%nFAIL: ошибки чтения (ON=%d, OFF=%d)%n", on.errors, off.errors);
            System.exit(1);
        }
    }

    private static DriverConfigLoader buildConfigLoader(boolean shardAwarenessEnabled) {
        // Typed-константа ЕСТЬ (DefaultDriverOption.CONNECTION_ADVANCED_SHARD_AWARENESS_ENABLED,
        // подтверждено javap по java-driver-core-4.19.2.0.jar, см. class javadoc) —
        // программный builder вместо сырого HOCON-пути. Дефолт форка = true; явный
        // override здесь делает намерение видимым в коде, а не полагается на молчаливый дефолт.
        return DriverConfigLoader.programmaticBuilder()
                .withBoolean(DefaultDriverOption.CONNECTION_ADVANCED_SHARD_AWARENESS_ENABLED, shardAwarenessEnabled)
                .build();
    }

    private static Result runOnce(String label, List<String> hosts, DriverConfigLoader loader,
                                   int n, int warmup, long seed) {
        System.out.println("-- " + label + " --");
        CqlSessionBuilder builder = CqlSession.builder()
                .withConfigLoader(loader)
                .withLocalDatacenter(LOCAL_DC)
                .withKeyspace("telemetry");
        for (String h : hosts) {
            builder.addContactPoint(toSocketAddress(h));
        }

        try (CqlSession session = builder.build()) {
            PreparedStatement ps = session.prepare(POINT_READ_CQL);
            Random r = new Random(seed);

            if (warmup > 0) {
                System.out.println("прогрев: " + warmup + " чтений (не в замере)...");
                int werrs = 0;
                for (int i = 0; i < warmup; i++) {
                    if (!readOnce(session, ps, r)) {
                        werrs++;
                    }
                }
                if (werrs > 0) {
                    System.out.println("  (прогрев: " + werrs + " ошибок из " + warmup + " -- не критично)");
                }
            }

            System.out.println("замер: " + n + " точечных чтений telemetry.readings...");
            List<Long> durationsNanos = new ArrayList<>(n);
            int errs = 0;
            long t0 = System.nanoTime();
            for (int i = 0; i < n; i++) {
                long qt0 = System.nanoTime();
                boolean ok = readOnce(session, ps, r);
                long qd = System.nanoTime() - qt0;
                if (ok) {
                    durationsNanos.add(qd);
                } else {
                    errs++;
                }
            }
            long elapsedNanos = System.nanoTime() - t0;

            int success = durationsNanos.size();
            double elapsedSec = elapsedNanos / 1_000_000_000.0;
            double throughput = elapsedSec > 0 ? success / elapsedSec : 0.0;
            long[] sorted = durationsNanos.stream().mapToLong(Long::longValue).sorted().toArray();
            long p50 = percentileMicros(sorted, 0.50);
            long p99 = percentileMicros(sorted, 0.99);

            return new Result(label, n, success, errs, elapsedNanos / 1_000_000, throughput, p50, p99);
        }
    }

    private static boolean readOnce(CqlSession session, PreparedStatement ps, Random r) {
        int dev = r.nextInt(NUM_DEVICES);
        int day = r.nextInt(NUM_DAYS);
        String deviceId = String.format(Locale.ROOT, "dev-%05d", dev);
        LocalDate date = REF_DATE.plusDays(day);
        BoundStatement bound = ps.bind(deviceId, date)
                // Finding 2 (ревью): CL=QUORUM явно, чтобы совпадать с Go-бенчом
                // (cluster.Consistency = gocql.Quorum, ../go/main.go) -- без этого
                // унаследовали бы дефолт драйвера LOCAL_ONE, и "нагрузка идентична"
                // в javadoc/README было бы неточным заявлением.
                .setConsistencyLevel(DefaultConsistencyLevel.QUORUM);
        ResultSet rs = session.execute(bound);
        Row row = rs.one();
        return row != null;
    }

    private static void printResult(Result res) {
        System.out.println("-- Результат: " + res.label + " --");
        System.out.printf(Locale.ROOT, "успешных чтений: %d/%d (ошибок: %d)%n", res.success, res.n, res.errors);
        System.out.printf(Locale.ROOT, "elapsed: %d ms%n", res.elapsedMillis);
        System.out.printf(Locale.ROOT, "throughput: %.1f rows/s%n", res.throughputRowsPerSec);
        System.out.printf(Locale.ROOT, "latency p50: %d us%n", res.p50Micros);
        System.out.printf(Locale.ROOT, "latency p99: %d us%n", res.p99Micros);
        System.out.printf(Locale.ROOT,
                "RESULT mode=%s n=%d success=%d errs=%d elapsed_ms=%d throughput_rows_s=%.1f p50_us=%d p99_us=%d%n%n",
                res.label.startsWith("ON") ? "on" : "off", res.n, res.success, res.errors,
                res.elapsedMillis, res.throughputRowsPerSec, res.p50Micros, res.p99Micros);
    }

    private static long percentileMicros(long[] sortedNanos, double p) {
        if (sortedNanos.length == 0) {
            return 0;
        }
        if (sortedNanos.length == 1) {
            return sortedNanos[0] / 1000;
        }
        int idx = (int) Math.ceil(p * sortedNanos.length) - 1;
        idx = Math.max(0, Math.min(idx, sortedNanos.length - 1));
        return sortedNanos[idx] / 1000;
    }

    private static InetSocketAddress toSocketAddress(String hostPort) {
        String[] parts = hostPort.split(":", 2);
        String host = parts[0].trim();
        int port = parts.length > 1 ? Integer.parseInt(parts[1].trim()) : 9042;
        return new InetSocketAddress(host, port);
    }

    private static List<String> parseHosts(String csv) {
        return Arrays.stream(csv.split(","))
                .map(String::trim)
                .filter(s -> !s.isEmpty())
                .collect(Collectors.toList());
    }

    private static String envOr(String key, String def) {
        String v = System.getenv(key);
        return (v == null || v.isBlank()) ? def : v;
    }

    private static int intArg(String[] args, String flag, int def) {
        for (int i = 0; i < args.length - 1; i++) {
            if (args[i].equals(flag)) {
                return Integer.parseInt(args[i + 1]);
            }
        }
        return def;
    }

    private static String strArg(String[] args, String flag, String def) {
        for (int i = 0; i < args.length - 1; i++) {
            if (args[i].equals(flag)) {
                return args[i + 1];
            }
        }
        return def;
    }

    private static boolean boolArg(String[] args, String flag) {
        for (String a : args) {
            if (a.equals(flag)) {
                return true;
            }
        }
        return false;
    }

    private static long longArg(String[] args, String flag, long def) {
        for (int i = 0; i < args.length - 1; i++) {
            if (args[i].equals(flag)) {
                return Long.parseLong(args[i + 1]);
            }
        }
        return def;
    }

    private static final class Result {
        final String label;
        final int n;
        final int success;
        final int errors;
        final long elapsedMillis;
        final double throughputRowsPerSec;
        final long p50Micros;
        final long p99Micros;

        Result(String label, int n, int success, int errors, long elapsedMillis,
               double throughputRowsPerSec, long p50Micros, long p99Micros) {
            this.label = label;
            this.n = n;
            this.success = success;
            this.errors = errors;
            this.elapsedMillis = elapsedMillis;
            this.throughputRowsPerSec = throughputRowsPerSec;
            this.p50Micros = p50Micros;
            this.p99Micros = p99Micros;
        }
    }
}
