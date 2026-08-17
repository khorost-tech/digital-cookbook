package race

import (
	"os"
	"sync"
	"testing"
)

// TestUnsyncCache_Race конкурентно пишет и читает один и тот же ключ
// несинхронизированного кэша. Без -race тест может пройти (гонка
// недетерминирована и не гарантированно приводит к panic/сбою). Под -race
// детектор — динамический: он инструментирует доступы и рапортует о гонке
// на реально исполненном конкурентном пути; при такой нагрузке отчёт
// WARNING: DATA RACE весьма вероятен, но формальной гарантии «поймает на
// любом прогоне» нет — это и есть артефакт задачи.
//
// По умолчанию тест пропускается (SKIP), чтобы основной прогон
// `go test ./... -short` и `go test -race ./...` оставались зелёными:
// без -race конкурентная запись в map под такой нагрузкой с очень высокой
// вероятностью (но не гарантированно — зависит от реально исполненного
// конкурентного пути) роняет процесс фаталом рантайма
// `fatal error: concurrent map writes` (это не panic, recover его не ловит
// — упал бы весь `go test ./...`), а с -race тест намеренно и ожидаемо
// FAIL (сам детектор гонки — это и есть демонстрация).
// Чтобы увидеть гонку вживую:
//
//	DEMO_RACE=1 go test ./race/ -run TestUnsyncCache_Race -race -v
func TestUnsyncCache_Race(t *testing.T) {
	if os.Getenv("DEMO_RACE") == "" {
		t.Skip("демо гонки: включить DEMO_RACE=1; под -race ловит DATA RACE, без -race роняет fatal concurrent map writes")
	}

	c := NewUnsyncCache()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			c.Set("k", "v")
		}()
		go func() {
			defer wg.Done()
			_ = c.Get("k")
		}()
	}
	wg.Wait()
}

// TestSyncCache_NoRace — та же конкурентная нагрузка на синхронизированный
// кэш (под sync.RWMutex). Под -race должен проходить чисто, без
// предупреждений детектора.
func TestSyncCache_NoRace(t *testing.T) {
	c := NewSyncCache()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			c.Set("k", "v")
		}()
		go func() {
			defer wg.Done()
			_ = c.Get("k")
		}()
	}
	wg.Wait()
}
