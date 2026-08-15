// fan_test.go: fan-out + fan-in обрабатывают каждый элемент ровно один раз и
// не оставляют висящих горутин.
package goroutines

import (
	"context"
	"sync"
	"testing"
)

func TestFanOutFanInEachElementOnce(t *testing.T) {
	ctx := context.Background()
	const n = 200

	in := make(chan int)
	go func() {
		defer close(in)
		for i := range n {
			in <- i
		}
	}()

	// fan-out: 5 воркеров, каждый удваивает значение.
	outs := FanOut(ctx, 5, in, func(v int) int { return v * 2 })
	// fan-in: сливаем результаты воркеров в один канал.
	merged := FanIn(ctx, outs...)

	seen := make(map[int]int) // удвоенное значение -> счётчик
	total := 0
	for v := range merged {
		seen[v]++
		total++
	}

	if total != n {
		t.Fatalf("получено %d элементов, ожидалось %d", total, n)
	}
	for i := range n {
		if seen[i*2] != 1 {
			t.Fatalf("значение %d встретилось %d раз, ожидался ровно 1", i*2, seen[i*2])
		}
	}
}

func TestFanInMergesAllSources(t *testing.T) {
	ctx := context.Background()

	makeSrc := func(vals ...int) <-chan int {
		ch := make(chan int)
		go func() {
			defer close(ch)
			for _, v := range vals {
				ch <- v
			}
		}()
		return ch
	}

	merged := FanIn(ctx, makeSrc(1, 2), makeSrc(3, 4), makeSrc(5))

	var mu sync.Mutex // не нужен для одного читателя, но подчёркивает намерение
	sum := 0
	for v := range merged {
		mu.Lock()
		sum += v
		mu.Unlock()
	}
	if sum != 1+2+3+4+5 {
		t.Fatalf("сумма %d, ожидалось %d", sum, 15)
	}
}
