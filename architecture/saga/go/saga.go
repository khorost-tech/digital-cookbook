// Пакет саги: воркфлоу-оркестратор «оформить заказ», активити-шаги и их компенсации.
//
// Оркестрация-based сага на Temporal. Один воркфлоу (OrderSaga) явно управляет
// последовательностью шагов и — при сбое — запускает компенсации в ОБРАТНОМ порядке.
//
// Классификация шагов (отражена в коде и логах):
//   1. ОПЛАТА     — compensatable (компенсация: возврат средств)
//   2. РЕЗЕРВ      — compensatable (компенсация: снятие резерва)
//   3. ОТГРУЗКА    — PIVOT: необратима, компенсации НЕТ, за ней откат невозможен (только вперёд)
//   4. УВЕДОМЛЕНИЕ — retriable: best-effort, его сбой сагу не валит
//
// Ключевые свойства, которые демонстрирует стенд:
//   - Компенсация ≠ rollback: это семантически обратные бизнес-действия, выполняемые
//     в обратном порядке относительно прямых шагов (сначала снять резерв, затем вернуть оплату).
//   - Идемпотентность шагов И компенсаций: повторный вызов активити (Temporal сам
//     ретраит) не задваивает эффект — защита ключом идемпотентности.
//   - Semantic lock: заказ держится в статусе PENDING на время саги; параллельный
//     «читатель» видит промежуточный статус до перехода в CONFIRMED/CANCELLED.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ---------------------------------------------------------------------------
// Модель состояния (in-memory). Postgres для стенда не нужен: цель — показать
// саму сагу, а не хранилище. Все операции под мьютексом — активити воркера и
// параллельный «читатель» в main работают с этой мапой конкурентно.
// ---------------------------------------------------------------------------

// Статусы заказа. PENDING — semantic lock на время саги.
const (
	StatusPending   = "PENDING"
	StatusConfirmed = "CONFIRMED"
	StatusCancelled = "CANCELLED"
)

// Order — запись заказа с текущим статусом (semantic lock живёт здесь).
type Order struct {
	ID      string
	Account string
	SKU     string
	Amount  int
	Qty     int
	Status  string
	Shipped bool
}

// Store — общее in-memory состояние: заказы, балансы счетов, резервы по SKU,
// журнал применённых ключей идемпотентности, счётчик «флапов» платёжного шлюза
// и лог фактического порядка выполненных компенсаций (для ассертов).
type Store struct {
	mu       sync.Mutex
	orders   map[string]*Order
	balances map[string]int      // account -> баланс средств
	reserved map[string]int      // sku -> зарезервированное количество
	applied  map[string]bool     // ключ идемпотентности -> применён ли эффект
	flaky    map[string]int      // orderID -> число физических вызовов оплаты (симуляция ретрая)
	compLog  map[string][]string // orderID -> порядок фактически отработавших компенсаций
}

// NewStore создаёт пустое состояние.
func NewStore() *Store {
	return &Store{
		orders:   make(map[string]*Order),
		balances: make(map[string]int),
		reserved: make(map[string]int),
		applied:  make(map[string]bool),
		flaky:    make(map[string]int),
		compLog:  make(map[string][]string),
	}
}

// SetBalance задаёт стартовый баланс счёта (используется main перед сценарием).
func (s *Store) SetBalance(account string, amount int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.balances[account] = amount
}

// Balance возвращает текущий баланс счёта.
func (s *Store) Balance(account string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.balances[account]
}

// Reserved возвращает зарезервированное количество по SKU.
func (s *Store) Reserved(sku string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserved[sku]
}

// Status возвращает статус заказа (или пустую строку, если заказа ещё нет).
// Именно этот метод дёргает параллельный «читатель» — semantic lock.
func (s *Store) Status(orderID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.orders[orderID]; o != nil {
		return o.Status
	}
	return ""
}

// CompLog возвращает копию журнала отработавших компенсаций для заказа.
func (s *Store) CompLog(orderID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.compLog[orderID]))
	copy(out, s.compLog[orderID])
	return out
}

// openOrder заводит заказ в статусе PENDING (semantic lock защёлкивается).
func (s *Store) openOrder(in OrderInput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders[in.OrderID] = &Order{
		ID: in.OrderID, Account: in.Account, SKU: in.SKU,
		Amount: in.Amount, Qty: in.Qty, Status: StatusPending,
	}
}

// setStatus переводит заказ в терминальный статус (снимает semantic lock).
func (s *Store) setStatus(orderID, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.orders[orderID]; o != nil {
		o.Status = status
	}
}

// charge списывает средства ИДЕМПОТЕНТНО по ключу "charge:<orderID>".
// Возвращает applied=true, если списание реально произошло; false — повторный
// вызов (эффект уже был применён ранее, деньги повторно НЕ списываются).
func (s *Store) charge(orderID, account string, amount int) (applied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "charge:" + orderID
	if s.applied[key] {
		return false
	}
	s.balances[account] -= amount
	s.applied[key] = true
	return true
}

// refund возвращает средства ИДЕМПОТЕНТНО (компенсация оплаты), ключ "refund:<orderID>".
func (s *Store) refund(orderID, account string, amount int) (applied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "refund:" + orderID
	if s.applied[key] {
		return false
	}
	s.balances[account] += amount
	s.applied[key] = true
	s.compLog[orderID] = append(s.compLog[orderID], "payment") // возврат оплаты отработал
	return true
}

// reserve резервирует Qty единиц SKU ИДЕМПОТЕНТНО, ключ "reserve:<orderID>".
func (s *Store) reserve(orderID, sku string, qty int) (applied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "reserve:" + orderID
	if s.applied[key] {
		return false
	}
	s.reserved[sku] += qty
	s.applied[key] = true
	return true
}

// unreserve снимает резерв ИДЕМПОТЕНТНО (компенсация резерва), ключ "unreserve:<orderID>".
// Если резерв не был применён (reserve не отрабатывал) — компенсация становится
// no-op: unreserve выставляет свой ключ, но количество не трогает (нечего снимать).
func (s *Store) unreserve(orderID, sku string, qty int) (applied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "unreserve:" + orderID
	if s.applied[key] {
		return false
	}
	if s.applied["reserve:"+orderID] {
		s.reserved[sku] -= qty
	}
	s.applied[key] = true
	s.compLog[orderID] = append(s.compLog[orderID], "reserve") // снятие резерва отработало
	return true
}

// ship отмечает отгрузку (PIVOT). Необратима, компенсации нет.
func (s *Store) ship(orderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.orders[orderID]; o != nil {
		o.Shipped = true
	}
}

// flakyHit имитирует «потерю ответа» платёжного шлюза: возвращает true только
// на ПЕРВОМ физическом вызове оплаты для заказа (эффект уже применён, но ответ
// «потерян» → Temporal ретраит; на втором вызове — false, шаг завершается успешно).
func (s *Store) flakyHit(orderID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flaky[orderID]++
	return s.flaky[orderID] == 1
}

// ---------------------------------------------------------------------------
// Входные данные воркфлоу и активити.
// ---------------------------------------------------------------------------

// OrderInput — параметры саги. FlakyPayment/FailReserve — переключатели инъекции сбоев.
type OrderInput struct {
	OrderID string
	Account string
	SKU     string
	Amount  int  // сумма к списанию/возврату
	Qty     int  // количество к резерву/снятию
	FlakyPayment bool // оплата «потеряет ответ» на 1-й попытке → ретрай (демонстрация идемпотентности)
	FailReserve  bool // резерв применит эффект, но провалит пост-проверку → компенсация всей саги
}

// CloseInput — параметры активити закрытия заказа (перевод в терминальный статус).
type CloseInput struct {
	OrderID string
	Status  string
}

// ---------------------------------------------------------------------------
// Активити: прямые шаги и компенсации. Держат ссылку на общий Store.
// Все активити ИДЕМПОТЕНТНЫ — Temporal может ретраить любую из них.
// ---------------------------------------------------------------------------

// Activities — набор активити, привязанных к общему состоянию.
type Activities struct {
	store *Store
}

// OpenOrder — служебный шаг: завести заказ в статусе PENDING (semantic lock).
func (a *Activities) OpenOrder(ctx context.Context, in OrderInput) error {
	a.store.openOrder(in)
	activity.GetLogger(ctx).Info("semantic lock: заказ открыт в статусе PENDING", "order", in.OrderID)
	return nil
}

// ChargePayment — ШАГ 1 (compensatable). Списывает средства идемпотентно.
func (a *Activities) ChargePayment(ctx context.Context, in OrderInput) error {
	log := activity.GetLogger(ctx)
	if a.store.charge(in.OrderID, in.Account, in.Amount) {
		log.Info("ОПЛАТА [compensatable]: списано", "order", in.OrderID, "amount", in.Amount)
	} else {
		log.Info("ОПЛАТА [compensatable]: идемпотентный повтор — эффект уже применён, повторно НЕ списываем", "order", in.OrderID)
	}
	if in.FlakyPayment && a.store.flakyHit(in.OrderID) {
		// эффект уже применён выше, но «ответ шлюза потерян» — вернём retriable-ошибку.
		// Temporal ретратит активити; идемпотентность гарантирует одно списание.
		log.Warn("ОПЛАТА: ответ платёжного шлюза «потерян» (симуляция) — Temporal повторит активити", "order", in.OrderID)
		return fmt.Errorf("транзиентный сбой: платёжный шлюз не подтвердил списание")
	}
	return nil
}

// RefundPayment — КОМПЕНСАЦИЯ ШАГА 1. Возвращает средства идемпотентно.
func (a *Activities) RefundPayment(ctx context.Context, in OrderInput) error {
	log := activity.GetLogger(ctx)
	if a.store.refund(in.OrderID, in.Account, in.Amount) {
		log.Info("КОМПЕНСАЦИЯ оплаты: средства возвращены", "order", in.OrderID, "amount", in.Amount)
	} else {
		log.Info("КОМПЕНСАЦИЯ оплаты: идемпотентный повтор — возврат уже выполнен", "order", in.OrderID)
	}
	return nil
}

// ReserveStock — ШАГ 2 (compensatable). Резервирует товар идемпотентно.
// При FailReserve эффект применяется, но затем срабатывает бизнес-пост-проверка
// (например, «риск-контроль не пройден») → non-retryable ошибка запускает откат саги.
func (a *Activities) ReserveStock(ctx context.Context, in OrderInput) error {
	log := activity.GetLogger(ctx)
	if a.store.reserve(in.OrderID, in.SKU, in.Qty) {
		log.Info("РЕЗЕРВ [compensatable]: товар зарезервирован", "order", in.OrderID, "sku", in.SKU, "qty", in.Qty)
	} else {
		log.Info("РЕЗЕРВ [compensatable]: идемпотентный повтор — резерв уже применён", "order", in.OrderID)
	}
	if in.FailReserve {
		log.Error("РЕЗЕРВ: пост-проверка не пройдена (симуляция бизнес-отказа) — сага откатывается", "order", in.OrderID)
		return temporal.NewNonRetryableApplicationError(
			"резерв применён, но проверка риска не пройдена", "RiskCheckFailed", nil)
	}
	return nil
}

// UnreserveStock — КОМПЕНСАЦИЯ ШАГА 2. Снимает резерв идемпотентно.
func (a *Activities) UnreserveStock(ctx context.Context, in OrderInput) error {
	log := activity.GetLogger(ctx)
	if a.store.unreserve(in.OrderID, in.SKU, in.Qty) {
		log.Info("КОМПЕНСАЦИЯ резерва: резерв снят", "order", in.OrderID, "sku", in.SKU, "qty", in.Qty)
	} else {
		log.Info("КОМПЕНСАЦИЯ резерва: идемпотентный повтор — резерв уже снят", "order", in.OrderID)
	}
	return nil
}

// ShipOrder — ШАГ 3 (PIVOT). Необратимая точка: компенсации НЕТ, назад дороги нет.
func (a *Activities) ShipOrder(ctx context.Context, in OrderInput) error {
	a.store.ship(in.OrderID)
	activity.GetLogger(ctx).Info("ОТГРУЗКА [PIVOT]: точка невозврата пройдена — компенсации нет", "order", in.OrderID)
	return nil
}

// NotifyCustomer — ШАГ 4 (retriable, best-effort). Его сбой не валит сагу.
func (a *Activities) NotifyCustomer(ctx context.Context, in OrderInput) error {
	activity.GetLogger(ctx).Info("УВЕДОМЛЕНИЕ [retriable]: клиент оповещён", "order", in.OrderID)
	return nil
}

// CloseOrder — служебный шаг: перевод заказа в терминальный статус (снятие semantic lock).
func (a *Activities) CloseOrder(ctx context.Context, in CloseInput) error {
	a.store.setStatus(in.OrderID, in.Status)
	activity.GetLogger(ctx).Info("semantic lock снят: заказ переведён в терминальный статус", "order", in.OrderID, "status", in.Status)
	return nil
}

// ---------------------------------------------------------------------------
// Воркфлоу-оркестратор. Компенсации регистрируются В СТЕК ПЕРЕД выполнением шага
// (чтобы частично применённый эффект тоже был компенсирован) и при сбое
// выполняются в ОБРАТНОМ порядке.
// ---------------------------------------------------------------------------

// compensation — зарегистрированная компенсация: имя (для лога) и активити.
type compensation struct {
	name string
	act  interface{}
}

// OrderSaga — воркфлоу саги «оформить заказ». Возвращает итоговый статус
// (CONFIRMED | CANCELLED).
func OrderSaga(ctx workflow.Context, in OrderInput) (string, error) {
	log := workflow.GetLogger(ctx)

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    200 * time.Millisecond,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Second,
			MaximumAttempts:    5,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var a *Activities // nil-receiver только для получения имён активити по рефлексии

	// Семантический замок: заказ создаётся в статусе PENDING.
	if err := workflow.ExecuteActivity(ctx, a.OpenOrder, in).Get(ctx, nil); err != nil {
		return "", err
	}

	var comps []compensation

	// compensate выполняет зарегистрированные компенсации в ОБРАТНОМ порядке.
	// Используем disconnected-контекст: компенсации должны отработать, даже если
	// исходный контекст был бы отменён.
	compensate := func() {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		dctx = workflow.WithActivityOptions(dctx, ao)
		log.Info("САГА: сбой — запускаю компенсации в ОБРАТНОМ порядке", "count", len(comps))
		for i := len(comps) - 1; i >= 0; i-- {
			c := comps[i]
			log.Info("САГА: компенсация", "order", i, "step", c.name)
			if err := workflow.ExecuteActivity(dctx, c.act, in).Get(dctx, nil); err != nil {
				log.Error("САГА: компенсация не удалась", "step", c.name, "err", err)
			}
		}
	}

	// cancel завершает сагу неуспехом: компенсирует и переводит заказ в CANCELLED.
	cancel := func(reason string) (string, error) {
		log.Error("САГА: откат", "reason", reason)
		compensate()
		_ = workflow.ExecuteActivity(ctx, a.CloseOrder, CloseInput{in.OrderID, StatusCancelled}).Get(ctx, nil)
		return StatusCancelled, nil
	}

	// ---- ШАГ 1: ОПЛАТА (compensatable) --------------------------------------
	// Компенсацию регистрируем ДО выполнения шага.
	comps = append(comps, compensation{"компенсация ОПЛАТЫ (возврат)", a.RefundPayment})
	if err := workflow.ExecuteActivity(ctx, a.ChargePayment, in).Get(ctx, nil); err != nil {
		return cancel("оплата не удалась: " + err.Error())
	}

	// ---- ШАГ 2: РЕЗЕРВ (compensatable) --------------------------------------
	comps = append(comps, compensation{"компенсация РЕЗЕРВА (снятие)", a.UnreserveStock})
	if err := workflow.ExecuteActivity(ctx, a.ReserveStock, in).Get(ctx, nil); err != nil {
		return cancel("резерв не удался: " + err.Error())
	}

	// ---- ШАГ 3: ОТГРУЗКА (PIVOT — необратима, компенсации нет) ---------------
	if err := workflow.ExecuteActivity(ctx, a.ShipOrder, in).Get(ctx, nil); err != nil {
		// За пивотом откатываться нельзя — только двигаться вперёд (ретраи уже
		// исчерпаны политикой). Возвращаем ошибку воркфлоу без компенсаций.
		return "", err
	}

	// ---- ШАГ 4: УВЕДОМЛЕНИЕ (retriable, best-effort — сагу не валит) ---------
	if err := workflow.ExecuteActivity(ctx, a.NotifyCustomer, in).Get(ctx, nil); err != nil {
		log.Warn("САГА: уведомление не удалось (best-effort) — не откатываем", "err", err)
	}

	// Успех: снимаем semantic lock переводом в CONFIRMED.
	if err := workflow.ExecuteActivity(ctx, a.CloseOrder, CloseInput{in.OrderID, StatusConfirmed}).Get(ctx, nil); err != nil {
		return "", err
	}
	return StatusConfirmed, nil
}
