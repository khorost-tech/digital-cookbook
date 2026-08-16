package tech.khorost;

import com.github.f4b6a3.uuid.UuidCreator;
import com.github.f4b6a3.ulid.Ulid;
import com.github.f4b6a3.ulid.UlidCreator;

import java.nio.ByteBuffer;
import java.util.Arrays;
import java.util.Comparator;
import java.util.UUID;
import java.util.function.Supplier;

/**
 * GenBench benchmarks throughput and ordering properties of three common
 * identifier generation schemes: UUIDv4, UUIDv7 and ULID.
 *
 * For each generator it produces n identifiers, measures throughput
 * (ops/sec) and checks whether the sequence of raw byte values, taken in
 * generation order, is non-decreasing lexicographically (compared as
 * unsigned bytes) -- i.e. IDs sort the same way they were created. That
 * single check stands in for both "monotonic_within_ms" and
 * "byte_sortable", exactly as in the Go benchmark (gen/go/main.go): for
 * these generators the two properties coincide.
 *
 * Output contract (shared across the Go/Java/Rust benchmarks in this
 * cookbook so gen-bench.sh can aggregate them uniformly):
 *
 *   java &lt;gen&gt; ops_per_sec=&lt;float&gt; monotonic_within_ms=&lt;true|false&gt; byte_sortable=&lt;true|false&gt;
 */
public final class GenBench {

    private GenBench() {
    }

    private static byte[] uuidBytes(UUID u) {
        ByteBuffer b = ByteBuffer.allocate(16);
        b.putLong(u.getMostSignificantBits());
        b.putLong(u.getLeastSignificantBits());
        return b.array();
    }

    private static byte[] ulidBytes(Ulid u) {
        ByteBuffer b = ByteBuffer.allocate(16);
        b.putLong(u.getMostSignificantBits());
        b.putLong(u.getLeastSignificantBits());
        return b.array();
    }

    private static void bench(String name, int n, Supplier<byte[]> gen) {
        byte[][] vals = new byte[n][];
        long start = System.nanoTime();
        for (int i = 0; i < n; i++) {
            vals[i] = gen.get();
        }
        double elapsedSeconds = (System.nanoTime() - start) / 1e9;
        double opsPerSec = n / elapsedSeconds;

        Comparator<byte[]> unsigned = Arrays::compareUnsigned;
        boolean monotonic = true;
        for (int i = 1; i < n; i++) {
            if (unsigned.compare(vals[i - 1], vals[i]) > 0) {
                monotonic = false;
                break;
            }
        }

        System.out.printf(
                "java %s ops_per_sec=%.0f monotonic_within_ms=%b byte_sortable=%b%n",
                name, opsPerSec, monotonic, monotonic);
    }

    public static void main(String[] args) {
        int n = args.length > 0 ? Integer.parseInt(args[0]) : 1_000_000;

        bench("uuidv4", n, () -> uuidBytes(UuidCreator.getRandomBased()));
        bench("uuidv7", n, () -> uuidBytes(UuidCreator.getTimeOrderedEpoch()));
        // getMonotonicUlid() (not plain getUlid()) mirrors the Go benchmark's
        // use of ulid.DefaultEntropy(), which returns a monotonic entropy
        // source: within the same millisecond the random component is
        // incremented rather than freshly randomized, guaranteeing
        // non-decreasing byte order across the whole sequence.
        bench("ulid", n, () -> ulidBytes(UlidCreator.getMonotonicUlid()));
    }
}
