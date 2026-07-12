package main

// choreography.go — ХОРЕОГРАФИЯ через in-process event-bus.
//
// Никакого внешнего брокера — простой pub/sub на подписках, чтобы стенд был
// самодостаточным. Каждый сервис знает только про СВОЁ событие: получил —
// сделал свой шаг — выпустил следующее событие. Единого сценария нет НИГДЕ:
// порядок оплата→резерв→доставка→уведомление нигде не записан целиком, он
// эмерджентный — возникает из того, кто на что подписан.
//
// Чтобы вообще увидеть «весь поток», приходится вести отдельный лог «кто на что
// отреагировал» (bus.flow) — иначе порядок восстановить неоткуда.

import (
	"fmt"
	"strings"
)

// choreoEvent — событие в шине. Несёт заказ и общий аккумулятор результата
// (сервисы дописывают в него свои идентификаторы по мере прохождения).
type choreoEvent struct {
	name   string
	order  Order
	result *OrderResult
}

// subscription — подписчик: имя сервиса + обработчик.
type subscription struct {
	subscriber string
	handle     func(*EventBus, choreoEvent)
}

// EventBus — минимальный синхронный pub/sub.
//
// Диспетчеризация синхронная и рекурсивная: Publish вызывает обработчиков сразу,
// а те внутри публикуют следующее событие. Поэтому к моменту возврата
// Publish(OrderPlaced) вся цепочка уже отработала — удобно и детерминированно
// для стенда. В реальной шине доставка была бы асинхронной и через сеть, но суть
// хореографии та же: связи только через события, центрального сценария нет.
type EventBus struct {
	subs map[string][]subscription
	flow []string // лог «кто на что отреагировал» — единственный способ увидеть поток
}

func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[string][]subscription)}
}

// Subscribe регистрирует обработчик сервиса subscriber на событие eventName.
func (b *EventBus) Subscribe(eventName, subscriber string, h func(*EventBus, choreoEvent)) {
	b.subs[eventName] = append(b.subs[eventName], subscription{subscriber, h})
}

// Publish синхронно доставляет событие всем подписчикам и пишет строку в flow-лог.
func (b *EventBus) Publish(e choreoEvent) {
	subs := b.subs[e.name]
	if len(subs) == 0 {
		// Событие, на которое никто не подписан — терминальное (конец цепочки).
		b.flow = append(b.flow, fmt.Sprintf("событие %-15s -> (подписчиков нет, конец цепочки)", e.name))
		return
	}
	for _, s := range subs {
		b.flow = append(b.flow, fmt.Sprintf("событие %-15s -> отреагировал %s", e.name, s.subscriber))
		s.handle(b, e)
	}
}

// wireServices регистрирует четыре сервиса-подписчика. Именно ЭТИ подписки — и
// только они — задают порядок. Нигде нет функции, где было бы написано
// «оплата, потом резерв, потом доставка»: порядок размазан по четырём Subscribe.
func wireServices(bus *EventBus) {
	// PaymentService: слышит OrderPlaced -> берёт оплату -> выпускает PaymentTaken.
	bus.Subscribe("OrderPlaced", "PaymentService", func(b *EventBus, e choreoEvent) {
		e.result.PaymentID = doTakePayment(e.order)
		fmt.Printf("  PaymentService     : оплата           -> %s\n", e.result.PaymentID)
		b.Publish(choreoEvent{"PaymentTaken", e.order, e.result})
	})

	// StockService: слышит PaymentTaken -> резервирует -> выпускает StockReserved.
	bus.Subscribe("PaymentTaken", "StockService", func(b *EventBus, e choreoEvent) {
		e.result.ReservationID = doReserveStock(e.order)
		fmt.Printf("  StockService       : резерв товара    -> %s\n", e.result.ReservationID)
		b.Publish(choreoEvent{"StockReserved", e.order, e.result})
	})

	// ShippingService: слышит StockReserved -> создаёт доставку -> выпускает ShipmentCreated.
	bus.Subscribe("StockReserved", "ShippingService", func(b *EventBus, e choreoEvent) {
		e.result.ShipmentID = doCreateShipment(e.order)
		fmt.Printf("  ShippingService    : создание доставки-> %s\n", e.result.ShipmentID)
		b.Publish(choreoEvent{"ShipmentCreated", e.order, e.result})
	})

	// NotificationService: слышит ShipmentCreated -> уведомляет. Дальше событий нет.
	bus.Subscribe("ShipmentCreated", "NotificationService", func(b *EventBus, e choreoEvent) {
		e.result.Notification = doNotifyCustomer(e.order)
		fmt.Printf("  NotificationService: уведомление      -> %s\n", e.result.Notification)
		b.Publish(choreoEvent{"OrderCompleted", e.order, e.result})
	})
}

// runChoreography поднимает шину, подписывает сервисы и бросает первое событие
// OrderPlaced. Дальше всё происходит само — по подпискам. Возвращает итог для
// сравнения с оркестрацией.
func runChoreography(order Order) OrderResult {
	line := strings.Repeat("=", 78)
	fmt.Println(line)
	fmt.Println("ХОРЕОГРАФИЯ (event-bus): сценария нет — только реакции на события")
	fmt.Println(line)

	bus := NewEventBus()
	wireServices(bus)

	var res OrderResult
	res.OrderID = order.ID

	fmt.Println("публикую OrderPlaced — дальше поток складывается сам, по подпискам:")
	bus.Publish(choreoEvent{"OrderPlaced", order, &res})

	// «Весь поток» целиком не записан нигде в коде — восстанавливаем его ТОЛЬКО
	// из лога реакций. Это и есть цена хореографии за развязку сервисов.
	fmt.Println()
	fmt.Println("восстановленный поток (единственный способ увидеть его целиком):")
	for i, s := range bus.flow {
		fmt.Printf("  %d. %s\n", i+1, s)
	}
	fmt.Println("итог:", res)
	fmt.Println()
	return res
}
