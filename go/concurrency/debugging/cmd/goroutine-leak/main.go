// Демо к статье #5 «Отладка: гонки, утечки, дедлоки»
// серии «Конкурентность в Go» (khorost.tech).
//
// Раздел «Утечка горутин: инструмент — pprof goroutine / runtime.NumGoroutine».
//
// СЛОМАННЫЙ пример. Функция leakyWorker запускает горутину, которая навсегда
// блокируется на приёме из канала. Отправитель отсутствует, канал никто не
// закроет — горутина никогда не разблокируется и не завершится. Каждый вызов
// leakyWorker оставляет ещё одну «зависшую» горутину: классическая утечка.
//
// Как запускать:
//
//	go run .
//
// Что увидишь (числа иллюстративны):
//
//	горутин на старте:       1
//	горутин после запусков:  6
//	=== goroutine dump ===
//	goroutine profile: total 6
//	5 @ 0x... 0x...
//	#	0x...	runtime.gopark ...
//	#	0x...	main.leakyWorker.func1 .../goroutine-leak/main.go:59  // <-ch
//	...
//
// Признаки утечки:
//   - runtime.NumGoroutine() растёт и НЕ падает обратно после «работы»;
//   - в дампе pprof.Lookup("goroutine") видно N одинаковых горутин, стоящих
//     на одной строке (здесь — приём из канала, `<-ch`), в состоянии chan receive.
//
// В реальном сервисе тот же дамп берут через net/http/pprof:
//
//	go tool pprof http://localhost:6060/debug/pprof/goroutine
//	(в pprof: команда `top`, затем `list main.leakyWorker`).
//
// Правильная версия — в leak_fixed.go: воркер слушает ctx.Done() и завершается
// по отмене; отсутствие утечки проверяется goleak в leak_fixed_test.go.
package main

import (
	"fmt"
	"runtime"
	"runtime/pprof"
	"os"
)

// leakyWorker запускает горутину, которая навсегда зависает на приёме из ch.
// Возвращаемый канал никогда не получит значение и не будет закрыт.
func leakyWorker() chan struct{} {
	ch := make(chan struct{})
	go func() {
		<-ch // блокировка навсегда: ни отправителя, ни close — утечка
		fmt.Println("этой строки мы не увидим")
	}()
	return ch
}

func main() {
	fmt.Printf("горутин на старте:      %d\n", runtime.NumGoroutine())

	// «Обрабатываем» пять задач — и теряем по горутине на каждой.
	for range 5 {
		_ = leakyWorker()
	}

	// Дать планировщику завести все горутины.
	runtime.Gosched()

	fmt.Printf("горутин после запусков: %d\n", runtime.NumGoroutine())

	fmt.Println("=== goroutine dump ===")
	// Полнотекстовый дамп всех горутин: видно, где именно они застряли.
	_ = pprof.Lookup("goroutine").WriteTo(os.Stdout, 1)
}
