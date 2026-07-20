package main

import (
	"fmt"
	"strconv"

	"github.com/bits-and-blooms/bloom/v3"
	cuckoo "github.com/seiflotfy/cuckoofilter"
)

// cuckooDeleteWorks демонстрирует ключевое отличие от Bloom: Cuckoo умеет
// удалять элементы. После Delete ключ перестаёт находиться через Lookup.
func cuckooDeleteWorks() bool {
	cf := cuckoo.NewFilter(1024)
	key := []byte("gamma")
	cf.Insert(key)
	if !cf.Lookup(key) {
		return false
	}
	cf.Delete(key)
	return !cf.Lookup(key)
}

// runCuckoo сравнивает память Cuckoo-фильтра и Bloom-фильтра при близком FP
// на одном и том же n, и демонстрирует удаление (недоступное в Bloom).
func runCuckoo() {
	const n = uint(1_000_000)
	const targetFP = 0.01

	cf := cuckoo.NewFilter(n)
	for i := uint(0); i < n; i++ {
		cf.Insert([]byte("x-" + strconv.FormatUint(uint64(i), 10)))
	}
	// Cuckoo: занятость ~ Count() отпечатков; оценим память как размер экспортируемого дампа.
	cuckooBytes := uint(len(cf.Encode()))

	bf := bloom.NewWithEstimates(n, targetFP)
	bloomBytes := bf.Cap() / 8

	fmt.Printf("n=%d  targetFP≈%.2f\n", n, targetFP)
	fmt.Printf("cuckoo count  : %d (вставлено %d)\n", cf.Count(), n)
	fmt.Printf("cuckoo memory : %d KB\n", cuckooBytes/1024)
	fmt.Printf("bloom  memory : %d KB\n", bloomBytes/1024)
	fmt.Printf("удаление: cuckoo=%v, bloom=невозможно\n", cuckooDeleteWorks())
}
