// Опора 2a: escape analysis.
//
// Компилятор Go решает на этапе компиляции, где размещать значение — на стеке
// (дёшево, освобождается при выходе из функции, GC не участвует) или в куче
// (heap, за него потом отвечает сборщик). Указатель, который "убегает" за пределы
// кадра функции (возвращается наружу), заставляет объект уехать в heap.
//
// Смотреть решения: go build -gcflags='-m' ./...
//   - stackLocal: point НЕ убегает -> "does not escape" / остаётся на стеке
//   - heapEscape: возвращаем &point -> "escapes to heap"
package memmgmt

type point struct {
	x, y int64
}

// stackLocal: берём АДРЕС локала (&point), но наружу его не отдаём — используем
// и бросаем внутри кадра. Компилятор видит, что указатель не убегает, и оставляет
// объект на стеке: "&point{...} does not escape". Аллокации в куче нет.
//
//go:noinline
func stackLocal(a, b int64) int64 {
	p := &point{x: a, y: b} // адрес берём, но за пределы кадра не выпускаем
	return p.x*p.x + p.y*p.y
}

// heapEscape: возвращаем указатель на локал — он переживёт кадр,
// значит обязан лежать в куче. Компилятор сообщит "escapes to heap".
//
//go:noinline
func heapEscape(a, b int64) *point {
	p := point{x: a, y: b} // убегает: возвращаем &p
	return &p
}

// sink нужен, чтобы вызовы не были вырезаны как мёртвый код.
var sinkInt int64
var sinkPtr *point

func runEscapeDemo() {
	sinkInt = stackLocal(3, 4)
	sinkPtr = heapEscape(3, 4)
}
