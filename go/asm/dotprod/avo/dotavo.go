//go:build amd64

// Package avo содержит avo-сгенерированную версию dot product.
package avo

//go:generate go run gen.go -out dotavo_amd64.s -stubs dotavo_stub_amd64.go

import "golang.org/x/sys/cpu"

// DotAvo — публичная обёртка над сгенерированным ядром.
func DotAvo(a, b []float64) float64 {
	if len(a) != len(b) {
		panic("dotprod/avo: length mismatch")
	}
	if len(a) == 0 {
		return 0
	}
	if cpu.X86.HasAVX2 && cpu.X86.HasFMA {
		return dotAvoAVX2(&a[0], &b[0], len(a))
	}
	return dotAvoGeneric(a, b)
}

func dotAvoGeneric(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
