// Package provisioning — общее доменное ядро стенда: один процесс
// «провизионинг ресурса», который во всех восьми профилях серии играет
// роль подопытного. Профили меняют ОБВЯЗКУ (как запускается воркер, что
// ломается, что меряется), а домен остаётся одним и тем же — иначе
// сравнивать профили между собой было бы нельзя.
package provisioning

import "time"

const (
	// TaskQueue — очередь по умолчанию. Профили, которым нужна отдельная
	// очередь (разнесение пулов воркеров), передают своё имя явно.
	TaskQueue = "provisioning-tq"

	// SignalConfirmation — сигнал «человек в цикле»: подтверждение брони.
	SignalConfirmation = "confirmation"

	// QueryStatus — query-обработчик текущего статуса. Query не пишет в
	// историю и не меняет состояние — только читает.
	QueryStatus = "status"
)

// Request — вход воркфлоу.
type Request struct {
	Resource       string        `json:"resource"`
	ConfirmTimeout time.Duration `json:"confirmTimeout"`
}

// Result — выход воркфлоу. Исход выражен строкой, а не булевым флагом:
// «отменено по таймауту» и «отменено отказом» — разные события, и в
// статьях они разбираются по отдельности.
type Result struct {
	ReservationID string `json:"reservationId"`
	// Outcome: "allocated" | "cancelled_timeout" | "cancelled_rejected"
	Outcome string `json:"outcome"`
}

// ConfirmationSignal — полезная нагрузка сигнала подтверждения.
type ConfirmationSignal struct {
	Approved bool   `json:"approved"`
	By       string `json:"by"`
}
