// Демо: ранний break из range по iter.Seq выполняет очистку итератора.
//
// Итератор держит «ресурс» (defer cleanup). Читатель прерывает обход через
// break на середине — cleanup всё равно срабатывает, потому что range
// передаёт итератору false из yield, тот выходит и разматывает defer.
//
// Запуск:
//
//	go run ./cmd/break-cleanup
package main

import (
	"fmt"

	"tech.khorost/go-iterators-cookbook/seq"
)

func main() {
	fmt.Print("итератор открывает ресурс и обязан закрыть его при любом выходе\n\n")

	it := seq.WithCleanup(seq.Count(100), func() {
		fmt.Println(">>> cleanup: ресурс закрыт (defer в итераторе)")
	})

	fmt.Println("читаем и прерываемся на 3-м элементе:")
	for v := range it {
		fmt.Printf("  получили %d\n", v)
		if v == 2 {
			fmt.Println("  break")
			break
		}
	}

	fmt.Println("\nВывод: несмотря на break, cleanup выполнился. range-over-func")
	fmt.Println("сообщает итератору об остановке (yield вернул false), и defer отработал.")
	fmt.Println("Поэтому файлы/строки БД/соединения можно закрывать внутри итератора.")
}
