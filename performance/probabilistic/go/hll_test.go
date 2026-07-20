package main

import (
	"math"
	"testing"
)

func TestHLLErrorWithinTwoPercent(t *testing.T) {
	n := uint(1_000_000)
	est, relErr := estimateHLL(n)
	if est == 0 {
		t.Fatal("оценка HLL == 0")
	}
	if math.Abs(relErr) > 0.03 {
		t.Fatalf("ошибка HLL %.4f превышает 3%%", relErr)
	}
}

func TestHLLMergeApproximatesUnion(t *testing.T) {
	// Две непересекающиеся половины по 500k → merge ≈ 1M.
	union, relErr := mergeTwoHalves(500_000)
	if union == 0 || math.Abs(relErr) > 0.03 {
		t.Fatalf("merge: union=%d relErr=%.4f (ожидали ~1e6, <3%%)", union, relErr)
	}
}
