package ratelimit

import (
	"testing"
	"time"
)

// TestBurstShape — форма реакции на всплеск: 10 запросов в один момент.
// Token bucket (ёмкость 5) пропускает всплеск до ёмкости — 5. Leaky bucket как
// строгий пейсер (ёмкость 1) пропускает 1 и сглаживает остальное в темп rate.
func TestBurstShape(t *testing.T) {
	token := NewTokenBucket(5, 5) // ёмкость 5, 5 токенов/сек
	if got := countAllowed(token, base, 10); got != 5 {
		t.Fatalf("token bucket пропустил %d из всплеска, ждали 5 (ёмкость)", got)
	}
	leaky := NewLeakyBucket(5, 1) // 5/сек, ёмкость 1 — строгое сглаживание
	if got := countAllowed(leaky, base, 10); got != 1 {
		t.Fatalf("leaky bucket пропустил %d из всплеска, ждали 1 (сглаживание)", got)
	}
}

// TestTokenRefill — после опустошения ведро пополняется по времени: за 1с при
// refill=5 набегает 5 токенов.
func TestTokenRefill(t *testing.T) {
	b := NewTokenBucket(5, 5)
	countAllowed(b, base, 5) // опустошили
	if b.AllowAt(base.Add(100 * time.Millisecond)).Allowed {
		t.Fatal("сразу после опустошения токенов быть не должно")
	}
	if got := countAllowed(b, base.Add(time.Second), 10); got != 5 {
		t.Fatalf("за секунду набежало %d токенов, ждали 5", got)
	}
}

// TestLeakySmoothsToRate — дырявое ведро выдаёт установившийся темп: по одному
// запросу на каждый интервал 1/rate, не больше.
func TestLeakySmoothsToRate(t *testing.T) {
	b := NewLeakyBucket(10, 1) // 10/сек → один каждые 100мс
	allowed := 0
	for ms := 0; ms < 1000; ms += 50 { // шлём каждые 50мс в течение секунды
		if b.AllowAt(base.Add(time.Duration(ms) * time.Millisecond)).Allowed {
			allowed++
		}
	}
	// при темпе 10/сек за секунду сглаженно проходит около 10 (не 20 попыток)
	if allowed < 9 || allowed > 11 {
		t.Fatalf("за секунду при 10/сек прошло %d, ждали ~10", allowed)
	}
}
