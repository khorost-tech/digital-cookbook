// once.go — ленивая единственная инициализация.
// sync.Once гарантирует, что переданная функция выполнится ровно один
// раз при любом числе конкурентных вызовов. sync.OnceValue (Go 1.21+) —
// удобная обёртка, когда инициализация возвращает значение.
package synccookbook

import "sync"

// LazyConfig — ресурс, который дорого строить и который нужен один раз.
type LazyConfig struct {
	once  sync.Once
	value string
	// init вызывается ровно один раз; счётчик вызовов держит вызывающий.
	init func() string
}

// NewLazyConfig принимает инициализатор, который будет вызван при первом Get.
func NewLazyConfig(init func() string) *LazyConfig {
	return &LazyConfig{init: init}
}

// Get возвращает значение, выполнив init при самом первом обращении.
// Остальные вызовы (в т. ч. конкурентные) дождутся результата первого.
func (c *LazyConfig) Get() string {
	c.once.Do(func() {
		c.value = c.init()
	})
	return c.value
}

// NewOnceValueConfig — тот же смысл через sync.OnceValue: возвращает
// функцию, которая при первом вызове выполнит init, а дальше отдаёт кэш.
func NewOnceValueConfig(init func() string) func() string {
	return sync.OnceValue(init)
}
