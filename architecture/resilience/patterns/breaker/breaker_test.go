package breaker

import (
	"testing"
	"time"
)

var t0 = time.Unix(2000, 0)

// TestTripsAfterThreshold — после N ошибок подряд цепь размыкается и вызовы
// перестают пропускаться.
func TestTripsAfterThreshold(t *testing.T) {
	b := New(3, time.Second, 2)
	for i := 0; i < 3; i++ {
		if !b.AllowAt(t0) {
			t.Fatalf("вызов %d должен пройти (closed)", i)
		}
		b.Record(t0, false)
	}
	if b.State() != Open {
		t.Fatalf("после 3 ошибок ждали open, получили %s", b.State())
	}
	if b.AllowAt(t0) {
		t.Fatal("в open вызовы не должны пропускаться")
	}
}

// TestSuccessResetsFailures — успех обнуляет счётчик, до размыкания не доходит.
func TestSuccessResetsFailures(t *testing.T) {
	b := New(3, time.Second, 2)
	b.Record(t0, false)
	b.Record(t0, false)
	b.Record(t0, true) // сброс
	b.Record(t0, false)
	b.Record(t0, false)
	if b.State() != Closed {
		t.Fatalf("два+сброс+два не должны размыкать; состояние %s", b.State())
	}
}

// TestHalfOpenRecovery — после паузы open → half-open; удачные пробы закрывают цепь.
func TestHalfOpenRecovery(t *testing.T) {
	b := New(1, 5*time.Second, 2)
	b.Record(t0, false) // → open
	if b.AllowAt(t0.Add(time.Second)) {
		t.Fatal("до истечения паузы вызовы не пропускаются")
	}
	after := t0.Add(6 * time.Second)
	if !b.AllowAt(after) || b.State() != HalfOpen {
		t.Fatalf("после паузы ждали half-open с пропуском, состояние %s", b.State())
	}
	b.Record(after, true)  // первая проба ок
	if !b.AllowAt(after) { // вторая проба
		t.Fatal("вторая проба должна пройти")
	}
	b.Record(after, true) // все пробы ок → closed
	if b.State() != Closed {
		t.Fatalf("после удачных проб ждали closed, получили %s", b.State())
	}
}

// TestHalfOpenReopensOnFail — провал пробы в half-open снова размыкает цепь.
func TestHalfOpenReopensOnFail(t *testing.T) {
	b := New(1, 5*time.Second, 2)
	b.Record(t0, false) // open
	after := t0.Add(6 * time.Second)
	b.AllowAt(after) // → half-open, проба
	b.Record(after, false)
	if b.State() != Open {
		t.Fatalf("провал пробы должен вернуть в open, состояние %s", b.State())
	}
}

// TestHalfOpenLimitsProbes — в half-open пропускается не больше halfOpenMax проб.
func TestHalfOpenLimitsProbes(t *testing.T) {
	b := New(1, time.Second, 2)
	b.Record(t0, false)
	after := t0.Add(2 * time.Second)
	if !b.AllowAt(after) {
		t.Fatal("первая проба должна пройти")
	}
	if !b.AllowAt(after) {
		t.Fatal("вторая проба должна пройти")
	}
	if b.AllowAt(after) {
		t.Fatal("третья проба сверх halfOpenMax=2 не должна пройти")
	}
}
