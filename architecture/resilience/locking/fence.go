// Package locking — распределённая блокировка и почему без fencing-токена она не
// защищает. Ядро (fence.go) детерминированно воспроизводит сценарий Клеппманна:
// владелец блокировки «уснул» (долгий GC-паузе, сетевой задержке), лок по TTL
// перешёл к другому — и теперь ДВОЕ считают, что держат его. Fencing-токен
// (монотонный номер) снимает проблему: ресурс отвергает запись с токеном меньше
// уже виденного, поэтому запоздавшая запись уснувшего владельца не проходит.
package locking

import (
	"errors"
	"sync"
)

// ErrStaleToken — запись отвергнута: её fencing-токен меньше уже принятого.
var ErrStaleToken = errors.New("locking: устаревший fencing-токен")

// FencedResource — ресурс под защитой fencing-токенов. Принимает запись только с
// токеном не меньше наибольшего уже виденного и запоминает этот максимум.
type FencedResource struct {
	mu       sync.Mutex
	maxToken uint64
	value    string
}

// Write применяет value, если token не устарел; иначе возвращает ErrStaleToken.
func (r *FencedResource) Write(token uint64, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if token < r.maxToken {
		return ErrStaleToken
	}
	r.maxToken = token
	r.value = value
	return nil
}

// Value возвращает текущее значение.
func (r *FencedResource) Value() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.value
}

// UnfencedResource — тот же ресурс без защиты, для контраста: принимает любую
// запись, поэтому запоздавший владелец затирает более свежие данные.
type UnfencedResource struct {
	mu    sync.Mutex
	value string
}

// Write применяет value безусловно.
func (r *UnfencedResource) Write(value string) {
	r.mu.Lock()
	r.value = value
	r.mu.Unlock()
}

// Value возвращает текущее значение.
func (r *UnfencedResource) Value() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.value
}
