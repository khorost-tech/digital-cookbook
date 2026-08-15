// fanout_fanin.go демонстрирует распараллеливание одного этапа (fan-out) и
// слияние результатов нескольких горутин в один канал (fan-in).
//
// Fan-out: N воркеров читают из общего входного канала и пишут в собственные
// выходы. Fan-in: отдельная горутина на каждый вход перекладывает элементы в
// общий выход, а sync.WaitGroup закрывает общий выход ровно один раз — после
// исчерпания всех входов. Все отправки обёрнуты в select с <-ctx.Done(), поэтому
// отмена не оставляет горутин.
package patterns

import (
	"context"
	"sync"
)

// FanOut запускает count воркеров: каждый читает из in, применяет fn и пишет в
// свой выходной канал. Возвращает срез выходных каналов (по одному на воркера).
func FanOut[T, R any](ctx context.Context, in <-chan T, count int, fn func(T) R) []<-chan R {
	outs := make([]<-chan R, count)
	for i := 0; i < count; i++ {
		out := make(chan R)
		outs[i] = out
		go func(out chan<- R) {
			defer close(out) // каждый воркер владеет своим выходом.
			for {
				select {
				case <-ctx.Done():
					return
				case v, ok := <-in:
					if !ok {
						return
					}
					select {
					case <-ctx.Done():
						return
					case out <- fn(v):
					}
				}
			}
		}(out)
	}
	return outs
}

// FanIn сливает несколько входных каналов в один. Общий выход закрывается один
// раз, когда все входы исчерпаны (координация через sync.WaitGroup).
func FanIn[T any](ctx context.Context, ins ...<-chan T) <-chan T {
	out := make(chan T)
	var wg sync.WaitGroup
	wg.Add(len(ins))
	for _, in := range ins {
		go func(in <-chan T) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case v, ok := <-in:
					if !ok {
						return
					}
					select {
					case <-ctx.Done():
						return
					case out <- v:
					}
				}
			}
		}(in)
	}
	// Закрываем общий выход только после завершения всех перекладчиков.
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
