// slices_test.go — тесты инвариантов роста append, поведения nil/пустого
// слайса и независимости копий.
package slicesmaps

import (
	"slices"
	"testing"
)

// TestAppendGrowthInvariants: len растёт по одному, cap не убывает и всегда
// покрывает len. Конкретные значения cap не фиксируем — это деталь рантайма.
func TestAppendGrowthInvariants(t *testing.T) {
	const n = 20
	steps := AppendGrowth(n)
	if len(steps) != n {
		t.Fatalf("шагов %d, ожидали %d", len(steps), n)
	}
	prevCap := 0
	for i, st := range steps {
		if st.Len != i+1 {
			t.Errorf("шаг %d: Len = %d, ожидали %d", i, st.Len, i+1)
		}
		if st.Cap < st.Len {
			t.Errorf("шаг %d: Cap %d < Len %d", i, st.Cap, st.Len)
		}
		if st.Cap < prevCap {
			t.Errorf("шаг %d: Cap уменьшилась с %d до %d", i, prevCap, st.Cap)
		}
		prevCap = st.Cap
	}
}

// TestAppendToNil: append к nil работает и даёт ожидаемое содержимое.
func TestAppendToNil(t *testing.T) {
	got := AppendToNil()
	if !slices.Equal(got, []int{1, 2, 3}) {
		t.Errorf("AppendToNil = %v, ожидали [1 2 3]", got)
	}
}

// TestNilVsEmpty: nil-слайс и пустой слайс различаются по == nil, но
// одинаковы по len — сравнивать «пусто ли» надо через len.
func TestNilVsEmpty(t *testing.T) {
	var nilSlice []int
	emptySlice := []int{}

	if !IsNil(nilSlice) {
		t.Error("nil-слайс должен быть nil")
	}
	if IsNil(emptySlice) {
		t.Error("пустой слайс []int{} не должен быть nil")
	}
	// По длине оба пусты.
	if len(nilSlice) != 0 || len(emptySlice) != 0 {
		t.Error("оба слайса должны иметь len == 0")
	}
}

// TestCloneIsIndependent: обе копии не делят backing-массив с исходником —
// правка копии не видна в оригинале.
func TestCloneIsIndependent(t *testing.T) {
	for name, clone := range map[string]func([]int) []int{
		"append": CloneViaAppend,
		"stdlib": CloneViaStdlib,
	} {
		t.Run(name, func(t *testing.T) {
			src := []int{1, 2, 3}
			dst := clone(src)
			if !slices.Equal(src, dst) {
				t.Fatalf("копия %v не равна исходнику %v", dst, src)
			}
			dst[0] = 99 // правим копию
			if src[0] != 1 {
				t.Errorf("правка копии протекла в исходник: src[0] = %d", src[0])
			}
		})
	}
}
