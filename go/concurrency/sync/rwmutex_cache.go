// rwmutex_cache.go — read-heavy кэш на sync.RWMutex.
// RWMutex пускает много читателей одновременно, но писатель эксклюзивен.
// Выгоден там, где чтений кратно больше записей.
package synccookbook

import "sync"

// RWCache — потокобезопасный кэш «строка → строка».
type RWCache struct {
	mu   sync.RWMutex
	data map[string]string
}

// NewRWCache создаёт пустой кэш.
func NewRWCache() *RWCache {
	return &RWCache{data: make(map[string]string)}
}

// Get читает значение под разделяемой блокировкой чтения:
// параллельные Get не мешают друг другу.
func (c *RWCache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.data[key]
	return v, ok
}

// Set пишет значение под эксклюзивной блокировкой записи.
func (c *RWCache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
}

// Len возвращает число записей (под блокировкой чтения).
func (c *RWCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.data)
}
