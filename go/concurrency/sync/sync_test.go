// sync_test.go — тесты примеров к статье #2 «Пакет sync и атомики».
// Обычный прогон: go test ./sync/...
// Проверка гонок (в контейнере с cgo): go test -race ./sync/...
package synccookbook

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/goleak"
)

// TestMain ловит утёкшие горутины после всех тестов пакета.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const (
	testWorkers   = 8
	testPerWorker = 10_000
)

// TestMutexCounter — счётчик под мьютексом доходит до workers*perWorker.
func TestMutexCounter(t *testing.T) {
	var c MutexCounter
	runConcurrent(testWorkers, testPerWorker, c.Inc)
	want := int64(testWorkers * testPerWorker)
	if got := c.Value(); got != want {
		t.Fatalf("MutexCounter = %d, want %d", got, want)
	}
}

// TestAtomicCounter — атомарный счётчик даёт тот же итог.
func TestAtomicCounter(t *testing.T) {
	var c AtomicCounter
	runConcurrent(testWorkers, testPerWorker, c.Inc)
	want := int64(testWorkers * testPerWorker)
	if got := c.Value(); got != want {
		t.Fatalf("AtomicCounter = %d, want %d", got, want)
	}
}

// TestRWCache — конкурентные чтения и записи не роняют кэш и дают верную выдачу.
func TestRWCache(t *testing.T) {
	c := NewRWCache()
	var wg sync.WaitGroup

	// Писатели заполняют непересекающиеся ключи.
	for w := 0; w < testWorkers; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				key := fmt.Sprintf("w%d-%d", w, i)
				c.Set(key, key)
			}
		}()
	}
	// Читатели параллельно дёргают Get (промахи допустимы, паники — нет).
	for r := 0; r < testWorkers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				c.Get(fmt.Sprintf("w0-%d", i))
			}
		}()
	}
	wg.Wait()

	if got := c.Len(); got != testWorkers*100 {
		t.Fatalf("RWCache.Len = %d, want %d", got, testWorkers*100)
	}
	// Точечная проверка корректности выдачи.
	if v, ok := c.Get("w3-42"); !ok || v != "w3-42" {
		t.Fatalf("RWCache.Get(w3-42) = %q,%v; want w3-42,true", v, ok)
	}
}

// TestPoolReuse — буфер из пула переиспользуется и выдаётся чистым.
func TestPoolReuse(t *testing.T) {
	// Кладём «грязный» буфер и убеждаемся, что при выдаче он очищен.
	dirty := getBuffer()
	dirty.WriteString("остатки прошлого владельца")
	putBuffer(dirty)

	b := getBuffer()
	if b.Len() != 0 {
		t.Fatalf("буфер из пула не чист: len=%d", b.Len())
	}
	putBuffer(b)

	if got := RenderGreeting("мир"); got != "Привет, мир!" {
		t.Fatalf("RenderGreeting = %q", got)
	}
}

// TestPoolConcurrent — пул выдерживает конкурентное использование.
func TestPoolConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for w := 0; w < testWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				_ = RenderGreeting("x")
			}
		}()
	}
	wg.Wait()
}

// TestRegistry — sync.Map назначает ключу id один раз, конкурентно.
func TestRegistry(t *testing.T) {
	var r Registry
	var wg sync.WaitGroup

	// Много горутин просят один и тот же набор ключей одновременно.
	const keys = 50
	for w := 0; w < testWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < keys; i++ {
				r.GetOrAssign(fmt.Sprintf("k%d", i))
			}
		}()
	}
	wg.Wait()

	if got := r.Len(); got != keys {
		t.Fatalf("Registry.Len = %d, want %d", got, keys)
	}
	// id для одного ключа стабилен между обращениями.
	id1 := r.GetOrAssign("k7")
	id2 := r.GetOrAssign("k7")
	if id1 != id2 {
		t.Fatalf("id для k7 нестабилен: %d != %d", id1, id2)
	}
}

// TestShardedCounters — каждая горутина пишет свой ключ, чтения корректны.
func TestShardedCounters(t *testing.T) {
	var s ShardedCounters
	var wg sync.WaitGroup
	for w := 0; w < testWorkers; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Add(fmt.Sprintf("shard-%d", w), int64(w*10))
		}()
	}
	wg.Wait()

	for w := 0; w < testWorkers; w++ {
		v, ok := s.Get(fmt.Sprintf("shard-%d", w))
		if !ok || v != int64(w*10) {
			t.Fatalf("shard-%d = %d,%v; want %d,true", w, v, ok, w*10)
		}
	}
}

// TestAtomicMax — CAS-цикл сходится к настоящему максимуму под конкуренцией.
func TestAtomicMax(t *testing.T) {
	var m AtomicMax
	var wg sync.WaitGroup
	// Каждая горутина предъявляет свой диапазон; общий максимум известен.
	for w := 0; w < testWorkers; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				m.Observe(int64(w*1000 + i))
			}
		}()
	}
	wg.Wait()

	want := int64((testWorkers-1)*1000 + 999)
	if got := m.Max(); got != want {
		t.Fatalf("AtomicMax = %d, want %d", got, want)
	}
}

// TestOnce — инициализатор sync.Once вызывается ровно один раз.
func TestOnce(t *testing.T) {
	var calls atomic.Int64
	cfg := NewLazyConfig(func() string {
		calls.Add(1)
		return "value"
	})

	var wg sync.WaitGroup
	for w := 0; w < testWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := cfg.Get(); got != "value" {
				t.Errorf("LazyConfig.Get = %q", got)
			}
		}()
	}
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Fatalf("инициализатор вызван %d раз, want 1", n)
	}
}

// TestOnceValue — sync.OnceValue тоже инициализирует ровно один раз.
func TestOnceValue(t *testing.T) {
	var calls atomic.Int64
	get := NewOnceValueConfig(func() string {
		calls.Add(1)
		return "once-value"
	})

	var wg sync.WaitGroup
	for w := 0; w < testWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := get(); got != "once-value" {
				t.Errorf("OnceValue = %q", got)
			}
		}()
	}
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Fatalf("инициализатор OnceValue вызван %d раз, want 1", n)
	}
}
