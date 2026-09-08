package locking

import (
	"errors"
	"testing"
)

// Сценарий во всех тестах один (Клеппманн):
//  1. владелец A берёт лок, получает токен 1;
//  2. A «засыпает», TTL истекает, лок переходит к B с токеном 2;
//  3. B пишет свои данные (токен 2);
//  4. A просыпается, всё ещё думает, что держит лок, и пишет со старым токеном 1.
// Разница — в защите ресурса.

// TestUnfencedCorrupts — без fencing запоздавшая запись A затирает данные B.
func TestUnfencedCorrupts(t *testing.T) {
	res := &UnfencedResource{}
	res.Write("B-data")  // B, «токен 2»
	res.Write("A-stale") // A проснулся и затёр — токенов нет, некому отвергнуть
	if got := res.Value(); got != "A-stale" {
		t.Fatalf("ожидали порчу (A-stale), получили %q", got)
	}
	// Показали проблему: свежие данные B потеряны.
}

// TestFencedRejectsStale — с fencing запись A с токеном 1 отвергается, данные B целы.
func TestFencedRejectsStale(t *testing.T) {
	res := &FencedResource{}
	if err := res.Write(2, "B-data"); err != nil {
		t.Fatalf("запись B (токен 2) должна пройти: %v", err)
	}
	err := res.Write(1, "A-stale") // A со старым токеном
	if !errors.Is(err, ErrStaleToken) {
		t.Fatalf("ждали ErrStaleToken, получили %v", err)
	}
	if got := res.Value(); got != "B-data" {
		t.Fatalf("данные B должны уцелеть, получили %q", got)
	}
}

// TestFencedAcceptsEqualAndNewer — токен, равный или больший текущего, проходит.
func TestFencedAcceptsEqualAndNewer(t *testing.T) {
	res := &FencedResource{}
	for _, tok := range []uint64{1, 1, 2, 5} {
		if err := res.Write(tok, "v"); err != nil {
			t.Fatalf("токен %d должен пройти: %v", tok, err)
		}
	}
	if err := res.Write(4, "late"); !errors.Is(err, ErrStaleToken) {
		t.Fatalf("токен 4 после 5 должен быть отвергнут, получили %v", err)
	}
}
