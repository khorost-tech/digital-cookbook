package main

// Наивный воркер: многошаговый процесс держится в памяти процесса.
// Никакой персистентности, никакой координации — ровно так выглядит
// «сделаю на горутине и ретраях», пока не упадёт.

import (
	"context"
	"fmt"
	"log"
	"time"

	"tech.khorost/temporal-cookbook/internal/provisioning"
)

// runNaive выполняет процесс в памяти. Состояние живёт в локальных
// переменных: убийство процесса теряет ВСЁ, включая факт брони, которую
// во внешней системе уже сделали. Побочный эффект остался, знание о нём — нет.
func runNaive(ctx context.Context, resource string, pause time.Duration) error {
	acts := &provisioning.Activities{Latency: 300 * time.Millisecond}

	ok, err := acts.CheckAvailability(ctx, resource)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("ресурс %q недоступен", resource)
	}

	reservationID, err := acts.Reserve(ctx, resource)
	if err != nil {
		return err
	}
	log.Printf("[naive] бронь получена: %s", reservationID)

	// Окно, в котором процесс убивают. Всё состояние — в этой функции.
	log.Printf("[naive] пауза %s — состояние ТОЛЬКО в памяти процесса", pause)
	time.Sleep(pause)

	msg, err := acts.Allocate(ctx, reservationID)
	if err != nil {
		return err
	}
	log.Printf("[naive] ЗАВЕРШЕНО: %s", msg)
	return nil
}
