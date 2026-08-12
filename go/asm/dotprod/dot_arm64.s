#include "textflag.h"

// func dotNEON(a, b *float64, n int) float64
// Аргументы: a+0(FP), b+8(FP), n+16(FP); результат ret+24(FP).
//
// ПРИМЕЧАНИЕ (кросс-сборка без arm-железа): семантика инструкций сверена
// построчно с cmd/internal/obj/arm64/doc.go и реальным использованием в
// GOROOT/src/runtime и GOROOT/src/math/big на amd64-хосте (исполнить нельзя,
// только собрать+vet). См. отчёт задачи за разбором операндов.
TEXT ·dotNEON(SB), NOSPLIT, $0-32
	MOVD a+0(FP), R0          // R0 = &a[0]
	MOVD b+8(FP), R1          // R1 = &b[0]
	MOVD n+16(FP), R2         // R2 = n
	VEOR V0.B16, V0.B16, V0.B16   // V0 = аккумулятор (2 частичные суммы), обнулён

	LSR  $1, R2, R3           // R3 = n / 2 (полных пар)
	CBZ  R3, tail             // если пар нет, V0 всё ещё 0 — редукция не нужна

pairloop:
	VLD1.P 16(R0), [V1.D2]    // V1 = a[i..i+1], пост-инкремент R0 += 16
	VLD1.P 16(R1), [V2.D2]    // V2 = b[i..i+1], пост-инкремент R1 += 16
	// VFMLA Vm, Vn, Vd => Vd += Vn*Vm (doc.go: "VFMLA V29.S2,V20.S2,V14.S2
	// <=> fmla v14.2s,v20.2s,v29.2s", т.е. fmla Vd,Vn,Vm). Здесь Vm=V2,
	// Vn=V1, Vd=V0 => V0 += V1*V2.
	VFMLA V2.D2, V1.D2, V0.D2
	SUB  $1, R3, R3
	CBNZ R3, pairloop

	// Горизонтальная сумма пары V0.D[0]+V0.D[1] -> F0.
	// В Go arm64 asm НЕТ мнемоники FADDP/VFADDP (векторный float pairwise
	// add) — есть только VADDP, которая кодирует ЦЕЛОЧИСЛЕННЫЙ ADDP
	// (U=0 в Advanced-SIMD-encoding), а не FADDP (U=1); для float64 это
	// дало бы неверный результат (побитовое сложение мантисс). Поэтому
	// вместо pairwise-add инструкции переносим старшую половину в
	// скалярный регистр и складываем через FADDD:
	//   VMOV Vn.T[i], Vd.T[j] — операнды в порядке src, dst (doc.go:
	//   "VMOV V13.B[1], R20 <=> mov x20, v13.b[1]"), т.е. VMOV V0.D[1],
	//   V1.D[0] копирует старшую половину V0 в младшую половину V1 —
	//   V1 и F1 это один физический регистр (doc.go: "Bn,Hn,Dn,Sn,Qn
	//   инструкции пишутся как Fn во float-инструкциях и как Vn в SIMD"),
	//   так что после этого F1 = V0.D[1] как обычный double.
	VMOV V0.D[1], V1.D[0]
	// 2-операндная FADDD аккумулирует в правый операнд (F0 += F1), форма
	// подтверждена в GOROOT/src/math/exp_arm64.s: "FADDD F1, F0".
	FADDD F1, F0              // F0 = V0.D[0] + V0.D[1] (полная сумма пар)

tail:
	AND  $1, R2, R4           // R4 = n % 2 (0 или 1)
	CBZ  R4, done
	FMOVD (R0), F1
	FMOVD (R1), F2
	// FMADDD Fm, Fa, Fn, Fd => Fd = Fa + Fn*Fm (doc.go: "FMADDD F30,F20,
	// F3,F29 <=> fmadd d29,d3,d30,d20", т.е. fmadd Fd,Fn,Fm,Fa). Здесь
	// Fm=F1, Fa=F0, Fn=F2, Fd=F0 => F0 = F0 + F2*F1.
	FMADDD F1, F0, F2, F0
done:
	FMOVD F0, ret+24(FP)
	RET
