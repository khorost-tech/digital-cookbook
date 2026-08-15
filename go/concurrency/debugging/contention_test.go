// contention_test.go: как СНЯТЬ профили блокировок под контеншеном.
//
// Тест намеренно создаёт конкуренцию за один мьютекс (много горутин дерутся за
// один Lock) и один небуферизованный канал, включает block- и mutex-профили и
// записывает block.pprof / mutex.pprof во временный каталог теста (t.TempDir(),
// который go сам удалит). Тест ПРОХОДИТ — он ничего не «ломает», а показывает
// рабочий рецепт сбора профилей.
//
// Прогон:  go test -run TestContentionProfiles -v ./debugging/...
//
// Как смотреть собранные профили (пути печатаются в лог теста):
//
//	go tool pprof <path>/mutex.pprof
//	  (в pprof: `top` — где дольше всего ждали мьютекс; `list contend` — по строкам)
//	go tool pprof <path>/block.pprof
//	  (то же для блокировок на каналах/примитивах синхронизации)
//
// Настройки:
//   - runtime.SetMutexProfileFraction(n): профилировать 1 из n событий на мьютексе
//     (n=1 — все; 0 — выключить). По умолчанию 0.
//   - runtime.SetBlockProfileRate(n): семплировать блокировки длительностью ~n нс
//     (1 — максимально подробно; 0 — выключить). По умолчанию 0.
// Оба профиля по умолчанию ВЫКЛЮЧЕНЫ — их нужно включать явно, лучше временно,
// т.к. подробное семплирование добавляет накладные расходы.
package debugging

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"testing"
)

func TestContentionProfiles(t *testing.T) {
	// Включаем оба профиля. defer возвращает настройки к «выключено», чтобы не
	// влиять на другие тесты пакета.
	runtime.SetMutexProfileFraction(1)
	defer runtime.SetMutexProfileFraction(0)
	runtime.SetBlockProfileRate(1)
	defer runtime.SetBlockProfileRate(0)

	// Генерируем контеншен: 50 горутин дерутся за один мьютекс...
	contend()

	dir := t.TempDir()
	writeProfile(t, "mutex", filepath.Join(dir, "mutex.pprof"))
	writeProfile(t, "block", filepath.Join(dir, "block.pprof"))

	t.Logf("профили записаны в %s (mutex.pprof, block.pprof)", dir)
	t.Log("смотреть: go tool pprof <файл>, затем команды top / list")
}

// contend создаёт заметную конкуренцию за общий мьютекс и небуферизованный
// канал, чтобы в профилях появились события.
func contend() {
	const workers = 50
	const iterations = 200

	var mu sync.Mutex
	shared := 0

	ch := make(chan int) // небуферизованный: отправка/приём порождают block-события

	// Читатель канала.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range workers * iterations {
			<-ch
		}
	}()

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				mu.Lock() // все воркеры дерутся за один замок -> mutex-профиль
				shared++
				v := shared // читаем под тем же замком, иначе гонка с чужим shared++
				mu.Unlock()
				ch <- v // блокировка на канале -> block-профиль
			}
		}()
	}
	wg.Wait()
	<-done
}

// writeProfile сбрасывает именованный профиль в файл.
func writeProfile(t *testing.T, name, path string) {
	t.Helper()
	p := pprof.Lookup(name)
	if p == nil {
		t.Fatalf("профиль %q недоступен", name)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("создать %s: %v", path, err)
	}
	defer f.Close()
	if err := p.WriteTo(f, 0); err != nil {
		t.Fatalf("записать профиль %s: %v", name, err)
	}
}
