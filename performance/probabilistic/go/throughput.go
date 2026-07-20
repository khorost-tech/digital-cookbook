package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/axiomhq/hyperloglog"
	"github.com/bits-and-blooms/bloom/v3"
	cuckoo "github.com/seiflotfy/cuckoofilter"
)

func opsPerSec(n uint, fn func(i uint)) float64 {
	start := time.Now()
	for i := uint(0); i < n; i++ {
		fn(i)
	}
	el := time.Since(start).Seconds()
	if el == 0 {
		el = 1e-9
	}
	return float64(n) / el
}

// runThroughput замеряет скорость вставки/запроса Bloom, Cuckoo и вставки HLL
// на фиксированном N. Замер скорости — не инвариант, поэтому теста нет; цифры
// используются для таблицы throughput в статье.
func runThroughput() {
	const n = uint(1_000_000)
	keys := make([][]byte, n)
	for i := uint(0); i < n; i++ {
		keys[i] = []byte("k-" + strconv.FormatUint(uint64(i), 10))
	}

	bf := bloom.NewWithEstimates(n, 0.01)
	bfIns := opsPerSec(n, func(i uint) { bf.Add(keys[i]) })
	bfQry := opsPerSec(n, func(i uint) { _ = bf.Test(keys[i]) })

	cf := cuckoo.NewFilter(n)
	cfIns := opsPerSec(n, func(i uint) { cf.Insert(keys[i]) })
	cfQry := opsPerSec(n, func(i uint) { _ = cf.Lookup(keys[i]) })

	hll := hyperloglog.New14()
	hllIns := opsPerSec(n, func(i uint) { hll.Insert(keys[i]) })

	fmt.Printf("Bloom  insert %.0f ops/s  query %.0f ops/s\n", bfIns, bfQry)
	fmt.Printf("Cuckoo insert %.0f ops/s  query %.0f ops/s\n", cfIns, cfQry)
	fmt.Printf("HLL    insert %.0f ops/s\n", hllIns)
}
