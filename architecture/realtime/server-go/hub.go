package main

import (
	"sync"
	"sync/atomic"
)

// Event — единица доставки. id монотонен в пределах темы и задаётся издателем:
// так пропуски и дубли у клиента видны по номерам, а не по времени.
type Event struct {
	ID      int64  `json:"id"`
	Topic   string `json:"topic"`
	Payload string `json:"payload"`
}

// Политика очереди на соединение (RT_QUEUE).
const (
	QueueUnbounded  = "unbounded"   // очередь растёт без предела
	QueueDropOldest = "drop-oldest" // при переполнении выбрасывается самое старое
	QueueDisconnect = "disconnect"  // при переполнении медленный клиент отключается
)

// Subscriber — очередь одного соединения. Писатель соединения забирает из неё
// события и пишет в сеть; если клиент читает медленно, очередь копится здесь.
type Subscriber struct {
	mu      sync.Mutex
	items   []Event
	signal  chan struct{}
	closed  chan struct{}
	policy  string
	limit   int
	maxLen  int
	dropped int64
	done    bool // очередь закрыта: дальше события не принимаются и не считаются
	once    sync.Once
}

func newSubscriber(policy string, limit int) *Subscriber {
	return &Subscriber{signal: make(chan struct{}, 1), closed: make(chan struct{}),
		policy: policy, limit: limit}
}

// push кладёт событие в очередь по политике. false — соединение надо закрыть.
func (s *Subscriber) push(e Event) bool {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return false
	}
	if s.policy != QueueUnbounded && len(s.items) >= s.limit {
		switch s.policy {
		case QueueDropOldest:
			s.items = s.items[1:]
			s.dropped++
		case QueueDisconnect:
			// Отключение считается один раз: подписчик остаётся в хабе, пока
			// писатель соединения не заметит закрытие, и следующие события сюда
			// ещё приходят.
			s.done = true
			s.mu.Unlock()
			s.close()
			stats.disconnected.Add(1)
			return false
		}
	}
	s.items = append(s.items, e)
	if len(s.items) > s.maxLen {
		s.maxLen = len(s.items)
	}
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
	return true
}

// next отдаёт накопленное разом; nil — соединение закрыто.
func (s *Subscriber) next() []Event {
	for {
		s.mu.Lock()
		if len(s.items) > 0 {
			out := s.items
			s.items = nil
			s.mu.Unlock()
			return out
		}
		s.mu.Unlock()
		select {
		case <-s.signal:
		case <-s.closed:
			return nil
		}
	}
}

func (s *Subscriber) close() { s.once.Do(func() { close(s.closed) }) }

// Hub — подписчики этого инстанса по темам и недавние события для опроса.
type Hub struct {
	mu     sync.Mutex
	subs   map[string]map[*Subscriber]struct{}
	recent map[string][]Event
	wait   map[string]chan struct{}
}

const recentKeep = 4096

func newHub() *Hub {
	return &Hub{subs: map[string]map[*Subscriber]struct{}{}, recent: map[string][]Event{},
		wait: map[string]chan struct{}{}}
}

func (h *Hub) subscribe(topic string, s *Subscriber) {
	h.mu.Lock()
	if h.subs[topic] == nil {
		h.subs[topic] = map[*Subscriber]struct{}{}
	}
	h.subs[topic][s] = struct{}{}
	h.mu.Unlock()
	stats.conns.Add(1)
}

func (h *Hub) unsubscribe(topic string, s *Subscriber) {
	h.mu.Lock()
	if _, ok := h.subs[topic][s]; ok {
		delete(h.subs[topic], s)
		stats.conns.Add(-1)
	}
	h.mu.Unlock()
	s.mu.Lock()
	if s.maxLen > int(stats.maxQueue.Load()) {
		stats.maxQueue.Store(int64(s.maxLen))
	}
	stats.dropped.Add(s.dropped)
	s.dropped = 0
	s.mu.Unlock()
}

// deliver — событие из шины (или прямой публикации без шины) всем своим.
func (h *Hub) deliver(e Event) {
	h.mu.Lock()
	r := append(h.recent[e.Topic], e)
	if len(r) > recentKeep {
		r = r[len(r)-recentKeep:]
	}
	h.recent[e.Topic] = r
	if ch, ok := h.wait[e.Topic]; ok {
		close(ch)
		delete(h.wait, e.Topic)
	}
	targets := make([]*Subscriber, 0, len(h.subs[e.Topic]))
	for s := range h.subs[e.Topic] {
		targets = append(targets, s)
	}
	h.mu.Unlock()
	stats.delivered.Add(1)
	for _, s := range targets {
		s.push(e)
	}
}

// after — недавние события темы с id больше заданного (опрос).
func (h *Hub) after(topic string, id int64) ([]Event, chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []Event{}
	for _, e := range h.recent[topic] {
		if e.ID > id {
			out = append(out, e)
		}
	}
	ch, ok := h.wait[topic]
	if !ok {
		ch = make(chan struct{})
		h.wait[topic] = ch
	}
	return out, ch
}

func (h *Hub) latest(topic string) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.recent[topic]
	if len(r) == 0 {
		return 0
	}
	return r[len(r)-1].ID
}

// Счётчики инстанса — для /debug/stats.
var stats struct {
	conns, delivered, dropped, disconnected, maxQueue atomic.Int64
}

// liveMaxQueue — наибольшая очередь среди ещё открытых соединений.
func (h *Hub) liveMaxQueue() (maxLen int, dropped int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.subs {
		for s := range set {
			s.mu.Lock()
			if s.maxLen > maxLen {
				maxLen = s.maxLen
			}
			dropped += s.dropped
			s.mu.Unlock()
		}
	}
	return
}
