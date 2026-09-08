//go:build integration

// Интеграционный тест: нужен Docker. Запуск: go test -tags integration ./...
package locking

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func startRedis(t *testing.T) redis.Cmdable {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcredis.Run(ctx, "redis:7.4-alpine")
	if err != nil {
		t.Fatalf("старт Redis: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	endpoint, err := ctr.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: endpoint})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// TestDoubleOwnershipFencedSafe — базовый сценарий: лок с TTL достаётся второму
// владельцу, пока первый «спит», и fencing-токены защищают ресурс от порчи.
func TestDoubleOwnershipFencedSafe(t *testing.T) {
	ctx := context.Background()
	rdb := startRedis(t)
	const ttl = 200 * time.Millisecond
	lock := NewNaiveLock(rdb, "job:lock", "job:fence")
	res := &FencedResource{}

	tokenA, okA, err := lock.AcquireWithToken(ctx, "A", ttl)
	if err != nil || !okA {
		t.Fatalf("A должен взять лок: ok=%v err=%v", okA, err)
	}
	time.Sleep(ttl + 300*time.Millisecond) // TTL истекает, пока A спит

	tokenB, okB, err := lock.AcquireWithToken(ctx, "B", ttl)
	if err != nil || !okB {
		t.Fatalf("B должен взять лок после TTL: ok=%v err=%v", okB, err)
	}
	if tokenB <= tokenA {
		t.Fatalf("токен B (%d) должен быть больше токена A (%d)", tokenB, tokenA)
	}

	if err := res.Write(tokenB, "B-data"); err != nil {
		t.Fatalf("запись B должна пройти: %v", err)
	}
	if err := res.Write(tokenA, "A-stale"); !errors.Is(err, ErrStaleToken) {
		t.Fatalf("устаревшая запись A должна быть отвергнута, получили %v", err)
	}
	if got := res.Value(); got != "B-data" {
		t.Fatalf("ресурс испорчен: %q вместо B-data", got)
	}
}

// TestUnsafeTokenIssuanceCorrupts — ПОЧЕМУ токен обязан выдаваться атомарно с локом.
// Воспроизводим чередование: A берёт лок, засыпает ДО получения токена; за это время
// TTL истекает, B берёт лок с токеном и пишет; A просыпается и получает токен БОЛЬШЕ,
// уже не владея локом, — и затирает данные B, хотя fencing «включён».
func TestUnsafeTokenIssuanceCorrupts(t *testing.T) {
	ctx := context.Background()
	rdb := startRedis(t)
	const ttl = 200 * time.Millisecond
	lock := NewNaiveLock(rdb, "job:lock", "job:fence")
	res := &FencedResource{}

	var tokenB uint64
	// Пауза A: за неё TTL истекает и B успевает взять лок, токен и записать.
	pause := func() {
		time.Sleep(ttl + 200*time.Millisecond)
		var ok bool
		var err error
		tokenB, ok, err = lock.AcquireWithToken(ctx, "B", 5*time.Second)
		if err != nil || !ok {
			t.Errorf("B должен взять освободившийся лок: ok=%v err=%v", ok, err)
			return
		}
		if err := res.Write(tokenB, "B-data"); err != nil {
			t.Errorf("запись B: %v", err)
		}
	}

	tokenA, okA, err := lock.AcquireThenTokenUnsafe(ctx, "A", ttl, pause)
	if err != nil || !okA {
		t.Fatalf("A должен взять лок: ok=%v err=%v", okA, err)
	}

	if tokenA <= tokenB {
		t.Fatalf("для воспроизведения дефекта нужен токен A (%d) больше токена B (%d)", tokenA, tokenB)
	}
	// A уже НЕ владеет локом, но его токен больше — ресурс принимает запись.
	if err := res.Write(tokenA, "A-stale"); err != nil {
		t.Fatalf("ресурс принял бы запись A (в этом и дефект), получили ошибку %v", err)
	}
	if got := res.Value(); got != "A-stale" {
		t.Fatalf("ожидали воспроизведение порчи (A-stale), получили %q", got)
	}
	t.Logf("дефект воспроизведён: A(токен %d) не владеет локом, но перезаписал B(токен %d) — "+
		"поэтому лок и токен выдаются одной атомарной операцией (AcquireWithToken)", tokenA, tokenB)
}

// TestTokenNotIssuedWithoutLock — атомарный вариант не выдаёт токен тому, кто лок
// не получил: счётчик не двигается, «обогнать» владельца токеном невозможно.
func TestTokenNotIssuedWithoutLock(t *testing.T) {
	ctx := context.Background()
	rdb := startRedis(t)
	lock := NewNaiveLock(rdb, "k", "k:fence")

	tokenA, okA, err := lock.AcquireWithToken(ctx, "A", time.Minute)
	if err != nil || !okA {
		t.Fatalf("A должен взять лок: %v", err)
	}
	tokenB, okB, err := lock.AcquireWithToken(ctx, "B", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if okB {
		t.Fatal("B не должен взять занятый лок")
	}
	if tokenB != 0 {
		t.Fatalf("B не должен получить токен, получил %d", tokenB)
	}
	// Счётчик не сдвинулся: следующий законный владелец получит tokenA+1.
	if _, err := rdb.Get(ctx, "k:fence").Int64(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(ctx, "A"); err != nil {
		t.Fatal(err)
	}
	tokenC, okC, err := lock.AcquireWithToken(ctx, "C", time.Minute)
	if err != nil || !okC {
		t.Fatalf("C должен взять освободившийся лок: %v", err)
	}
	if tokenC != tokenA+1 {
		t.Fatalf("токен C = %d, ждали %d (счётчик не должен был двигаться от неудачных попыток)", tokenC, tokenA+1)
	}
}

// TestUnlockOnlyOwn — Unlock не снимает чужой лок.
func TestUnlockOnlyOwn(t *testing.T) {
	ctx := context.Background()
	rdb := startRedis(t)
	lock := NewNaiveLock(rdb, "k", "k:fence")
	if _, ok, err := lock.AcquireWithToken(ctx, "A", time.Minute); err != nil || !ok {
		t.Fatalf("A должен взять лок: %v", err)
	}
	if err := lock.Unlock(ctx, "B"); err != nil { // чужой owner
		t.Fatal(err)
	}
	if _, ok, err := lock.AcquireWithToken(ctx, "C", time.Minute); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("чужой Unlock не должен был освободить лок A")
	}
}

// TestFenceCounterErrorLeavesNoLock — граница гарантии Lua: изоляция есть, отката
// нет. Если счётчик токенов занят нечисловым значением, INCR падает уже ПОСЛЕ того,
// как лок поставлен. Скрипт обязан снять его сам, иначе вызывающий получит ошибку,
// а ресурс останется заблокированным до истечения TTL.
func TestFenceCounterErrorLeavesNoLock(t *testing.T) {
	ctx := context.Background()
	rdb := startRedis(t)
	lockKey, fenceKey, err := KeysForResource("job")
	if err != nil {
		t.Fatal(err)
	}
	if err = rdb.Set(ctx, fenceKey, "не число", 0).Err(); err != nil {
		t.Fatal(err)
	}
	lock := NewNaiveLock(rdb, lockKey, fenceKey)

	token, ok, err := lock.AcquireWithToken(ctx, "A", time.Minute)
	if err == nil {
		t.Fatalf("ждали ошибку из-за испорченного счётчика, получили token=%d ok=%v", token, ok)
	}
	if ok {
		t.Fatal("при ошибке захват не должен считаться успешным")
	}
	// Главное: лок не остался висеть.
	n, err := rdb.Exists(ctx, lockKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("лок остался после ошибки выдачи токена — ресурс заблокирован до TTL")
	}
	// После починки счётчика захват снова работает.
	if err := rdb.Del(ctx, fenceKey).Err(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := lock.AcquireWithToken(ctx, "A", time.Minute); err != nil || !ok {
		t.Fatalf("после починки счётчика захват должен пройти: ok=%v err=%v", ok, err)
	}
}

// crc16 — та самая CCITT/XMODEM-таблица, по которой Redis Cluster считает слот.
func crc16(s string) uint16 {
	var crc uint16
	for i := 0; i < len(s); i++ {
		crc ^= uint16(s[i]) << 8
		for j := 0; j < 8; j++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// slot повторяет правило Redis: если в ключе есть НЕПУСТОЙ hash tag {…},
// слот считается по нему, иначе по всему ключу.
func slot(key string) uint16 {
	if i := strings.IndexByte(key, '{'); i >= 0 {
		if j := strings.IndexByte(key[i+1:], '}'); j > 0 {
			key = key[i+1 : i+1+j]
		}
	}
	return crc16(key) % 16384
}

// TestKeysForResourceSameSlot — проверяем НАСТОЯЩИЙ контракт (совпадение слота),
// а не префикс строки: ключи одного Lua-скрипта обязаны попасть в один слот кластера.
func TestKeysForResourceSameSlot(t *testing.T) {
	for _, res := range []string{"orders", "a", "очень-длинное-имя-ресурса", "job:42"} {
		lockKey, fenceKey, err := KeysForResource(res)
		if err != nil {
			t.Fatalf("%q: неожиданная ошибка %v", res, err)
		}
		if s1, s2 := slot(lockKey), slot(fenceKey); s1 != s2 {
			t.Fatalf("%q: ключи в разных слотах: %d и %d (%s / %s)", res, s1, s2, lockKey, fenceKey)
		}
	}
}

// TestKeysForResourceRejectsBadNames — пустой тег Redis игнорирует, и `{}:lock`
// с `{}:fence` уезжают в РАЗНЫЕ слоты; имя со своими скобками ломается так же.
// Поэтому такие имена отвергаются, а не принимаются молча.
func TestKeysForResourceRejectsBadNames(t *testing.T) {
	for _, bad := range []string{"", "a{b", "a}b", "{x}"} {
		if _, _, err := KeysForResource(bad); !errors.Is(err, ErrBadResource) {
			t.Fatalf("имя %q должно быть отвергнуто, получили err=%v", bad, err)
		}
	}
	// Контрольная проверка того, ПОЧЕМУ пустой тег недопустим.
	if slot("{}:lock") == slot("{}:fence") {
		t.Fatal("ожидали, что пустой тег даёт разные слоты — иначе проверка бессмысленна")
	}
}

// TestStaleUnlockCannotDeleteReacquiredLock — идентификатор захвата обязан быть
// уникальным для КАЖДОГО захвата, а не стабильным именем воркера. Сценарий:
// worker-1 взял лок → TTL истёк → тот же worker-1 взял лок ЗАНОВО → запоздавший
// Release от ПЕРВОГО захвата не должен снять второй, живой лок.
func TestStaleUnlockCannotDeleteReacquiredLock(t *testing.T) {
	ctx := context.Background()
	rdb := startRedis(t)
	lock, err := NewLock(rdb, "job")
	if err != nil {
		t.Fatal(err)
	}
	const ttl = 200 * time.Millisecond

	first, ok, err := lock.Acquire(ctx, ttl)
	if err != nil || !ok {
		t.Fatalf("первый захват: ok=%v err=%v", ok, err)
	}
	time.Sleep(ttl + 300*time.Millisecond) // TTL истёк

	second, ok, err := lock.Acquire(ctx, time.Minute) // тот же воркер взял заново
	if err != nil || !ok {
		t.Fatalf("повторный захват: ok=%v err=%v", ok, err)
	}
	if second.Token <= first.Token {
		t.Fatalf("токен повторного захвата (%d) должен быть больше первого (%d)", second.Token, first.Token)
	}

	// Запоздавшее освобождение от ПЕРВОГО захвата.
	if err := lock.Release(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Второй лок обязан остаться: снять его может только его собственный Release.
	if _, ok, err := lock.Acquire(ctx, time.Minute); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("запоздавший Release снял живой лок повторного захвата")
	}
	if err := lock.Release(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := lock.Acquire(ctx, time.Minute); err != nil || !ok {
		t.Fatalf("после законного Release лок должен освободиться: ok=%v err=%v", ok, err)
	}
}

// TestFenceCounterNonPositive — счётчик может быть испорчен ОТРИЦАТЕЛЬНЫМ числом,
// и тогда INCR отработает штатно, вернув мусор. Проверяем оба случая:
//
//	-1 → INCR даёт 0, что неотличимо от «лок занят» (а лок при этом поставлен);
//	-2 → INCR даёт -1, и приведение к uint64 дало бы максимальный токен, который
//	     отравил бы ресурс навсегда.
//
// Оба должны считаться повреждением: ошибка, лок не остаётся, счётчик не «дотикивает».
func TestFenceCounterNonPositive(t *testing.T) {
	for _, start := range []string{"-1", "-2"} {
		t.Run("счётчик="+start, func(t *testing.T) {
			ctx := context.Background()
			rdb := startRedis(t)
			lockKey, fenceKey, err := KeysForResource("job")
			if err != nil {
				t.Fatal(err)
			}
			if err := rdb.Set(ctx, fenceKey, start, 0).Err(); err != nil {
				t.Fatal(err)
			}
			lock := NewNaiveLock(rdb, lockKey, fenceKey)

			lease, ok, err := lock.Acquire(ctx, time.Minute)
			if err == nil {
				t.Fatalf("ждали ошибку на повреждённом счётчике, получили token=%d ok=%v", lease.Token, ok)
			}
			if ok {
				t.Fatal("захват не должен считаться успешным")
			}
			// Лок не должен остаться висеть до TTL.
			if n, err := rdb.Exists(ctx, lockKey).Result(); err != nil {
				t.Fatal(err)
			} else if n != 0 {
				t.Fatal("лок остался после отказа — ресурс заблокирован до TTL")
			}
			// Счётчик откачен: повторные попытки не «дотикают» его до положительного,
			// маскируя повреждение.
			if got, err := rdb.Get(ctx, fenceKey).Result(); err != nil {
				t.Fatal(err)
			} else if got != start {
				t.Fatalf("счётчик изменился: %q вместо %q — INCR не откачен", got, start)
			}
			// Повтор даёт тот же отказ, а не «самолечение».
			if _, ok, err := lock.Acquire(ctx, time.Minute); err == nil || ok {
				t.Fatal("повторная попытка на повреждённом счётчике должна снова падать")
			}
			if got, _ := rdb.Get(ctx, fenceKey).Result(); got != start {
				t.Fatalf("после повтора счётчик уполз: %q", got)
			}
		})
	}
}
