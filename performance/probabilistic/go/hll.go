package main

import (
	"fmt"
	"strconv"

	"github.com/axiomhq/hyperloglog"
)

// estimateHLL строит HLL-скетч (precision 14, стандартная ошибка ~0.8%),
// вставляет n уникальных ключей и возвращает оценку кардинальности
// и относительную ошибку против истинного n.
func estimateHLL(n uint) (uint64, float64) {
	sk := hyperloglog.New14()
	for i := uint(0); i < n; i++ {
		sk.Insert([]byte("u-" + strconv.FormatUint(uint64(i), 10)))
	}
	est := sk.Estimate()
	relErr := (float64(est) - float64(n)) / float64(n)
	return est, relErr
}

// mergeTwoHalves: два HLL по half уникальных каждый (непересекающиеся множества
// "a-*" и "b-*"), merge → должно дать оценку ~2*half (объединение).
func mergeTwoHalves(half uint) (uint64, float64) {
	a := hyperloglog.New14()
	b := hyperloglog.New14()
	for i := uint(0); i < half; i++ {
		a.Insert([]byte("a-" + strconv.FormatUint(uint64(i), 10)))
		b.Insert([]byte("b-" + strconv.FormatUint(uint64(i), 10)))
	}
	_ = a.Merge(b)
	union := a.Estimate()
	trueUnion := float64(2 * half)
	return union, (float64(union) - trueUnion) / trueUnion
}

func runHLL() {
	for _, n := range []uint{1_000_000, 10_000_000} {
		sk := hyperloglog.New14()
		for i := uint(0); i < n; i++ {
			sk.Insert([]byte("u-" + strconv.FormatUint(uint64(i), 10)))
		}
		est := sk.Estimate()
		relErr := (float64(est) - float64(n)) / float64(n)

		// Реальный размер сериализованного скетча (после реальных вставок,
		// т.к. axiomhq/hyperloglog не имеет ToBytes() — используем
		// MarshalBinary() из encoding.BinaryMarshaler).
		data, err := sk.MarshalBinary()
		if err != nil {
			fmt.Printf("marshal error: %v\n", err)
			continue
		}
		mem := len(data)
		exact := n * 16 // оценка точного set: ~16 байт/ключ (без оверхеда map)
		fmt.Printf("N=%d  true=%d  HLL=%d  err=%.3f%%  HLL≈%d B  exact≈%d KB\n",
			n, n, est, relErr*100, mem, exact/1024)
	}
	union, relErr := mergeTwoHalves(500_000)
	fmt.Printf("merge(500k+500k) → %d  err=%.3f%%\n", union, relErr*100)
}
