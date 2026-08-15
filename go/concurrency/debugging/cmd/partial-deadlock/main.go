// Демо к статье #5 «Отладка: гонки, утечки, дедлоки»
// серии «Конкурентность в Go» (khorost.tech).
//
// Раздел «Частичный дедлок: инструмент — goroutine-дамп (детектор рантайма МОЛЧИТ)».
//
// СЛОМАННЫЙ пример. Классический lock-ordering deadlock: две горутины берут
// два мьютекса в ПРОТИВОПОЛОЖНОМ порядке.
//   - g1: Lock(muA) → Lock(muB)
//   - g2: Lock(muB) → Lock(muA)
// При неудачном чередовании g1 держит muA и ждёт muB, а g2 держит muB и ждёт
// muA — взаимная блокировка. Но main-горутина продолжает жить (в примере она
// раз в секунду печатает, что ещё «жива»), поэтому спят НЕ все горутины —
// встроенный детектор рантайма НЕ срабатывает и `all goroutines are asleep`
// НЕ печатается. Программа просто зависает навсегда.
//
// Как запускать:
//
//	go run .
//
// Вывод (программа висит, детектор молчит):
//
//	живём, но две горутины уже, вероятно, в клинче...
//	живём, но две горутины уже, вероятно, в клинче...
//	...
//
// Как ДИАГНОСТИРОВАТЬ частичный дедлок — снять goroutine-дамп:
//
//	Вариант A (сигналом, Linux/macOS): послать процессу SIGQUIT (Ctrl-\)
//	  или запустить с GOTRACEBACK=all — рантайм печатает стек ВСЕХ горутин.
//	Вариант B (кросс-платформенно): раскомментировать блок dumpAfter ниже —
//	  через несколько секунд программа сама сбросит дамп через
//	  pprof.Lookup("goroutine").WriteTo(os.Stdout, 1).
//
// Что искать в дампе (иллюстративно):
//
//	goroutine 6 [sync.Mutex.Lock]:
//	  main.main.func1() .../cmd/partial-deadlock/main.go:74   // muB.Lock()
//	goroutine 7 [sync.Mutex.Lock]:
//	  main.main.func2() .../cmd/partial-deadlock/main.go:85   // muA.Lock()
//
// Две горутины в состоянии [sync.Mutex.Lock], каждая стоит на Lock второго
// мьютекса — это и есть перекрёстный захват. Лечится единым порядком взятия
// замков (обе горутины: сначала muA, потом muB).
package main

import (
	"fmt"
	"sync"
	"time"
	// "os"
	// "runtime/pprof"
)

func main() {
	var muA, muB sync.Mutex

	go func() { // g1: порядок muA -> muB
		muA.Lock()
		time.Sleep(10 * time.Millisecond) // даём g2 успеть взять muB
		muB.Lock()                        // зависнет здесь: muB держит g2
		muB.Unlock()
		muA.Unlock()
	}()

	go func() { // g2: порядок muB -> muA (противоположный!)
		muB.Lock()
		time.Sleep(10 * time.Millisecond) // даём g1 успеть взять muA
		muA.Lock()                        // зависнет здесь: muA держит g1
		muA.Unlock()
		muB.Unlock()
	}()

	// main НЕ спит — поэтому детектор рантайма не считает это дедлоком.
	// Раскомментируй dumpAfter, чтобы программа сама сбросила goroutine-дамп.
	//
	// go dumpAfter(3 * time.Second)

	for {
		fmt.Println("живём, но две горутины уже, вероятно, в клинче...")
		time.Sleep(1 * time.Second)
	}
}

// dumpAfter спустя d печатает полный goroutine-дамп в stdout. Кросс-платформенная
// замена SIGQUIT: удобно на Windows, где нет Ctrl-\.
//
// func dumpAfter(d time.Duration) {
// 	time.Sleep(d)
// 	fmt.Println("=== goroutine dump ===")
// 	_ = pprof.Lookup("goroutine").WriteTo(os.Stdout, 1)
// }
