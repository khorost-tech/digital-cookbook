// Опора 1: трассирующий GC собирает циклическую структуру.
//
// В отличие от чистого подсчёта ссылок (reference counting), где два объекта
// со взаимными ссылками A.other=B, B.other=A держат счётчик друг друга >0 и
// никогда не освобождаются, трассирующий сборщик Go идёт от корней (roots) и
// собирает всё, до чего нельзя дойти. Циклы, отрезанные от корней, — мусор.
//
// ВАЖНЫЙ CAVEAT про финализаторы:
// Мы НЕ вешаем runtime.SetFinalizer на узлы цикла. В Go финализатор для объекта,
// участвующего в цикле, НЕ гарантирован к вызову (см. документацию runtime.SetFinalizer:
// "there is no guarantee that finalizers will run before a program exits" и объекты,
// достижимые друг из друга через финализируемые ссылки, могут никогда не собраться).
// Поэтому доказываем сборку цикла НЕ финализаторами, а ПАМЯТЬЮ: снимаем ReadMemStats
// до и после того, как обнулили корень и вызвали GC. Падение HeapAlloc/HeapObjects —
// прямое доказательство, что циклический граф собран.
package main

import (
	"fmt"
	"runtime"
)

// node образует взаимную ссылку с другим node: цикл длиной 2.
// Плюс payload, чтобы аллокации были заметны в HeapAlloc.
type node struct {
	other   *node
	payload [64]byte
}

const pairs = 200_000 // 200k пар = 400k узлов со взаимными ссылками

func readMB() (heapAllocMB float64, heapObjects uint64) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1024 * 1024), m.HeapObjects
}

func main() {
	fmt.Println("go version:", runtime.Version())
	fmt.Printf("pairs=%d (взаимные ссылки A.other=B, B.other=A -> %d узлов, %d циклов длины 2)\n",
		pairs, pairs*2, pairs)

	// Baseline до постройки.
	runtime.GC()
	baseMB, baseObj := readMB()
	fmt.Printf("[baseline] HeapAlloc=%.2f MB  HeapObjects=%d\n", baseMB, baseObj)

	// Строим большой циклический граф. roots держит только по одному узлу
	// каждой пары; второй достижим лишь через .other. Оба живы, пока жив roots.
	roots := make([]*node, pairs)
	for i := 0; i < pairs; i++ {
		a := &node{}
		b := &node{}
		a.other = b // цикл...
		b.other = a // ...замкнут
		roots[i] = a
	}

	// Не даём компилятору счесть граф мёртвым раньше замера.
	runtime.KeepAlive(roots)

	beforeMB, beforeObj := readMB()
	fmt.Printf("[после постройки] HeapAlloc=%.2f MB  HeapObjects=%d\n", beforeMB, beforeObj)

	// Обнуляем ЕДИНСТВЕННЫЙ корень графа. Теперь ни один узел не достижим от roots.
	// При этом каждый узел всё ещё ссылается на своего партнёра — классический цикл,
	// который reference counting НЕ собрал бы. Трассирующий GC — соберёт.
	roots = nil

	// Два прохода GC: первый метит/выметает, второй возвращает память ОС/аллокатору
	// и стабилизирует статистику.
	runtime.GC()
	runtime.GC()

	afterMB, afterObj := readMB()
	fmt.Printf("[после roots=nil + GC x2] HeapAlloc=%.2f MB  HeapObjects=%d\n", afterMB, afterObj)

	reclaimedMB := beforeMB - afterMB
	reclaimedObj := int64(beforeObj) - int64(afterObj)
	fmt.Printf("[reclaimed] %.2f MB  %d объектов освобождено\n", reclaimedMB, reclaimedObj)

	// Fail-loud: если цикл НЕ собран, это баг демонстрации — падаем громко.
	if reclaimedObj < int64(pairs) { // ожидаем освобождения порядка pairs*2 узлов
		panic(fmt.Sprintf("цикл НЕ собран: reclaimedObj=%d < ожидания %d", reclaimedObj, pairs))
	}
	fmt.Println("ВЫВОД: циклический граф собран трассирующим GC (reference counting здесь бы утёк).")
}
