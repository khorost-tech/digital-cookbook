// Опора 3: паузы GC — ОДИН прогон, качественно.
//
// Аллокационно-тяжёлый ворклоад создаёт давление на сборщик; мы читаем РЕАЛЬНЫЕ
// длительности stop-the-world пауз через runtime/debug.ReadGCStats — поле
// PauseQuantiles даёт настоящие наносекунды (min / p50 / p90 / p99 / max по
// последним записанным паузам), а не грубую оценку по границам бакетов
// гистограммы runtime/metrics. Дополнительно программу полезно гонять с
// GODEBUG=gctrace=1 — тогда runtime печатает строку на каждый цикл GC.
//
// ВАЖНО: это замер ОДНОГО прогона на конкретном железе/ОС, а НЕ свойство языка.
// Абсолютные числа пауз плавают от машины к машине НА ПОРЯДКИ (напр. linux/amd64
// даёт десятки-сотни микросекунд там, где windows/amd64 — единицы; см. RUN.md).
// Устойчиво лишь качественное: сборщик Go нацелен на СУБ-МИЛЛИСЕКУНДНЫЕ STW-паузы,
// на практике — десятки–сотни микросекунд с редкими выбросами в низкие миллисекунды.
package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"time"
)

func main() {
	fmt.Println("go version:", runtime.Version())

	// Ворклоад: много короткоживущих аллокаций -> частые циклы GC.
	const rounds = 200
	const perRound = 50_000
	var sink *[]byte
	for r := 0; r < rounds; r++ {
		garbage := make([][]byte, perRound)
		for i := 0; i < perRound; i++ {
			b := make([]byte, 256)
			b[0] = byte(i)
			garbage[i] = b
		}
		// Держим последний, остальное — мусор для GC.
		last := garbage[perRound-1]
		sink = &last
	}
	runtime.KeepAlive(sink)

	// PauseQuantiles(101) -> [min, p1, ..., p99, max] по записанным паузам
	// (ReadGCStats хранит до 256 последних длительностей STW-пауз).
	var s debug.GCStats
	s.PauseQuantiles = make([]time.Duration, 101)
	debug.ReadGCStats(&s)

	if s.NumGC == 0 || len(s.PauseQuantiles) == 0 {
		panic("GC не отработал / нет записанных пауз — замер недействителен")
	}

	fmt.Printf("ворклоад: rounds=%d perRound=%d (короткоживущих срезов ~%d)\n",
		rounds, perRound, rounds*perRound)
	fmt.Printf("циклов GC за процесс: %d; суммарная пауза STW: %v\n", s.NumGC, s.PauseTotal)
	fmt.Printf("длительности STW-пауз (реальные, этот прогон):\n")
	fmt.Printf("  min ~ %v\n", s.PauseQuantiles[0])
	fmt.Printf("  p50 ~ %v\n", s.PauseQuantiles[50])
	fmt.Printf("  p90 ~ %v\n", s.PauseQuantiles[90])
	fmt.Printf("  p99 ~ %v\n", s.PauseQuantiles[99])
	fmt.Printf("  max ~ %v\n", s.PauseQuantiles[100])
	// Никакого прошитого вывода: печатаем только измеренные значения. Абсолютные
	// числа host-зависимы на порядки (см. RUN.md), обобщать их в «субмиллисекундные»
	// нельзя — max отдельных прогонов заходит за миллисекунду.
	fmt.Println("(замер ОДНОГО прогона; числа host-зависимы, здесь НЕ обобщаются)")
}
