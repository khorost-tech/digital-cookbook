//! identifiers-gen benchmarks throughput and ordering properties of three
//! common identifier generation schemes: UUIDv4, UUIDv7 and ULID.
//!
//! For each generator it produces n identifiers, measures throughput
//! (ops/sec) and checks whether the sequence of raw byte values, taken in
//! generation order, is non-decreasing lexicographically -- i.e. IDs sort
//! the same way they were created. That single check stands in for both
//! "monotonic_within_ms" and "byte_sortable", exactly as in the Go
//! benchmark (gen/go/main.go) and the Java benchmark (gen/java): for these
//! generators the two properties coincide.
//!
//! Output contract (shared across the Go/Java/Rust benchmarks in this
//! cookbook so gen-bench.sh can aggregate them uniformly):
//!
//!   rust <gen> ops_per_sec=<float> monotonic_within_ms=<true|false> byte_sortable=<true|false>

use std::time::Instant;
use ulid::Generator as UlidGenerator;
use uuid::Uuid;

/// Generates n identifiers via `gen`, measures throughput and checks
/// whether the raw bytes come out in non-decreasing lexicographic order.
fn bench<F: FnMut() -> Vec<u8>>(name: &str, n: usize, mut gen: F) {
    let mut v: Vec<Vec<u8>> = Vec::with_capacity(n);
    let t = Instant::now();
    for _ in 0..n {
        v.push(gen());
    }
    let elapsed = t.elapsed().as_secs_f64();
    let ops = n as f64 / elapsed;
    let mono = v.windows(2).all(|w| w[0] <= w[1]);
    println!(
        "rust {name} ops_per_sec={ops:.0} monotonic_within_ms={mono} byte_sortable={mono}"
    );
}

fn main() {
    let n: usize = std::env::args()
        .nth(1)
        .and_then(|s| s.parse().ok())
        .unwrap_or(1_000_000);

    bench("uuidv4", n, || Uuid::new_v4().as_bytes().to_vec());

    bench("uuidv7", n, || Uuid::now_v7().as_bytes().to_vec());

    // ulid::Ulid::generate() draws fresh random bits on every call (the
    // crate's equivalent of the old `Ulid::new()`), which is NOT
    // monotonic across a tight loop within the same millisecond -- it
    // would fail the byte-sortability check here. To mirror the Go
    // benchmark's ulid.DefaultEntropy() (a monotonic entropy source) and
    // the Java benchmark's UlidCreator.getMonotonicUlid(), we drive a
    // single ulid::Generator across the whole run: within the same
    // millisecond it increments the random component instead of
    // re-randomizing, guaranteeing non-decreasing byte order across the
    // whole sequence. On the (extremely unlikely) random-bits overflow
    // within a millisecond, we commit the overflow via increment so the
    // sequence stays monotonic rather than panicking.
    let mut ulid_gen = UlidGenerator::new();
    bench("ulid", n, || {
        let id = match ulid_gen.generate() {
            Ok(id) => id,
            Err(overflow) => overflow.commit_overflow_increment(),
        };
        let bytes: [u8; 16] = id.into();
        bytes.to_vec()
    });
}
