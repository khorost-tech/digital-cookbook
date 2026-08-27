//go:build flaky

package timedep

import (
	"testing"
	"time"
)

// FLAKY: тест полагается на настоящие часы и Sleep для «синхронизации».
// Выпускаем токен на 20 мс, спим 19 мс, ждём, что он ещё годен. Запас 1 мс —
// планировщик/GC-пауза изредка его перепрыгивают, и тест мигает. Чем выше
// нагрузка (CI!), тем чаще: та же логика, разная частота — суть flaky.
// Запуск: go test -tags=flaky -count=200 ./timedep/
func TestValidAt_FlakyRealClock(t *testing.T) {
	tok := NewToken(time.Now(), 20*time.Millisecond)
	time.Sleep(19 * time.Millisecond) // «подождать почти до истечения»
	if !ValidAt(tok, time.Now()) {
		t.Fatal("токен неожиданно истёк — Sleep перепрыгнул TTL (недетерминизм времени)")
	}
}
