// Стенд к статье «Хореография vs оркестрация» на khorost.tech:
//
//	https://khorost.tech/architecture/choreography-vs-orchestration/
//
// Один и тот же бизнес-сценарий «оформить заказ» — оплата → резерв товара →
// создание доставки → уведомление клиента — реализован ДВУМЯ способами
// координации, чтобы наглядно увидеть контраст «где виден весь поток».
//
//   - ОРКЕСТРАЦИЯ (orchestration.go) — Temporal workflow. Явный сценарий в одном
//     месте: шаги идут по порядку в коде workflow, при ошибке — явная обработка.
//     Весь поток читается сверху вниз в одной функции; состояние каждого заказа
//     видно в Temporal UI (http://localhost:8233).
//
//   - ХОРЕОГРАФИЯ (choreography.go) — сервисы реагируют на события через
//     IN-PROCESS event-bus (простой pub/sub на подписках, без внешнего брокера).
//     OrderPlaced→оплата, PaymentTaken→резерв, StockReserved→доставка,
//     ShipmentCreated→уведомление. Единого сценария нет нигде — поток
//     эмерджентный, собирается из независимых подписчиков.
//
// Оба варианта используют ОДНУ И ТУ ЖЕ бизнес-логику шагов (функции do*Step
// ниже) — отличается только КООРДИНАЦИЯ. Итог у обоих одинаковый (заказ доведён
// до конца), но точка наблюдаемости разная — в этом весь смысл сравнения.
//
// Запуск:
//
//	docker compose -f compose/compose.yml up -d      # поднять Temporal dev-сервер
//	go run . -mode=both                              # оба сценария подряд (по умолчанию)
//	go run . -mode=orchestration                     # только Temporal
//	go run . -mode=choreography                      # только event-bus (Temporal не нужен)
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

// Order — заказ, который проводим через все четыре шага. Общий тип для обоих
// вариантов координации: и Temporal-activity, и подписчики event-bus работают
// с ним же.
type Order struct {
	ID       string
	Customer string
	Amount   int // сумма в условных копейках
}

// OrderResult — итог оформления. Собирает идентификаторы, выданные каждым шагом.
// Оба варианта обязаны прийти к идентичному по составу результату.
type OrderResult struct {
	OrderID       string
	PaymentID     string
	ReservationID string
	ShipmentID    string
	Notification  string
}

// Completed — true, если все четыре шага отработали и заказ доведён до конца.
func (r OrderResult) Completed() bool {
	return r.PaymentID != "" && r.ReservationID != "" &&
		r.ShipmentID != "" && r.Notification != ""
}

func (r OrderResult) String() string {
	return fmt.Sprintf("order=%s payment=%s reservation=%s shipment=%s notify=%q",
		r.OrderID, r.PaymentID, r.ReservationID, r.ShipmentID, r.Notification)
}

// --- Общая бизнес-логика четырёх шагов -------------------------------------
//
// Чистые детерминированные функции — ровно те же вызовы делают и Temporal-activity
// (оркестрация), и сервисы-подписчики (хореография). Это подчёркивает, что
// сравниваем именно способ КООРДИНАЦИИ, а не разную бизнес-логику.

func doTakePayment(o Order) string     { return fmt.Sprintf("pay-%s-%d", o.ID, o.Amount) }
func doReserveStock(o Order) string    { return fmt.Sprintf("resv-%s", o.ID) }
func doCreateShipment(o Order) string  { return fmt.Sprintf("ship-%s", o.ID) }
func doNotifyCustomer(o Order) string  { return fmt.Sprintf("клиент %s уведомлён о заказе %s", o.Customer, o.ID) }

const temporalHostPort = "localhost:7233"

func main() {
	mode := flag.String("mode", "both",
		"сценарий: orchestration | choreography | both")
	hostPort := flag.String("temporal", temporalHostPort,
		"адрес Temporal frontend (gRPC)")
	flag.Parse()

	order := Order{ID: "1001", Customer: "Иванов", Amount: 4990}

	var orch, chor OrderResult
	var runOrch, runChor bool
	switch strings.ToLower(*mode) {
	case "orchestration":
		runOrch = true
	case "choreography":
		runChor = true
	case "both":
		runOrch, runChor = true, true
	default:
		log.Fatalf("неизвестный -mode=%q (ожидается orchestration|choreography|both)", *mode)
	}

	if runOrch {
		res, err := runOrchestration(*hostPort, order)
		if err != nil {
			fmt.Println()
			fmt.Println("!!! Оркестрация не выполнена:", err)
			fmt.Println("!!! Поднят ли Temporal dev-сервер?  docker compose -f compose/compose.yml up -d")
			fmt.Println("!!! UI после старта: http://localhost:8233")
			os.Exit(1)
		}
		orch = res
	}

	if runChor {
		chor = runChoreography(order)
	}

	if runOrch && runChor {
		printContrast(orch, chor)
	}
}

// printContrast — короткое резюме: оба варианта дали одинаковый итог, но точка
// наблюдаемости у них разная. Здесь же — ассерты на совпадение результата.
func printContrast(orch, chor OrderResult) {
	line := strings.Repeat("=", 78)
	fmt.Println()
	fmt.Println(line)
	fmt.Println("РЕЗЮМЕ КОНТРАСТА")
	fmt.Println(line)

	// Ассерт: оба варианта доводят заказ до конца.
	if !orch.Completed() {
		log.Fatalf("АССЕРТ ПРОВАЛЕН: оркестрация не довела заказ до конца: %s", orch)
	}
	if !chor.Completed() {
		log.Fatalf("АССЕРТ ПРОВАЛЕН: хореография не довела заказ до конца: %s", chor)
	}
	// Ассерт: итог идентичен (одна бизнес-логика — один результат).
	if orch.String() != chor.String() {
		log.Fatalf("АССЕРТ ПРОВАЛЕН: итоги разошлись\n  оркестрация: %s\n  хореография: %s", orch, chor)
	}
	fmt.Println("[ассерт] оба варианта довели заказ до конца — OK")
	fmt.Println("[ассерт] итог идентичен (одна и та же бизнес-логика) — OK")
	fmt.Println("  итог:", orch)
	fmt.Println()

	fmt.Println("Одинаковый результат — разная координация и наблюдаемость:")
	fmt.Println()
	fmt.Println("1) ГДЕ ВИДЕН ВЕСЬ ПОТОК")
	fmt.Println("   оркестрация: собран в одной функции OrderWorkflow (orchestration.go).")
	fmt.Println("                Читается сверху вниз: оплата→резерв→доставка→уведомление.")
	fmt.Println("   хореография: нигде целиком. Порядок эмерджентный — восстанавливается")
	fmt.Println("                только по логу «кто на что отреагировал» (см. выше).")
	fmt.Println()
	fmt.Println("2) КАК ДОБАВИТЬ ШАГ (например, антифрод перед оплатой)")
	fmt.Println("   оркестрация: правка ОДНОГО файла — вставить ExecuteActivity в workflow.")
	fmt.Println("   хореография: НОВАЯ подписка на событие + новое событие в цепочке;")
	fmt.Println("                правится несколько независимых мест, порядок неявный.")
	fmt.Println()
	fmt.Println("3) ОТЛАДКА «НА КАКОМ ШАГЕ ЗАСТРЯЛ ЗАКАЗ»")
	fmt.Println("   оркестрация: видно в состоянии workflow и в Temporal UI")
	fmt.Println("                (http://localhost:8233) — история шагов по workflow ID.")
	fmt.Println("   хореография: единого состояния нет — надо обойти подписчиков и")
	fmt.Println("                собрать картину из их логов вручную.")
	fmt.Println(line)
}
