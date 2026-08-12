#include "textflag.h"

// func dotAVX2(a, b *float64, n int) float64
// Аргументы: a+0(FP), b+8(FP), n+16(FP); результат ret+24(FP).
TEXT ·dotAVX2(SB), NOSPLIT, $0-32
	MOVQ a+0(FP), AX          // AX = &a[0]
	MOVQ b+8(FP), BX          // BX = &b[0]
	MOVQ n+16(FP), CX         // CX = n
	VXORPD Y0, Y0, Y0         // Y0 = аккумулятор (4 частичные суммы)

	MOVQ CX, DX
	SHRQ $2, DX               // DX = n / 4  (полных блоков по 4)
	JZ   tail

blockloop:
	VMOVUPD (AX), Y1          // Y1 = a[i..i+3]
	VMOVUPD (BX), Y2          // Y2 = b[i..i+3]
	VFMADD231PD Y2, Y1, Y0    // Y0 += Y1 * Y2  (порядок операндов проверяется тестом корректности)
	ADDQ $32, AX
	ADDQ $32, BX
	DECQ DX
	JNZ  blockloop

	// горизонтальная свёртка Y0 (4 double) -> X0[0]
	VEXTRACTF128 $1, Y0, X1
	VADDPD X1, X0, X0
	VHADDPD X0, X0, X0        // X0[0] = сумма всех четырёх

tail:
	ANDQ $3, CX               // CX = n % 4 (остаток)
	JZ   done
tailloop:
	VMOVSD (AX), X1
	VMOVSD (BX), X2
	VFMADD231SD X2, X1, X0    // X0[0] += X1 * X2 (скаляр)
	ADDQ $8, AX
	ADDQ $8, BX
	DECQ CX
	JNZ  tailloop

done:
	VMOVSD X0, ret+24(FP)
	VZEROUPPER                // сброс верхних AVX-состояний перед возвратом
	RET
