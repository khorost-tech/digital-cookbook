package retry

import (
	"testing"
	"time"
)

// TestBackoffSchedule — расписание удвоения с потолком.
func TestBackoffSchedule(t *testing.T) {
	base := 100 * time.Millisecond
	maxDelay := time.Second
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, time.Second, time.Second}
	for attempt, w := range want {
		if got := Backoff(attempt, base, maxDelay); got != w {
			t.Fatalf("Backoff(%d) = %v, ждали %v", attempt, got, w)
		}
	}
}

// TestFullJitterBounds — джиттер всегда в [0, Backoff]: rnd=0 → 0, rnd→1 → почти вся задержка.
func TestFullJitterBounds(t *testing.T) {
	base, maxDelay := 100*time.Millisecond, time.Second
	for attempt := 0; attempt < 5; attempt++ {
		full := Backoff(attempt, base, maxDelay)
		if d := FullJitter(attempt, base, maxDelay, 0); d != 0 {
			t.Fatalf("rnd=0 должен дать 0, получили %v", d)
		}
		if d := FullJitter(attempt, base, maxDelay, 0.999); d <= 0 || d > full {
			t.Fatalf("rnd≈1: ждали (0, %v], получили %v", full, d)
		}
		if d := FullJitter(attempt, base, maxDelay, 0.5); d < 0 || d > full {
			t.Fatalf("rnd=0.5 вне [0, %v]: %v", full, d)
		}
	}
}

// TestBudgetCapsRetries — бюджет ограничивает число повторов: при ratio 0.1
// десять обычных запросов дают ровно один допустимый повтор.
func TestBudgetCapsRetries(t *testing.T) {
	b := NewBudget(0.1, 10) // старт с нуля токенов, потолок 10
	for i := 0; i < 10; i++ {
		b.OnRequest()
	}
	if !b.TryRetry() {
		t.Fatal("после 10 запросов при ratio 0.1 один повтор должен быть разрешён")
	}
	if b.TryRetry() {
		t.Fatal("второй повтор должен быть отклонён — бюджет исчерпан")
	}
}

// TestBudgetDampensStorm — на 100 обычных запросов при ratio 0.1 приходится не
// больше ~10 повторов, а не 100×maxRetries, как у наивного повтора.
func TestBudgetDampensStorm(t *testing.T) {
	b := NewBudget(0.1, 100)
	retries := 0
	for i := 0; i < 100; i++ {
		b.OnRequest()
		// наивно хотели бы повторить 3 раза на каждый; бюджет разрешит немногим:
		for r := 0; r < 3; r++ {
			if b.TryRetry() {
				retries++
			}
		}
	}
	if retries > 12 {
		t.Fatalf("бюджет пропустил %d повторов, ждали ~10 (не 300)", retries)
	}
}
