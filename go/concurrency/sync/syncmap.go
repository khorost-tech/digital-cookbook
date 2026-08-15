// syncmap.go — sync.Map под «родные» ей сценарии.
// sync.Map выигрывает у «map + RWMutex», когда:
//   - ключ пишется один раз, а дальше только читается (растущий кэш);
//   - разные горутины работают с непересекающимися наборами ключей.
// В остальных случаях обычная map под мьютексом проще и обычно быстрее.
package synccookbook

import "sync"

// Registry — растущий реестр «ключ → id». Значение для ключа назначается
// один раз (GetOrAssign) и дальше только читается — сценарий, под который
// sync.Map и заточена: минимум конкуренции на запись, много чтений.
type Registry struct {
	m       sync.Map     // map[string]int64
	counter sync.Mutex   // защищает выдачу монотонных id
	nextID  int64
}

// GetOrAssign возвращает id для ключа, назначая новый при первом обращении.
// LoadOrStore атомарен: даже если несколько горутин одновременно пришли
// с новым ключом, победит ровно одно значение, остальные его получат.
func (r *Registry) GetOrAssign(key string) int64 {
	if v, ok := r.m.Load(key); ok {
		return v.(int64)
	}
	id := r.allocID()
	actual, loaded := r.m.LoadOrStore(key, id)
	if loaded {
		// Кто-то опередил нас — выданный нами id просто не используется.
		return actual.(int64)
	}
	return id
}

func (r *Registry) allocID() int64 {
	r.counter.Lock()
	defer r.counter.Unlock()
	r.nextID++
	return r.nextID
}

// Len подсчитывает число записей. ВНИМАНИЕ: Range НЕ даёт согласованный
// снимок — он не блокирует Map, и во время обхода могут отражаться
// конкурентные вставки/удаления; для нагруженной Map результат приблизителен.
func (r *Registry) Len() int {
	n := 0
	r.m.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

// ShardedCounters — второй «родной» сценарий: каждая горутина пишет в
// СВОЙ ключ (непересекающиеся наборы), конкуренции на одну запись нет.
type ShardedCounters struct {
	m sync.Map // map[string]*int64-подобное; храним значение напрямую
}

// Add кладёт (перезаписывает) значение для «своего» ключа горутины.
func (s *ShardedCounters) Add(key string, value int64) {
	s.m.Store(key, value)
}

// Get читает значение по ключу.
func (s *ShardedCounters) Get(key string) (int64, bool) {
	v, ok := s.m.Load(key)
	if !ok {
		return 0, false
	}
	return v.(int64), true
}
