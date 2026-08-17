// Package race демонстрирует артефакт: реальная гонка данных, пойманная
// детектором `go test -race`, и её исправление через sync.RWMutex.
//
// UnsyncCache — заведомо небезопасная реализация (прямой доступ к map без
// синхронизации). Под конкурентной нагрузкой Get/Set она провоцирует
// DATA RACE, который детектор ловит по факту конкурентного read/write
// одной и той же map без happens-before отношения между горутинами.
//
// SyncCache — та же логика под sync.RWMutex: Get берёт RLock (несколько
// читателей одновременно), Set берёт Lock (эксклюзивно). Под -race чиста.
package race

import "sync"

// UnsyncCache — in-memory кэш без синхронизации (broken).
//
// Прямой доступ к map из нескольких горутин без mutex/atomic — гонка данных
// в терминах Go memory model, даже если внешне тест иногда проходит без
// -race (гонка недетерминирована и не всегда приводит к видимому сбою).
type UnsyncCache struct {
	m map[string]string
}

// NewUnsyncCache создаёт пустой несинхронизированный кэш.
func NewUnsyncCache() *UnsyncCache {
	return &UnsyncCache{m: make(map[string]string)}
}

// Get возвращает значение по ключу без какой-либо синхронизации.
func (c *UnsyncCache) Get(key string) string {
	return c.m[key]
}

// Set записывает значение по ключу без какой-либо синхронизации.
func (c *UnsyncCache) Set(key, val string) {
	c.m[key] = val
}

// SyncCache — in-memory кэш под sync.RWMutex (fixed).
//
// Set берёт эксклюзивную блокировку (Lock), Get — разделяемую (RLock):
// это устраняет гонку данных, сохраняя параллелизм чтения.
type SyncCache struct {
	mu sync.RWMutex
	m  map[string]string
}

// NewSyncCache создаёт пустой синхронизированный кэш.
func NewSyncCache() *SyncCache {
	return &SyncCache{m: make(map[string]string)}
}

// Get возвращает значение по ключу под RLock.
func (c *SyncCache) Get(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m[key]
}

// Set записывает значение по ключу под Lock.
func (c *SyncCache) Set(key, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = val
}
