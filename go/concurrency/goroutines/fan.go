// fan.go демонстрирует fan-out и fan-in.
//
// FanOut: N воркеров читают один общий входной канал; каждый элемент достаётся
// ровно одному воркеру (конкурентное чтение из канала распределяет элементы без
// дублей). Каждый воркер отдаёт результаты в собственный выходной канал.
//
// FanIn: слияние N каналов в один через WaitGroup + единственный close. Каждый
// элемент из любого источника проходит в общий выход ровно один раз; выход
// закрывается после того, как все источники исчерпаны или ctx отменён.
package goroutines

import (
	"context"
	"sync"
)

// FanOut запускает workers горутин, читающих общий канал in. Каждая горутина
// применяет fn и пишет в свой выходной канал; возвращается срез из workers
// каналов. Отмена ctx прерывает и приём, и отправку — горутины не зависают.
func FanOut[T, R any](
	ctx context.Context,
	workers int,
	in <-chan T,
	fn func(T) R,
) []<-chan R {
	if workers < 1 {
		workers = 1
	}
	outs := make([]<-chan R, workers)
	for i := range workers {
		out := make(chan R)
		outs[i] = out
		go func() {
			defer close(out)
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
		}()
	}
	return outs
}

// FanIn сливает несколько входных каналов в один. По горутине на источник;
// общий выход закрывается один раз после wg.Wait(). Отмена ctx прерывает
// отправку в общий выход, чтобы горутины-сливатели не зависали.
func FanIn[T any](ctx context.Context, ins ...<-chan T) <-chan T {
	out := make(chan T)
	var wg sync.WaitGroup
	wg.Add(len(ins))
	for _, in := range ins {
		go func() {
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
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
