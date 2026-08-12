//go:build ignore

// gen.go — генератор AVX2-реализации dot product через avo.
// Запуск: go run gen.go -out dotavo_amd64.s -stubs dotavo_stub_amd64.go
package main

import (
	. "github.com/mmcloughlin/avo/build"
	. "github.com/mmcloughlin/avo/operand"
)

func main() {
	TEXT("dotAvoAVX2", NOSPLIT, "func(a, b *float64, n int) float64")
	Doc("dotAvoAVX2 — сгенерированное avo скалярное произведение (AVX2+FMA).")
	a := Load(Param("a"), GP64())
	b := Load(Param("b"), GP64())
	n := Load(Param("n"), GP64())

	acc := YMM()
	VXORPD(acc, acc, acc)

	blocks := GP64()
	MOVQ(n, blocks)
	SHRQ(Imm(2), blocks) // n / 4

	Label("blockloop")
	CMPQ(blocks, Imm(0))
	JE(LabelRef("reduce"))
	ya, yb := YMM(), YMM()
	VMOVUPD(Mem{Base: a}, ya)
	VMOVUPD(Mem{Base: b}, yb)
	VFMADD231PD(yb, ya, acc)
	ADDQ(Imm(32), a)
	ADDQ(Imm(32), b)
	DECQ(blocks)
	JMP(LabelRef("blockloop"))

	Label("reduce")
	low := XMM()
	high := XMM()
	VEXTRACTF128(Imm(1), acc, high)
	VADDPD(high, acc.AsX(), low)
	VHADDPD(low, low, low)

	Label("tail")
	rem := GP64()
	MOVQ(n, rem)
	ANDQ(Imm(3), rem)
	Label("tailloop")
	CMPQ(rem, Imm(0))
	JE(LabelRef("done"))
	xa, xb := XMM(), XMM()
	VMOVSD(Mem{Base: a}, xa)
	VMOVSD(Mem{Base: b}, xb)
	VFMADD231SD(xb, xa, low)
	ADDQ(Imm(8), a)
	ADDQ(Imm(8), b)
	DECQ(rem)
	JMP(LabelRef("tailloop"))

	Label("done")
	Store(low, ReturnIndex(0))
	VZEROUPPER()
	RET()
	Generate()
}
