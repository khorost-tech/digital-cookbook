// Демо: одна отмена гасит всё поддерево горутин.
//
// От корневого контекста запускаем несколько «стадий», каждая — со своим
// дочерним контекстом. Один cancel() у корня закрывает Done во всех потомках.
//
// Запуск:
//
//	go run ./cmd/propagate
package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

func stage(ctx context.Context, name string, wg *sync.WaitGroup, log chan<- string) {
	defer wg.Done()
	<-ctx.Done()
	log <- fmt.Sprintf("  [%s] остановлена: %v", name, ctx.Err())
}

func main() {
	root, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	log := make(chan string, 3)

	// три стадии, каждая наследует отмену от общего корня
	for _, name := range []string{"fetch", "decode", "index"} {
		child, childCancel := context.WithCancel(root)
		defer childCancel()
		wg.Add(1)
		go stage(child, name, &wg, log)
	}

	fmt.Println("запущены три стадии на общем контексте, работают...")
	time.Sleep(30 * time.Millisecond)

	fmt.Println("cancel() у корня — одна отмена на всё дерево:")
	cancel()

	wg.Wait()
	close(log)
	for line := range log {
		fmt.Println(line)
	}

	fmt.Println("\nВывод: отмена течёт вниз по дереву. Держите контексты деревом,")
	fmt.Println("и одна точка отмены (таймаут запроса, сигнал завершения) погасит всю цепочку.")
}
