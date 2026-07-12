// Command event-payload-cookbook — живой стенд к статье «Что кладём в событие:
// notification vs event-carried state transfer» на khorost.tech
// (/architecture/event-notification-vs-state-transfer/).
//
// Один и тот же поток «заказ оплачен» проигрывается в двух формах события —
// тонком (notification) и толстом (event-carried state transfer) — и сравнивается
// по трём измеримым осям:
//
//   - обращения к источнику: сколько раз консьюмеры дёрнули source.Get(orderID);
//   - размер payload: байты сериализованного (JSON) события в каждом варианте;
//   - свежесть данных: видит ли консьюмер изменение, внесённое в источник ПОСЛЕ
//     публикации события (staleness толстого события против «GET-storm» тонкого).
//
// Всё in-process, без брокеров и docker: источник — мапа под мьютексом с
// счётчиком обращений, «шина» — срез событий, консьюмеры — обычные функции.
// Это полностью показывает суть контраста; NATS усложнил бы стенд, не добавив
// к демонстрации ничего по существу.
//
// Запуск:
//
//	cd event-payload/go && go run .
//
// Программа сама проверяет инварианты (log.Fatalf при расхождении) и печатает
// итоговую таблицу метрик.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
)

// numConsumers — сколько независимых потребителей реагирует на каждое событие.
// Именно этот множитель превращает тонкое событие в «GET-storm»: на N консьюмеров
// приходится N обращений к источнику на КАЖДОЕ событие.
const numConsumers = 4

// Order — доменная сущность «заказ». В толстое событие кладётся её полная копия,
// в тонкое — только Number (идентификатор).
type Order struct {
	Number   string   `json:"orderId"`
	Status   string   `json:"status"`
	Amount   int64    `json:"amount"` // в минимальных единицах (копейки/центы)
	Currency string   `json:"currency"`
	Items    []string `json:"items"`
}

// -----------------------------------------------------------------------------
// Источник истины (owning service)
// -----------------------------------------------------------------------------

// Source — сервис-владелец заказов. Хранит состояние в мапе под мьютексом и
// считает, сколько раз к нему обратились за деталями. Флаг available моделирует
// временную недоступность источника (сеть/деплой/перегрузка).
type Source struct {
	mu        sync.Mutex
	orders    map[string]Order
	getCount  int  // сколько раз вызвали Get — метрика «обращений к источнику»
	available bool // false → источник недоступен, Get вернёт ошибку
}

func NewSource() *Source {
	return &Source{
		orders:    make(map[string]Order),
		available: true,
	}
}

// Put/Update — источник кладёт или обновляет заказ у себя. Обращением «наружу»
// не считается (это внутренняя мутация владельца).
func (s *Source) Put(o Order) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders[o.Number] = o
}

// Get — единственный способ для консьюмера дотянуться до деталей заказа.
// Каждый вызов инкрементит getCount. Если источник недоступен — возвращает ошибку
// (демонстрация ВРЕМЕННОЙ связанности тонкого события с источником).
func (s *Source) Get(orderID string) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCount++
	if !s.available {
		return Order{}, fmt.Errorf("source unavailable: cannot fetch %s", orderID)
	}
	o, ok := s.orders[orderID]
	if !ok {
		return Order{}, fmt.Errorf("order %s not found", orderID)
	}
	return o, nil
}

func (s *Source) GetCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCount
}

func (s *Source) resetCount() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCount = 0
}

func (s *Source) setAvailable(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.available = v
}

// -----------------------------------------------------------------------------
// Формы события
// -----------------------------------------------------------------------------

// NotificationEvent — тонкое событие: несёт только идентификатор. Консьюмер
// ОБЯЗАН сходить в источник за деталями.
type NotificationEvent struct {
	Type    string `json:"type"`
	OrderID string `json:"orderId"`
}

// StateTransferEvent — толстое событие (event-carried state transfer): несёт
// полный снимок состояния заказа. Консьюмер автономен, но снимок может устареть.
type StateTransferEvent struct {
	Type  string `json:"type"`
	Order Order  `json:"order"`
}

// -----------------------------------------------------------------------------
// Результат прогона одного варианта
// -----------------------------------------------------------------------------

type result struct {
	variant     string
	sourceHits  int    // суммарно обращений к источнику при обработке события
	payloadSize int    // байт сериализованного события
	seenStatus  string // какой статус заказа «увидели» консьюмеры
	fresh       bool   // отражает ли увиденный статус свежее изменение источника
}

// -----------------------------------------------------------------------------
// Сценарий
// -----------------------------------------------------------------------------

func main() {
	const orderID = "ORD-42"

	// Исходное состояние заказа на момент оплаты.
	initial := Order{
		Number:   orderID,
		Status:   "PAID",
		Amount:   1599900, // 15 999.00
		Currency: "RUB",
		Items:    []string{"keyboard-noon-edition", "wrist-rest", "usb-c-cable"},
	}

	fmt.Println("Стенд: notification vs event-carried state transfer")
	fmt.Printf("Поток «заказ оплачен», %d консьюмера(ов) на событие, заказ %s (status=%s)\n\n",
		numConsumers, orderID, initial.Status)

	notif := runNotification(initial)
	state := runStateTransfer(initial)

	printTable(notif, state)
	printPayloads(orderID, initial)
	verify(notif, state)

	fmt.Println("\nOK: все инварианты выполнены.")
}

// runNotification проигрывает тонкое событие.
//
// Ключевой момент staleness-теста: источник МЕНЯЕТ заказ уже ПОСЛЕ публикации
// события (PAID → REFUNDED). Тонкий консьюмер читает детали лениво, в момент
// обработки, — и поэтому видит уже свежий статус. Плата — numConsumers обращений
// к источнику на одно событие (GET-storm).
func runNotification(initial Order) result {
	src := NewSource()
	src.Put(initial)

	// Публикуем тонкое событие.
	ev := NotificationEvent{Type: "order.paid", OrderID: initial.Number}
	payload, err := json.Marshal(ev)
	if err != nil {
		log.Fatalf("notification: marshal: %v", err)
	}

	// Источник обновляет заказ ПОСЛЕ публикации события.
	updated := initial
	updated.Status = "REFUNDED"
	src.Put(updated)

	// N консьюмеров обрабатывают одно и то же событие: каждый идёт в источник.
	src.resetCount()
	var seen string
	for i := 0; i < numConsumers; i++ {
		var got NotificationEvent
		if err := json.Unmarshal(payload, &got); err != nil {
			log.Fatalf("notification: unmarshal: %v", err)
		}
		o, err := src.Get(got.OrderID) // ← обращение к источнику
		if err != nil {
			log.Fatalf("notification: consumer %d: %v", i, err)
		}
		seen = o.Status
	}
	hits := src.GetCount() // фиксируем GET-storm ДО демонстрации недоступности

	// Демонстрация временной связанности: если источник недоступен —
	// тонкий консьюмер не может обработать событие вообще.
	src.setAvailable(false)
	if _, err := src.Get(initial.Number); err == nil {
		log.Fatalf("notification: ожидалась ошибка недоступного источника")
	} else {
		fmt.Printf("[notification] источник недоступен → консьюмер не может обработать событие: %v\n", err)
	}
	src.setAvailable(true)

	return result{
		variant:     "notification",
		sourceHits:  hits,
		payloadSize: len(payload),
		seenStatus:  seen,
		fresh:       seen == updated.Status, // увидели свежий REFUNDED?
	}
}

// runStateTransfer проигрывает толстое событие.
//
// Событие несёт снимок состояния на момент публикации (PAID). Источник затем
// меняет заказ (PAID → REFUNDED), но консьюмеры работают по снимку внутри события
// и в источник не ходят вовсе — 0 обращений. Плата — staleness: свежий REFUNDED
// они не видят.
func runStateTransfer(initial Order) result {
	src := NewSource()
	src.Put(initial)

	// Публикуем толстое событие — снимок ПОЛНОГО состояния на текущий момент.
	ev := StateTransferEvent{Type: "order.paid", Order: initial}
	payload, err := json.Marshal(ev)
	if err != nil {
		log.Fatalf("state-transfer: marshal: %v", err)
	}

	// Источник обновляет заказ ПОСЛЕ публикации события — как и в тонком варианте.
	updated := initial
	updated.Status = "REFUNDED"
	src.Put(updated)

	// N консьюмеров обрабатывают событие автономно: читают состояние из payload,
	// в источник не обращаются.
	src.resetCount()
	var seen string
	for i := 0; i < numConsumers; i++ {
		var got StateTransferEvent
		if err := json.Unmarshal(payload, &got); err != nil {
			log.Fatalf("state-transfer: unmarshal: %v", err)
		}
		seen = got.Order.Status // ← данные из события, без похода в источник
	}

	// Контроль: показываем, что источник УЖЕ содержит свежий статус,
	// которого толстый консьюмер не увидел.
	if o, err := src.Get(initial.Number); err == nil {
		fmt.Printf("[state-transfer] источник уже содержит status=%s, но консьюмер увидел status=%s (staleness)\n",
			o.Status, seen)
	}
	// Один Get выше — контрольный, для наглядности лога; в метрику автономности
	// консьюмеров он не входит (они не обращались к источнику вообще).
	hits := src.GetCount() - 1

	return result{
		variant:     "state-transfer",
		sourceHits:  hits,
		payloadSize: len(payload),
		seenStatus:  seen,
		fresh:       seen == updated.Status, // false: увидели старый PAID
	}
}

// -----------------------------------------------------------------------------
// Вывод и проверки
// -----------------------------------------------------------------------------

func printTable(rs ...result) {
	fmt.Println("\n" + strings.Repeat("─", 74))
	fmt.Printf("%-16s | %-20s | %-14s | %-14s\n",
		"вариант", "обращений к источн.", "payload, байт", "свежие данные")
	fmt.Println(strings.Repeat("─", 74))
	for _, r := range rs {
		fresh := "нет (staleness)"
		if r.fresh {
			fresh = "да"
		}
		fmt.Printf("%-16s | %-20d | %-14d | %-14s\n",
			r.variant, r.sourceHits, r.payloadSize, fresh)
	}
	fmt.Println(strings.Repeat("─", 74))
}

// printPayloads печатает фактические сериализованные события, чтобы разница в
// размере была не абстрактным числом, а видимой глазами.
func printPayloads(orderID string, o Order) {
	n, _ := json.Marshal(NotificationEvent{Type: "order.paid", OrderID: orderID})
	s, _ := json.Marshal(StateTransferEvent{Type: "order.paid", Order: o})
	fmt.Printf("\nnotification  payload (%d байт): %s\n", len(n), n)
	fmt.Printf("state-transfer payload (%d байт): %s\n", len(s), s)
	if len(s) > len(n) {
		fmt.Printf("толстое событие тяжелее в %.1f× (+%d байт)\n",
			float64(len(s))/float64(len(n)), len(s)-len(n))
	}
}

// verify проверяет инварианты статьи. Расхождение — log.Fatalf.
func verify(notif, state result) {
	// Тонкое событие: обращения к источнику > 0 и данные свежие.
	if notif.sourceHits <= 0 {
		log.Fatalf("инвариант нарушен: notification должно обращаться к источнику (>0), получили %d", notif.sourceHits)
	}
	if notif.sourceHits != numConsumers {
		log.Fatalf("инвариант нарушен: ожидали %d обращений (GET-storm), получили %d", numConsumers, notif.sourceHits)
	}
	if !notif.fresh {
		log.Fatalf("инвариант нарушен: notification должно видеть свежие данные")
	}
	// Толстое событие: 0 обращений, но staleness.
	if state.sourceHits != 0 {
		log.Fatalf("инвариант нарушен: state-transfer должно быть автономным (0 обращений), получили %d", state.sourceHits)
	}
	if state.fresh {
		log.Fatalf("инвариант нарушен: state-transfer должно демонстрировать staleness (устаревший снимок)")
	}
	// Размер: толстое событие крупнее тонкого.
	if state.payloadSize <= notif.payloadSize {
		log.Fatalf("инвариант нарушен: state-transfer payload (%d) должен быть больше notification (%d)",
			state.payloadSize, notif.payloadSize)
	}
}
