// bridge_test.go проверяет, что Bridge разворачивает канал каналов в один
// плоский поток в правильном порядке, и что отмена не оставляет утечек.
package patterns

import (
	"context"
	"testing"
)

// genStream порождает поток каналов: по одному вложенному каналу на каждый
// переданный срез. Внешний канал закрывается после отправки всех вложенных.
func genStream(ctx context.Context, batches ...[]int) <-chan <-chan int {
	stream := make(chan (<-chan int))
	go func() {
		defer close(stream)
		for _, b := range batches {
			inner := Source(ctx, b...)
			select {
			case <-ctx.Done():
				return
			case stream <- inner:
			}
		}
	}()
	return stream
}

func TestBridge(t *testing.T) {
	ctx := context.Background()

	stream := genStream(ctx, []int{1, 2}, []int{3}, []int{4, 5, 6})

	var got []int
	for v := range Bridge(ctx, stream) {
		got = append(got, v)
	}

	want := []int{1, 2, 3, 4, 5, 6}
	if len(got) != len(want) {
		t.Fatalf("длина результата: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("элемент %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

// TestBridgeCancel: отмена ctx до вычитывания всего потока не должна оставлять
// висящих горутин.
func TestBridgeCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	stream := genStream(ctx, []int{1, 2, 3}, []int{4, 5, 6}, []int{7, 8, 9})
	out := Bridge(ctx, stream)

	<-out
	cancel()
	Drain(ctx, out)
}
