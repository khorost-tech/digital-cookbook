// Демо: утечка горутины, если не пробросить и не проверять context.
//
// worker без отмены блокируется навсегда, когда получатель ушёл, — горутина
// висит до конца процесса. Тот же worker с select по ctx.Done() завершается.
//
// Запуск:
//
//	go run ./cmd/leak
package main

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

// leakyWorker шлёт значения в out и не знает про отмену. Если читатель
// перестал читать, отправка в небуферизованный канал блокируется навсегда.
func leakyWorker(out chan<- int) {
	i := 0
	for {
		out <- i // некому читать → блокировка навсегда
		i++
	}
}

// politeWorker умеет останавливаться: на каждой итерации проверяет ctx.Done().
func politeWorker(ctx context.Context, out chan<- int) {
	i := 0
	for {
		select {
		case <-ctx.Done():
			return // отменили — выходим, горутина освобождается
		case out <- i:
			i++
		}
	}
}

func main() {
	base := runtime.NumGoroutine()
	fmt.Printf("горутин в начале: %d\n\n", base)

	// --- утечка ---
	leakOut := make(chan int)
	go leakyWorker(leakOut)
	<-leakOut // прочитали одно значение и ушли
	time.Sleep(20 * time.Millisecond)
	fmt.Printf("после leakyWorker (читатель ушёл): %d горутин — worker навсегда завис на send\n",
		runtime.NumGoroutine())

	// --- корректная остановка ---
	ctx, cancel := context.WithCancel(context.Background())
	politeOut := make(chan int)
	go politeWorker(ctx, politeOut)
	<-politeOut // прочитали одно значение
	cancel()    // сообщаем: больше не нужно
	time.Sleep(20 * time.Millisecond)
	fmt.Printf("после politeWorker + cancel():           %d горутин — politeWorker вышел по ctx.Done();\n",
		runtime.NumGoroutine())
	fmt.Println("                                        остаётся main + всё ещё висящий leakyWorker")

	fmt.Println("\nВывод: без отмены и проверки ctx.Done() горутина-производитель")
	fmt.Println("зависает, когда потребитель ушёл. context — это протокол «пора останавливаться».")
}
