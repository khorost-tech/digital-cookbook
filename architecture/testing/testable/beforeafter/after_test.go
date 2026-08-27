package beforeafter

import (
	"context"
	"errors"
	"testing"
	"time"
)

// --- Фейки швов: короткие, без «магии» мок-фреймворков ---

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

type seqIDGen struct{ id string }

func (g seqIDGen) NewID() string { return g.id }

// captureSender запоминает отправленное — так проверяют побочный эффект,
// не выходя в сеть.
type captureSender struct {
	calls []struct{ To, Text string }
	err   error
}

func (s *captureSender) Send(_ context.Context, to, text string) error {
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, struct{ To, Text string }{to, text})
	return nil
}

// Тест «после»: те же поля, но проверяются ТОЧНО — без допусков и префиксов.
// Часы фиксированы, ID предсказуем, отправка захвачена фейком.
func TestIssuer_Issue_Точно(t *testing.T) {
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sender := &captureSender{}
	iss := Issuer{
		Clock:  fixedClock{base},
		IDs:    seqIDGen{"CPN-000042"},
		Sender: sender,
		TTL:    24 * time.Hour,
	}

	got, err := iss.Issue(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// 1) ID — точное значение, а не префикс
	if got.ID != "CPN-000042" {
		t.Errorf("ID = %q, want CPN-000042", got.ID)
	}
	// 2) время — точное равенство, без допусков
	if !got.IssuedAt.Equal(base) {
		t.Errorf("IssuedAt = %v, want %v", got.IssuedAt, base)
	}
	if want := base.Add(24 * time.Hour); !got.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want)
	}
	// 3) побочный эффект — захвачен, без сети
	if len(sender.calls) != 1 {
		t.Fatalf("отправок = %d, want 1", len(sender.calls))
	}
	if sender.calls[0].To != "user-1" || sender.calls[0].Text != "Ваш купон CPN-000042" {
		t.Errorf("отправлено %+v", sender.calls[0])
	}
}

// Ветка ошибки отправки — раньше её было не достать вовсе (нужен был бы
// падающий сетевой вызов), теперь это одна строка в фейке.
func TestIssuer_Issue_ОшибкаОтправки(t *testing.T) {
	sendErr := errors.New("канал недоступен")
	iss := Issuer{
		Clock:  fixedClock{time.Now()},
		IDs:    seqIDGen{"CPN-000001"},
		Sender: &captureSender{err: sendErr},
		TTL:    time.Hour,
	}

	_, err := iss.Issue(context.Background(), "user-1")
	if !errors.Is(err, sendErr) {
		t.Fatalf("ждали обёрнутую ошибку отправки, получили %v", err)
	}
}

// Инвариант, который в варианте «до» был недоказуем: срок годности ровно
// IssuedAt+TTL. Там IssuedAt и ExpiresAt брались двумя разными вызовами
// time.Now() — точное равенство не выполнялось никогда.
func TestIssuer_ExpiresAt_РовноIssuedAtПлюсTTL(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, ttl := range []time.Duration{time.Minute, time.Hour, 24 * time.Hour, 30 * 24 * time.Hour} {
		iss := Issuer{Clock: fixedClock{base}, IDs: seqIDGen{"X"}, Sender: &captureSender{}, TTL: ttl}
		c, err := iss.Issue(context.Background(), "u")
		if err != nil {
			t.Fatal(err)
		}
		if !c.ExpiresAt.Equal(c.IssuedAt.Add(ttl)) {
			t.Errorf("TTL %v: ExpiresAt != IssuedAt+TTL", ttl)
		}
	}
}
