package timedep

import (
	"testing"
	"time"
)

// ФИКС: время подаётся явно, теста часов нет — результат детерминирован при
// любом числе прогонов. Никаких time.Now()/Sleep.
func TestValidAt_Deterministic(t *testing.T) {
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	tok := NewToken(base, 30*time.Minute)

	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"до истечения — годен", base.Add(10 * time.Minute), true},
		{"за миг до истечения — годен", base.Add(30*time.Minute - time.Nanosecond), true},
		{"ровно в момент истечения — не годен", base.Add(30 * time.Minute), false},
		{"после истечения — не годен", base.Add(31 * time.Minute), false},
	}
	for _, c := range cases {
		if got := ValidAt(tok, c.now); got != c.want {
			t.Errorf("%s: ValidAt = %v, want %v", c.name, got, c.want)
		}
	}
}
