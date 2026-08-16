// Command identifiers-gen benchmarks throughput and ordering properties of
// four common identifier generation schemes: UUIDv4, UUIDv7, ULID and a
// Snowflake-style generator (sonyflake).
//
// For each generator it produces n identifiers, measures throughput
// (ops/sec) and checks two ordering properties on the raw bytes of the
// generated values:
//   - monotonic_within_ms: whether the sequence of raw byte slices, taken in
//     generation order, is non-decreasing lexicographically (i.e. IDs sort
//     the same way they were created).
//   - byte_sortable: whether lexicographic ordering of the raw bytes matches
//     the generation order (for these generators this is the same check as
//     monotonicity, since byte layout is designed to be sortable).
//
// Output contract (shared across the Go/Java/Rust benchmarks in this
// cookbook so gen-bench.sh can aggregate them uniformly):
//
//	go <gen> ops_per_sec=<float> monotonic_within_ms=<true|false> byte_sortable=<true|false>
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/oklog/ulid/v2"
	"github.com/sony/sonyflake"
)

// bench generates n identifiers via gen, measures throughput and checks
// whether the raw bytes come out in non-decreasing lexicographic order.
func bench(name string, n int, gen func() []byte) {
	vals := make([][]byte, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		vals[i] = gen()
	}
	elapsed := time.Since(start).Seconds()

	ops := float64(n) / elapsed
	mono := sort.SliceIsSorted(vals, func(i, j int) bool {
		return string(vals[i]) < string(vals[j])
	})

	fmt.Printf("go %s ops_per_sec=%.0f monotonic_within_ms=%v byte_sortable=%v\n", name, ops, mono, mono)
}

func main() {
	n := flag.Int("n", 1000000, "count of ids to generate per generator")
	flag.Parse()

	bench("uuidv4", *n, func() []byte {
		u := uuid.New()
		return u[:]
	})

	bench("uuidv7", *n, func() []byte {
		u, err := uuid.NewV7()
		if err != nil {
			log.Fatalf("uuidv7: %v", err)
		}
		return u[:]
	})

	entropy := ulid.DefaultEntropy()
	bench("ulid", *n, func() []byte {
		u := ulid.MustNew(ulid.Timestamp(time.Now()), entropy)
		return u[:]
	})

	sf := sonyflake.NewSonyflake(sonyflake.Settings{})
	if sf == nil {
		log.Fatal("snowflake: failed to initialize sonyflake generator")
	}
	bench("snowflake", *n, func() []byte {
		id, err := sf.NextID()
		if err != nil {
			log.Fatalf("snowflake: %v", err)
		}
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, id)
		return b
	})
}
