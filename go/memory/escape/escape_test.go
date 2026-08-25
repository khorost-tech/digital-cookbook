// escape_test.go — тесты корректности примеров + бенчмарки, показывающие
// разницу в аллокациях между «стековыми» и «убегающими» функциями.
//
// Тесты фиксируют поведение (что функции возвращают ожидаемое), а бенчмарки
// с b.ReportAllocs() наглядно печатают allocs/op: 0 у стековых функций и >0
// у тех, чьи значения уходят в кучу.
//
// Числа снимайте у себя:
//
//	go test -bench=. -benchmem -run='^$' ./escape/
package escape

import "testing"

// sink-переменные не дают компилятору выкинуть вычисления как «мёртвые».
var (
	sinkInt      int
	sinkPtr      *Point
	sinkAny      any
	sinkByteSize byte
)

func TestNewOnStack(t *testing.T) {
	if got := newOnStack(); got != 3 {
		t.Fatalf("newOnStack() = %d, want 3", got)
	}
}

func TestNewOnHeap(t *testing.T) {
	p := newOnHeap()
	if p == nil {
		t.Fatal("newOnHeap() = nil, want non-nil")
	}
	if p.X != 1 || p.Y != 2 {
		t.Fatalf("newOnHeap() = %+v, want {X:1 Y:2}", *p)
	}
}

func TestBoxIntoInterface(t *testing.T) {
	v := boxIntoInterface(42)
	got, ok := v.(int)
	if !ok {
		t.Fatalf("boxIntoInterface(42) держит %T, want int", v)
	}
	if got != 42 {
		t.Fatalf("boxIntoInterface(42) = %d, want 42", got)
	}
}

func TestLeakViaSlice(t *testing.T) {
	leaked = nil // сбрасываем пакетное состояние для изоляции теста
	leakViaSlice()
	leakViaSlice()
	if len(leaked) != 2 {
		t.Fatalf("len(leaked) = %d, want 2", len(leaked))
	}
	if leaked[0].X != 1 || leaked[0].Y != 2 {
		t.Fatalf("leaked[0] = %+v, want {X:1 Y:2}", *leaked[0])
	}
	leaked = nil // не тащим состояние в другие тесты/бенчи
}

func TestStackArg(t *testing.T) {
	p := &Point{X: 4, Y: 5}
	if got := stackArg(p); got != 9 {
		t.Fatalf("stackArg(%+v) = %d, want 9", *p, got)
	}
}

func TestClosureCapture(t *testing.T) {
	next := closureCapture()
	if a, b, c := next(), next(), next(); a != 1 || b != 2 || c != 3 {
		t.Fatalf("closureCapture() выдал %d,%d,%d, want 1,2,3", a, b, c)
	}
	// Независимые замыкания захватывают РАЗНЫЕ переменные.
	other := closureCapture()
	if got := other(); got != 1 {
		t.Fatalf("новое замыкание начинает с %d, want 1", got)
	}
}

// BenchmarkOnStack — значение остаётся на стеке: ожидаемо 0 allocs/op.
func BenchmarkOnStack(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sinkInt = newOnStack()
	}
}

// BenchmarkOnHeap — возвращается указатель на локаль: значение уходит в кучу,
// ожидаемо >0 allocs/op (1 аллокация на вызов).
func BenchmarkOnHeap(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sinkPtr = newOnHeap()
	}
}

// BenchmarkBoxInterface — боксинг int в any: значение упаковывается в кучу,
// ожидаемо >0 allocs/op.
func BenchmarkBoxInterface(b *testing.B) {
	b.ReportAllocs()
	// Боксим значения > 255: малые int (0–255) рантайм кэширует в staticuint64s,
	// и на них боксинг НЕ аллоцирует, хотя escape-анализ и метит «escapes to heap».
	// Крупные значения кэш не покрывает — упаковка реально уходит в кучу.
	for i := 0; i < b.N; i++ {
		sinkAny = boxIntoInterface(i + 256)
	}
}

// BenchmarkStackArg — указатель на локаль передаётся аргументом, но не утекает:
// значение остаётся на стеке вызывающего, ожидаемо 0 allocs/op.
func BenchmarkStackArg(b *testing.B) {
	b.ReportAllocs()
	p := &Point{X: 4, Y: 5}
	for i := 0; i < b.N; i++ {
		sinkInt = stackArg(p)
	}
}

// BenchmarkSizeVarMake — make([]byte, n) с непостоянным n: буфер локален, но
// уходит в кучу из-за неизвестного размера. Ожидаемо >0 allocs/op, хотя
// -gcflags=-m метит его «does not escape».
func BenchmarkSizeVarMake(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sinkByteSize = sizeVarMake(64)
	}
}

// BenchmarkSizeConstMake — make([]byte, 64) с константой: остаётся на стеке,
// ожидаемо 0 allocs/op.
func BenchmarkSizeConstMake(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sinkByteSize = sizeConstMake()
	}
}
