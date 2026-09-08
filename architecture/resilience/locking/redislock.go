package locking

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// luaUnlock освобождает лок, только если он всё ещё наш (сравнение владельца),
// одной атомарной операцией — иначе можно снять чужой лок, взятый после нашего TTL.
const luaUnlock = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`

// luaAcquireWithToken — захват лока и выдача fencing-токена в одном скрипте.
// Токен выдаётся ТОЛЬКО тому, кто в этот же момент получил лок, поэтому «взял лок →
// уснул → получил токен позже соседа» невозможно.
//
// ВАЖНО про границы гарантии. Lua в Redis даёт ИЗОЛЯЦИЮ (скрипт выполняется целиком,
// его никто не перебьёт), но НЕ транзакционный откат: если вторая команда упадёт с
// runtime-ошибкой, первая уже применена. Поэтому INCR вызывается через pcall, и при
// ошибке (например, счётчик занят нечисловым значением) мы САМИ снимаем только что
// поставленный лок — иначе вызывающий получил бы ошибку, а ресурс остался бы
// заблокированным до истечения TTL.
//
// Мало проверить ОШИБКУ INCR — надо проверить и его РЕЗУЛЬТАТ. Счётчик может быть
// испорчен отрицательным числом, и тогда INCR отработает штатно, вернув мусор:
//
//	счётчик -1 → INCR вернёт 0, а ноль у нас означает «лок занят» — вызывающий
//	  решит, что захвата не было, хотя лок уже поставлен и провисит до TTL;
//	счётчик -2 → INCR вернёт -1, и приведение к uint64 даст максимальное значение,
//	  которое НАВСЕГДА отравит ресурс: любой следующий честный токен будет меньше
//	  и станет отвергаться как устаревший.
//
// Поэтому неположительный результат считаем повреждением: откатываем собственный
// INCR через DECR (чтобы повторные попытки не «дотикали» счётчик до положительного
// и не сделали вид, что всё в порядке), снимаем лок и возвращаем ошибку.
//
// KEYS[1] — ключ лока, KEYS[2] — счётчик токенов; ARGV[1] — владелец, ARGV[2] — TTL мс.
const luaAcquireWithToken = `
if redis.call('SET', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[2]) then
	local n = redis.pcall('INCR', KEYS[2])
	if type(n) == 'table' and n.err then
		redis.call('DEL', KEYS[1]) -- лок только что наш, снимаем его сами
		return redis.error_reply('fencing counter unusable: ' .. n.err)
	end
	if n <= 0 then
		redis.call('DECR', KEYS[2]) -- откатываем свой INCR, не «лечим» счётчик молча
		redis.call('DEL', KEYS[1])
		return redis.error_reply('fencing counter is non-positive')
	end
	return n
end
return 0
`

// NaiveLock — простейший распределённый лок на Redis: SET key owner NX PX ttl.
// TTL страхует от «вечного» лока при падении владельца. Именно такой лок критикует
// Клеппманн: TTL может истечь, пока владелец жив, но «спит» (GC-паузы, сеть), — и лок
// достанется второму. Сам по себе он безопасность НЕ даёт; защищает fencing-токен на
// стороне ресурса — но только если токен выдан АТОМАРНО с локом (см. ниже).
type NaiveLock struct {
	rdb      redis.Cmdable
	key      string
	fenceKey string
	unlock   *redis.Script
	acquire  *redis.Script
}

// ErrBadResource — имя ресурса непригодно для построения ключей.
var ErrBadResource = errors.New("locking: имя ресурса пустое или содержит фигурные скобки")

// ErrBadFenceCounter — счётчик fencing-токенов повреждён (неположительное значение).
var ErrBadFenceCounter = errors.New("locking: счётчик fencing-токенов неположителен")

// KeysForResource строит пару ключей с общим hash tag: `{resource}:lock` и
// `{resource}:fence`. В Redis Cluster все ключи одного Lua-скрипта обязаны лежать
// в ОДНОМ слоте — иначе CROSSSLOT. Тег в фигурных скобках заставляет кластер считать
// слот по одной и той же подстроке.
//
// ГОТЧА: тег работает не всегда. ПУСТОЙ тег (`{}`) Redis игнорирует и считает слот
// по всему ключу — `{}:lock` и `{}:fence` уедут в разные слоты. Так же ломается имя,
// в котором уже есть свои скобки. Поэтому имя ресурса валидируется, а не берётся на веру.
func KeysForResource(resource string) (lockKey, fenceKey string, err error) {
	if resource == "" || strings.ContainsAny(resource, "{}") {
		return "", "", ErrBadResource
	}
	return "{" + resource + "}:lock", "{" + resource + "}:fence", nil
}

// NewLock создаёт лок для ресурса, сам строя обе ключевые записи с общим hash tag.
// Это предпочтительный конструктор: произвольная пара ключей легко разъедется по
// слотам кластера, а здесь такой возможности просто нет.
func NewLock(rdb redis.Cmdable, resource string) (*NaiveLock, error) {
	lockKey, fenceKey, err := KeysForResource(resource)
	if err != nil {
		return nil, err
	}
	return newLock(rdb, lockKey, fenceKey), nil
}

// NewNaiveLock создаёт лок на произвольной паре ключей. В кластере используйте
// NewLock: произвольные ключи не обязаны попасть в один слот.
func NewNaiveLock(rdb redis.Cmdable, key, fenceKey string) *NaiveLock {
	return newLock(rdb, key, fenceKey)
}

func newLock(rdb redis.Cmdable, key, fenceKey string) *NaiveLock {
	return &NaiveLock{
		rdb:      rdb,
		key:      key,
		fenceKey: fenceKey,
		unlock:   redis.NewScript(luaUnlock),
		acquire:  redis.NewScript(luaAcquireWithToken),
	}
}

// Lease — результат успешного захвата: fencing-токен плюс идентификатор ИМЕННО
// ЭТОГО захвата. Идентификатор случайный и генерируется внутри: стабильное имя
// (хост, pid, «worker-1») для этого не годится. Иначе возможен сценарий, который
// выглядит безобидно: воркер взял лок, TTL истёк, тот же воркер взял лок ЗАНОВО —
// и запоздавший Unlock от первого захвата удаляет второй, живой лок.
type Lease struct {
	Token uint64 // fencing-токен, его несут к ресурсу
	id    string // случайный идентификатор захвата, не экспортируется
}

func newAcquisitionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Acquire — рекомендуемый способ захвата: идентификатор захвата генерируется
// внутри, поэтому снять чужой (в том числе свой прошлый) лок невозможно.
func (l *NaiveLock) Acquire(ctx context.Context, ttl time.Duration) (Lease, bool, error) {
	id, err := newAcquisitionID()
	if err != nil {
		return Lease{}, false, err
	}
	token, ok, err := l.AcquireWithToken(ctx, id, ttl)
	if err != nil || !ok {
		return Lease{}, ok, err
	}
	return Lease{Token: token, id: id}, true, nil
}

// Release снимает лок, только если он всё ещё принадлежит ЭТОМУ захвату.
func (l *NaiveLock) Release(ctx context.Context, lease Lease) error {
	return l.Unlock(ctx, lease.id)
}

// AcquireWithToken атомарно берёт лок и возвращает fencing-токен. owner должен быть
// уникален для КАЖДОГО захвата — предпочитайте Acquire, который генерирует его сам.
// ok=false — лок занят (токен при этом НЕ тратится и не выдаётся).
func (l *NaiveLock) AcquireWithToken(ctx context.Context, owner string, ttl time.Duration) (token uint64, ok bool, err error) {
	n, err := l.acquire.Run(ctx, l.rdb, []string{l.key, l.fenceKey}, owner, ttl.Milliseconds()).Int64()
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, nil // лок занят: скрипт до INCR не дошёл
	}
	if n < 0 {
		// Скрипт такого вернуть не должен, но подстраховываемся: приведение
		// отрицательного к uint64 дало бы огромный токен и отравило ресурс.
		return 0, false, ErrBadFenceCounter
	}
	return uint64(n), true, nil
}

// AcquireThenTokenUnsafe — НЕПРАВИЛЬНЫЙ способ, оставлен как демонстрация дефекта.
// Захват лока и выдача токена здесь две отдельные операции, между которыми владелец
// может «уснуть». Тогда возможно чередование: A взял лок → уснул до INCR → TTL истёк →
// B взял лок и токен 1, записал → A проснулся и получил токен 2, НЕ владея локом →
// ресурс принимает запись A как более свежую и портит данные. Ровно от этого fencing
// и должен защищать, поэтому связка «лок + токен» обязана быть атомарной.
//
// pause вызывается между двумя операциями — тест использует его, чтобы воспроизвести
// чередование детерминированно (в проде эту паузу устраивает сборщик мусора или сеть).
func (l *NaiveLock) AcquireThenTokenUnsafe(ctx context.Context, owner string, ttl time.Duration, pause func()) (token uint64, ok bool, err error) {
	acquired, err := l.rdb.SetNX(ctx, l.key, owner, ttl).Result()
	if err != nil || !acquired {
		return 0, false, err
	}
	if pause != nil {
		pause() // здесь владелец «спит»: TTL успевает истечь
	}
	n, err := l.rdb.Incr(ctx, l.fenceKey).Result()
	if err != nil {
		return 0, false, err
	}
	return uint64(n), true, nil
}

// Unlock освобождает лок, если он ещё принадлежит owner.
func (l *NaiveLock) Unlock(ctx context.Context, owner string) error {
	return l.unlock.Run(ctx, l.rdb, []string{l.key}, owner).Err()
}
