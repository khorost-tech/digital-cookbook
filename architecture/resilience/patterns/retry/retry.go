// Package retry — экспоненциальный backoff с полным джиттером и retry-бюджет.
//
// Повтор — обоюдоострый приём: он спасает от разовой ошибки, но при массовом
// отказе зависимости наивный повтор УМНОЖАЕТ нагрузку на неё (retry-шторм) и не
// даёт ей встать. Два предохранителя против этого: джиттер (разносит синхронные
// повторы во времени) и бюджет (ограничивает долю повторов к обычным запросам).
package retry

import (
	"math"
	"sync"
	"time"
)

// Backoff — экспоненциальная задержка для попытки attempt (с нуля):
// base*2^attempt, но не больше maxDelay.
func Backoff(attempt int, base, maxDelay time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := float64(base) * math.Pow(2, float64(attempt))
	if math.IsInf(d, 1) || d > float64(maxDelay) {
		return maxDelay
	}
	return time.Duration(d)
}

// FullJitter — «полный джиттер»: равномерно случайная задержка в [0, Backoff].
// rnd — источник в [0,1) (инъектируется в тестах). Разносит одновременно упавших
// клиентов, чтобы после отказа они не ломились повторно в один и тот же момент.
func FullJitter(attempt int, base, maxDelay time.Duration, rnd float64) time.Duration {
	return time.Duration(rnd * float64(Backoff(attempt, base, maxDelay)))
}

// Budget ограничивает долю повторов к обычным запросам (приём из gRPC retry
// throttling). Каждый обычный запрос начисляет ratio токенов (до потолка),
// каждый повтор тратит один. Исчерпан бюджет — повторов нет, и шторм гаснет.
type Budget struct {
	ratio  float64
	max    float64
	mu     sync.Mutex
	tokens float64
}

// NewBudget: ratio — сколько повторов на один обычный запрос (напр. 0.1),
// maxTokens — потолок накопления. Стартует с нуля токенов: пока нет обычного
// трафика, повторов нет.
func NewBudget(ratio float64, maxTokens int) *Budget {
	if ratio <= 0 || maxTokens < 1 {
		panic("retry: нужны ratio > 0 и maxTokens >= 1")
	}
	return &Budget{ratio: ratio, max: float64(maxTokens)}
}

// OnRequest начисляет бюджет за один обычный (не повторный) запрос.
func (b *Budget) OnRequest() {
	b.mu.Lock()
	b.tokens = math.Min(b.max, b.tokens+b.ratio)
	b.mu.Unlock()
}

// TryRetry занимает токен под повтор; false — бюджет исчерпан, повторять нельзя.
// Эпсилон в сравнении: ratio вроде 0.1 накапливается сложением float и к целому
// не сходится точно (0.1×10 = 0.999…), а бюджет не должен терять токен на дрейфе.
func (b *Budget) TryRetry() bool {
	const eps = 1e-9
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens >= 1-eps {
		b.tokens--
		return true
	}
	return false
}
