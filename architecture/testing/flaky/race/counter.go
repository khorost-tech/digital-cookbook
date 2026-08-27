// Package race — класс flaky «гонка данных».
// Два счётчика: небезопасный (обычный int, инкремент без синхронизации) и
// безопасный (atomic). Первый под конкурентной нагрузкой теряет обновления и
// ловится -race; второй детерминирован.
package race

import "sync/atomic"

// UnsafeCounter — счётчик БЕЗ синхронизации. Конкурентный Inc — гонка данных.
type UnsafeCounter struct {
	n int
}

func (c *UnsafeCounter) Inc()     { c.n++ } // read-modify-write без защиты
func (c *UnsafeCounter) Value() int { return c.n }

// SafeCounter — счётчик на atomic. Конкурентный Inc корректен.
type SafeCounter struct {
	n atomic.Int64
}

func (c *SafeCounter) Inc()       { c.n.Add(1) }
func (c *SafeCounter) Value() int64 { return c.n.Load() }
