package main

import "testing"

// При корректной оценке n фактический FP не должен грубо превышать целевой.
func TestBloomFPWithinBoundWhenSizedCorrectly(t *testing.T) {
	n, queries := uint(100_000), uint(200_000)
	target := 0.01
	fp, _ := measureBloomFP(n, queries, target, n) // sizedFor == n
	if fp > 2*target {
		t.Fatalf("actual FP %.4f превышает 2×target %.4f", fp, 2*target)
	}
	if fp == 0 {
		t.Fatalf("FP == 0 подозрительно: проверь, что запросы по отсутствующим ключам")
	}
}

// При недооценке n в 10× фильтр переполняется и FP резко растёт (капкан).
func TestBloomFPBlowsUpWhenUndersized(t *testing.T) {
	n, queries := uint(100_000), uint(200_000)
	target := 0.01
	fpUnder, _ := measureBloomFP(n, queries, target, n/10) // построен под 10k, кладём 100k
	if fpUnder < 0.5 {
		t.Fatalf("ожидали взрыв FP при недооценке, получили %.4f", fpUnder)
	}
}
