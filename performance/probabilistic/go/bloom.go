package main

import (
	"fmt"
	"strconv"

	"github.com/bits-and-blooms/bloom/v3"
)

// measureBloomFP строит фильтр под sizedFor элементов и целевой FP,
// добавляет n РЕАЛЬНЫХ ключей ("present-<i>"), затем делает queries запросов
// по заведомо ОТСУТСТВУЮЩИМ ключам ("absent-<i>") и считает долю false positive.
func measureBloomFP(n, queries uint, targetFP float64, sizedFor uint) (float64, uint) {
	f := bloom.NewWithEstimates(sizedFor, targetFP)
	for i := uint(0); i < n; i++ {
		f.Add([]byte("present-" + strconv.FormatUint(uint64(i), 10)))
	}
	var fpCount uint
	for i := uint(0); i < queries; i++ {
		if f.Test([]byte("absent-" + strconv.FormatUint(uint64(i), 10))) {
			fpCount++
		}
	}
	return float64(fpCount) / float64(queries), f.Cap()
}

func runBloom() {
	const n, queries = uint(1_000_000), uint(1_000_000)
	const target = 0.01

	fpOk, bits := measureBloomFP(n, queries, target, n)
	fpUnder, _ := measureBloomFP(n, queries, target, n/10)

	bloomBytes := bits / 8
	// Точный аналог: map[string]struct{} c ключами ~16 байт + оверхед ~48 байт/запись (оценка Go map).
	setBytes := n * (16 + 48)

	fmt.Printf("target FP           : %.4f\n", target)
	fmt.Printf("actual FP (sized ok): %.4f\n", fpOk)
	fmt.Printf("actual FP (undersized 10x): %.4f\n", fpUnder)
	fmt.Printf("bloom memory        : %d KB\n", bloomBytes/1024)
	fmt.Printf("exact set memory    : %d KB (оценка)\n", setBytes/1024)
	fmt.Printf("экономия памяти     : %.0fx\n", float64(setBytes)/float64(bloomBytes))
}
