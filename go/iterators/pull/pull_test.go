package pull

import (
	"reflect"
	"testing"

	"tech.khorost/go-iterators-cookbook/seq"
)

func TestZipStopsAtShorter(t *testing.T) {
	nums := seq.Count(5)                                        // 0..4
	letters := seq.Map(seq.Count(3), func(i int) string {       // "a","b","c"
		return string(rune('a' + i))
	})

	var keys []int
	var vals []string
	for k, v := range Zip(nums, letters) {
		keys = append(keys, k)
		vals = append(vals, v)
	}
	if !reflect.DeepEqual(keys, []int{0, 1, 2}) {
		t.Errorf("keys = %v, ждали [0 1 2] (обрыв по короткой)", keys)
	}
	if !reflect.DeepEqual(vals, []string{"a", "b", "c"}) {
		t.Errorf("vals = %v", vals)
	}
}

func TestTake(t *testing.T) {
	got := Take(seq.Count(1_000_000), 3) // не материализуем миллион — берём 3
	if !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Fatalf("Take = %v", got)
	}
}

func TestTakeMoreThanAvailable(t *testing.T) {
	got := Take(seq.Count(2), 10) // просим больше, чем есть
	if !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("Take = %v, ждали [0 1]", got)
	}
}
