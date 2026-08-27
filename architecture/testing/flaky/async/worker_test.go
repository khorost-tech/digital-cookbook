package async

import "testing"

// ФИКС: ждём результат из канала — синхронизация по готовности, не по времени.
// Детерминированно при любом числе прогонов и любой загрузке машины.
func TestCompute_ChannelSync(t *testing.T) {
	got := <-Compute(9) // блокируемся ровно до готовности
	if got != 81 {
		t.Fatalf("Compute(9) = %d, want 81", got)
	}
}
