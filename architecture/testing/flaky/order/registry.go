// Package order — класс flaky «порядок и изоляция».
// Общее изменяемое состояние на уровне пакета делает тесты зависимыми от
// порядка выполнения. go test -shuffle=on вскрывает это; лечится изоляцией
// (свежее состояние на тест + t.Cleanup), а не «фиксацией» порядка.
package order

// Registry — простое хранилище. Проблема не в нём, а в том, что тесты делят
// ОДИН экземпляр на уровне пакета (см. registry_flaky_test.go).
type Registry struct {
	items map[string]int
}

func NewRegistry() *Registry {
	return &Registry{items: map[string]int{}}
}

func (r *Registry) Add(key string) { r.items[key]++ }
func (r *Registry) Count() int     { return len(r.items) }
func (r *Registry) Reset()         { r.items = map[string]int{} }
