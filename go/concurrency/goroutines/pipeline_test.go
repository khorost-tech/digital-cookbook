// pipeline_test.go: каскадное завершение конвейера при закрытии входа и
// досрочная остановка через отмену контекста без утечек горутин.
package goroutines

import (
	"context"
	"testing"
)

func TestPipelineCascadingClose(t *testing.T) {
	ctx := context.Background()
	src := Generator(ctx, 1, 2, 3, 4, 5)
	doubled := Stage(ctx, src, func(v int) int { return v * 2 })
	got := Sink(ctx, doubled)

	want := []int{2, 4, 6, 8, 10}
	if len(got) != len(want) {
		t.Fatalf("получено %v, ожидалось %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("позиция %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestPipelineEarlyCancelNoLeak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// Бесконечный источник, чтобы проверить именно досрочную остановку.
	src := make(chan int)
	go func() {
		defer close(src)
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			case src <- i:
			}
		}
	}()

	stage := Stage(ctx, src, func(v int) int { return v + 1 })

	// Прочитаем часть и остановим конвейер отменой контекста.
	for range 5 {
		<-stage
	}
	cancel()

	// Дренируем выход стадии до закрытия — стадии каскадно завершатся.
	for range stage {
	}
}
