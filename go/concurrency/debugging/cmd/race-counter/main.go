// Демо к статье #5 «Отладка: гонки, утечки, дедлоки»
// серии «Конкурентность в Go» (khorost.tech).
//
// Раздел «Гонка данных: инструмент — детектор гонок (-race)».
//
// СЛОМАННЫЙ пример. Много горутин инкрементируют общую переменную counter
// без какой-либо синхронизации. Операция counter++ не атомарна (чтение —
// прибавление — запись), поэтому инкременты «теряются», а само обращение к
// общей памяти без happens-before — это гонка данных.
//
// Как запускать:
//
//	go run -race .
//
// Что увидишь (вывод иллюстративный, адреса и номера строк условны):
//
//	==================
//	WARNING: DATA RACE
//	Write at 0x00c000012345 by goroutine 8:
//	  main.main.func1()
//	      .../cmd/race-counter/main.go:44 +0x...   // counter++
//	Previous write at 0x00c000012345 by goroutine 7:
//	  main.main.func1()
//	      .../cmd/race-counter/main.go:44 +0x...   // counter++
//	Goroutine 8 (running) created at:
//	  main.main()
//	      .../cmd/race-counter/main.go:41 +0x...
//	==================
//	итог: 9873 (ожидалось 10000)   // потерянные инкременты
//	Found 1 data race(s)
//	exit status 66
//
// Как читать блок:
//   - «Write at 0x… by goroutine N» — кто и где ПРЯМО СЕЙЧАС трогает адрес;
//   - «Previous write/read … by goroutine M» — конфликтующий доступ ДО него;
//   - «created at» под каждой горутиной — где она была запущена (go ...).
//     Совпадение адресов сверху и снизу = та самая общая переменная.
//
// Без -race программа компилируется и завершается, но «итог» почти всегда
// меньше ожидаемого и меняется от запуска к запуску. Правильная версия —
// в counter_fixed.go (atomic.Int64), она проходит тесты под -race.
package main

import (
	"fmt"
	"sync"
)

func main() {
	const goroutines = 100
	const perGoroutine = 100

	var counter int // общий счётчик БЕЗ синхронизации
	var wg sync.WaitGroup

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perGoroutine {
				counter++ // гонка данных: незащищённые чтение+запись
			}
		}()
	}
	wg.Wait()

	fmt.Printf("итог: %d (ожидалось %d)\n", counter, goroutines*perGoroutine)
}
