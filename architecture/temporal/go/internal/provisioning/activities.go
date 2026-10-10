package provisioning

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"
)

// Activities — набор activity процесса. Здесь и ТОЛЬКО здесь разрешён
// побочный эффект: код воркфлоу детерминирован и в сеть ходить не может.
//
// runs — счётчик фактических выполнений В ПРЕДЕЛАХ ОДНОГО ПРОЦЕССА воркера.
// Он локален процессу: после перезапуска воркера счётчик снова с нуля.
// Именно этим доказывается, что уже выполненные activity при replay не
// вызываются повторно — их имён просто нет в логе нового процесса.
type Activities struct {
	runs atomic.Int64

	// Latency — искусственная задержка, имитирующая обращение к внешней
	// системе. Профили, которые меряют время, ставят её в ноль, чтобы не
	// мерить собственную заглушку.
	Latency time.Duration
}

// RunsOf — сколько activity реально выполнено этим процессом.
//
// Намеренно ФУНКЦИЯ пакета, а не метод. RegisterActivity регистрирует
// ВСЕ экспортированные методы структуры и требует от каждого сигнатуру
// activity: метод `Runs() int64` роняет регистрацию с сообщением
// «expected function second return value to return error but found int64».
func RunsOf(a *Activities) int64 { return a.runs.Load() }

func (a *Activities) tick(name, arg string) int64 {
	n := a.runs.Add(1)
	log.Printf(">>> ACTIVITY %s(%q) — РЕАЛЬНОЕ выполнение №%d в этом процессе воркера", name, arg, n)
	if a.Latency > 0 {
		time.Sleep(a.Latency)
	}
	return n
}

// CheckAvailability — «проверить доступность ресурса».
func (a *Activities) CheckAvailability(_ context.Context, resource string) (bool, error) {
	a.tick("CheckAvailability", resource)
	return true, nil
}

// Reserve — «зарезервировать ресурс», возвращает идентификатор брони.
// Идентификатор детерминирован от имени ресурса: так лог читается, а
// повторный прогон профиля даёт то же значение.
func (a *Activities) Reserve(_ context.Context, resource string) (string, error) {
	a.tick("Reserve", resource)
	return fmt.Sprintf("res-%s-001", resource), nil
}

// Allocate — «закрепить ресурс по брони», успешный путь.
func (a *Activities) Allocate(_ context.Context, reservationID string) (string, error) {
	a.tick("Allocate", reservationID)
	return fmt.Sprintf("ресурс выделен по брони %s", reservationID), nil
}

// CancelReservation — компенсация: снять бронь.
func (a *Activities) CancelReservation(_ context.Context, reservationID string) (string, error) {
	a.tick("CancelReservation", reservationID)
	return fmt.Sprintf("бронь %s снята", reservationID), nil
}
