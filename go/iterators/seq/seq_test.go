package seq

import (
	"reflect"
	"testing"
)

func TestCount(t *testing.T) {
	got := Collect(Count(4))
	want := []int{0, 1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Count(4) = %v, ждали %v", got, want)
	}
}

func TestFilterMapCompose(t *testing.T) {
	// чётные из 0..9, возведённые в квадрат
	even := Filter(Count(10), func(v int) bool { return v%2 == 0 })
	squares := Map(even, func(v int) int { return v * v })
	got := Collect(squares)
	want := []int{0, 4, 16, 36, 64}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("получили %v, ждали %v", got, want)
	}
}

func TestEarlyBreakStopsIterator(t *testing.T) {
	// break на третьем элементе — итератор не должен продолжать выдачу
	var seen []int
	for v := range Count(1_000_000) {
		seen = append(seen, v)
		if v == 2 {
			break
		}
	}
	if !reflect.DeepEqual(seen, []int{0, 1, 2}) {
		t.Fatalf("ранний break не сработал: %v", seen)
	}
}

func TestEnumerate(t *testing.T) {
	var idx, vals []int
	for i, v := range Enumerate(Map(Count(3), func(v int) int { return v * 10 })) {
		idx = append(idx, i)
		vals = append(vals, v)
	}
	if !reflect.DeepEqual(idx, []int{0, 1, 2}) {
		t.Errorf("индексы: %v", idx)
	}
	if !reflect.DeepEqual(vals, []int{0, 10, 20}) {
		t.Errorf("значения: %v", vals)
	}
}

func TestCleanupRunsOnCompletion(t *testing.T) {
	cleaned := false
	it := WithCleanup(Count(3), func() { cleaned = true })
	Collect(it)
	if !cleaned {
		t.Fatal("cleanup не выполнился при полном обходе")
	}
}

func TestCleanupRunsOnEarlyBreak(t *testing.T) {
	// ключевая гарантия: cleanup срабатывает и при раннем break
	cleaned := false
	it := WithCleanup(Count(1000), func() { cleaned = true })
	for v := range it {
		if v == 1 {
			break
		}
	}
	if !cleaned {
		t.Fatal("cleanup не выполнился при раннем break — ресурс утёк бы")
	}
}
