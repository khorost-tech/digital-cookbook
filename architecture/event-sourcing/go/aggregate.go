package main

import (
	"encoding/json"
	"fmt"
)

// Типы событий домена «учёт кислорода в резервуарах лунной базы».
const (
	EvtRegistered = "TankRegistered" // резервуар введён в строй; data: {capacity}
	EvtAdded      = "OxygenAdded"    // закачали кислород;         data: {amount}
	EvtConsumed   = "OxygenConsumed" // израсходовали кислород;    data: {amount}
)

// Tank — агрегат «резервуар с кислородом». Состояние восстанавливается свёрткой
// (fold) событий стрима: чистая функция apply(state, event) -> state.
type Tank struct {
	StreamID string  `json:"stream_id"`
	Capacity float64 `json:"capacity"`
	Level    float64 `json:"level"`
	Version  int64   `json:"version"`
}

// Apply применяет одно событие к состоянию агрегата. Детерминированная и без
// побочных эффектов: тот же набор событий всегда даёт то же состояние.
func (t *Tank) Apply(e Event) error {
	switch e.EventType {
	case EvtRegistered:
		var d struct {
			Capacity float64 `json:"capacity"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return err
		}
		t.StreamID = e.StreamID
		t.Capacity = d.Capacity
		t.Level = 0
	case EvtAdded:
		var d struct {
			Amount float64 `json:"amount"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return err
		}
		t.Level += d.Amount
	case EvtConsumed:
		var d struct {
			Amount float64 `json:"amount"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return err
		}
		t.Level -= d.Amount
	default:
		return fmt.Errorf("неизвестный тип события: %s", e.EventType)
	}
	t.Version = e.Version
	return nil
}

// foldTank сворачивает срез событий в состояние агрегата.
func foldTank(events []Event) (*Tank, error) {
	t := &Tank{}
	for _, e := range events {
		if err := t.Apply(e); err != nil {
			return nil, err
		}
	}
	return t, nil
}
