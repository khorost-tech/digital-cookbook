// flag_fixed_test.go — тесты к flag_fixed.go.
// Раздел «Как чинить: атомик как сигнал и закрытие канала как сигнал».
//
// Оба теста детерминированы и -race-safe: они лишь подтверждают, что
// починенные ожидания завершаются (сигнал доходит), тогда как сломанная
// версия из cmd/broken-flag могла бы зависнуть навсегда.
package memorymodel

import (
	"testing"
	"time"
)

// runWithTimeout запускает fn в отдельной горутине и падает, если она не
// завершилась за timeout (значит, сигнал не дошёл и busy-wait завис).
func runWithTimeout(t *testing.T, timeout time.Duration, fn func()) {
	t.Helper()
	finished := make(chan struct{})
	go func() {
		fn()
		close(finished)
	}()
	select {
	case <-finished:
		// ок: ожидание завершилось — сигнал стал видимым
	case <-time.After(timeout):
		t.Fatalf("ожидание не завершилось за %s: сигнал не дошёл", timeout)
	}
}

func TestWaitAtomicFlag(t *testing.T) {
	runWithTimeout(t, time.Second, WaitAtomicFlag)
}

func TestWaitChannelSignal(t *testing.T) {
	runWithTimeout(t, time.Second, WaitChannelSignal)
}
