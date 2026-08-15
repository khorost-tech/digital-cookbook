// Пакет memorymodel — примеры к статье #3 «Модель памяти Go: happens-before»
// серии «Конкурентность в Go» (khorost.tech).
//
// publish_safe.go — безопасная публикация объекта, починка cmd/publish-unsafe.
// Раздел «Публикация объекта: как читатель видит недостроенный Config».
//
// atomic.Pointer[Config] (Go 1.19+) даёт happens-before: атомарный Store
// указателя happens-before атомарного Load, который этот указатель прочитал.
// Так как объект полностью сконструирован ДО Store, читатель, увидевший
// ненулевой указатель, гарантированно видит и все инициализированные поля.
package memorymodel

import "sync/atomic"

// Config — публикуемый объект (тот же, что в демо cmd/publish-unsafe).
type Config struct {
	Endpoint string
	Retries  int
	Ready    bool
}

// ConfigHolder безопасно публикует *Config между горутинами.
// Нулевое значение готово к использованию: Load вернёт nil, пока не было Store.
type ConfigHolder struct {
	ptr atomic.Pointer[Config]
}

// Publish атомарно публикует полностью сконструированный объект.
// Конструирование завершено до Store, поэтому читатель не увидит «половину».
func (h *ConfigHolder) Publish(c *Config) {
	h.ptr.Store(c)
}

// Load атомарно возвращает опубликованный объект (или nil, если ещё не было
// Publish). Если результат не nil — все поля объекта уже видны корректно.
func (h *ConfigHolder) Load() *Config {
	return h.ptr.Load()
}
