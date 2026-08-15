// Точка входа стенда «Saga на практике» — https://khorost.tech/architecture/saga-in-practice/
//
// Оркестрация-based сага «оформить заказ» на Temporal с компенсациями.
// Процесс поднимает воркера (обслуживает task queue) и клиента (запускает воркфлоу),
// затем прогоняет ДВА сценария против живого dev-сервера Temporal (localhost:7243):
//
//   Сценарий A — успешный путь (с транзиентным сбоем оплаты):
//     оплата «теряет ответ» на 1-й попытке → Temporal ретраит активити →
//     идемпотентность гарантирует ОДНО списание → резерв → отгрузка (PIVOT) →
//     уведомление → заказ CONFIRMED.
//
//   Сценарий B — отказ на шаге резерва:
//     оплата проходит → резерв применяется, но проваливает пост-проверку →
//     сага откатывается: компенсации в ОБРАТНОМ порядке (снятие резерва, затем
//     возврат оплаты) → заказ CANCELLED, баланс возвращён.
//
// Параллельно с каждым воркфлоу работает «читатель», который опрашивает статус
// заказа в общем Store и фиксирует, что во время саги он виден как PENDING
// (semantic lock), а по завершении — CONFIRMED/CANCELLED.
//
// Запуск (dev-сервер Temporal должен быть поднят, см. README):
//
//	docker compose -f saga/compose/compose.yml up -d
//	cd saga/go && go run .
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"reflect"
	"time"

	"go.temporal.io/sdk/client"
)

func main() {
	store := NewStore()

	c, err := newClient()
	if err != nil {
		log.Fatalf("не удалось подключиться к Temporal на %s: %v (поднят ли dev-сервер? см. README)", HostPort, err)
	}
	defer c.Close()

	w := newWorker(c, store)
	if err := w.Start(); err != nil {
		log.Fatalf("не удалось запустить воркера: %v", err)
	}
	defer w.Stop()

	ctx := context.Background()
	allOK := true

	// ---- Сценарий A: успешный путь (с ретраем оплаты) -----------------------
	fmt.Println("\n================ СЦЕНАРИЙ A: успешный путь (CONFIRMED) ================")
	inA := OrderInput{
		OrderID: "order-success", Account: "acc-A", SKU: "sku-widget",
		Amount: 1000, Qty: 2, FlakyPayment: true, FailReserve: false,
	}
	store.SetBalance(inA.Account, 5000)
	resA := runScenario(ctx, c, store, inA)
	allOK = checkSuccess(store, inA, resA) && allOK

	// ---- Сценарий B: отказ на резерве (откат, CANCELLED) --------------------
	fmt.Println("\n================ СЦЕНАРИЙ B: отказ на резерве (CANCELLED) ================")
	inB := OrderInput{
		OrderID: "order-fail", Account: "acc-B", SKU: "sku-gadget",
		Amount: 1500, Qty: 3, FlakyPayment: false, FailReserve: true,
	}
	store.SetBalance(inB.Account, 5000)
	resB := runScenario(ctx, c, store, inB)
	allOK = checkFailure(store, inB, resB) && allOK

	fmt.Println("\n================================================================")
	if allOK {
		fmt.Println("ИТОГ: все ассерты зелёные.")
	} else {
		fmt.Println("ИТОГ: есть падения ассертов (см. выше).")
		os.Exit(1)
	}
}

// runScenario запускает воркфлоу и параллельного «читателя», ждёт результат.
func runScenario(ctx context.Context, c client.Client, store *Store, in OrderInput) string {
	// «Читатель»: опрашивает статус заказа, фиксирует наблюдаемые переходы.
	readerCtx, stopReader := context.WithCancel(ctx)
	observed := make(chan []string, 1)
	go func() {
		var seq []string
		last := ""
		sawPending := false
		for {
			select {
			case <-readerCtx.Done():
				observed <- seq
				return
			default:
			}
			st := store.Status(in.OrderID)
			if st != "" && st != last {
				seq = append(seq, st)
				last = st
				if st == StatusPending {
					sawPending = true
					fmt.Printf("  [ЧИТАТЕЛЬ] заказ %s виден в статусе PENDING — semantic lock держит сагу\n", in.OrderID)
				} else {
					fmt.Printf("  [ЧИТАТЕЛЬ] заказ %s перешёл в терминальный статус %s\n", in.OrderID, st)
				}
			}
			_ = sawPending
			time.Sleep(15 * time.Millisecond)
		}
	}()

	we, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        "saga-" + in.OrderID,
		TaskQueue: TaskQueue,
	}, OrderSaga, in)
	if err != nil {
		stopReader()
		<-observed
		log.Fatalf("не удалось запустить воркфлоу: %v", err)
	}
	fmt.Printf("  воркфлоу запущен: WorkflowID=%s RunID=%s\n", we.GetID(), we.GetRunID())

	var result string
	if err := we.Get(ctx, &result); err != nil {
		stopReader()
		<-observed
		log.Fatalf("воркфлоу завершился ошибкой: %v", err)
	}

	// Дать читателю шанс увидеть терминальный статус, затем остановить.
	time.Sleep(60 * time.Millisecond)
	stopReader()
	seq := <-observed
	fmt.Printf("  наблюдённые читателем статусы: %v\n", seq)
	fmt.Printf("  итоговый результат воркфлоу: %s\n", result)
	return result
}

// checkSuccess проверяет ассерты успешного сценария.
func checkSuccess(store *Store, in OrderInput, result string) bool {
	ok := true
	ok = assert(result == StatusConfirmed, fmt.Sprintf("результат == CONFIRMED (получено %q)", result)) && ok
	ok = assert(store.Status(in.OrderID) == StatusConfirmed, "статус заказа в Store == CONFIRMED") && ok
	// идемпотентность: несмотря на ретрай оплаты, списано РОВНО один раз.
	wantBalance := 5000 - in.Amount
	ok = assert(store.Balance(in.Account) == wantBalance,
		fmt.Sprintf("баланс == %d (списано ровно один раз, идемпотентность): получено %d", wantBalance, store.Balance(in.Account))) && ok
	ok = assert(store.Reserved(in.SKU) == in.Qty,
		fmt.Sprintf("резерв == %d (отгружено, не снято): получено %d", in.Qty, store.Reserved(in.SKU))) && ok
	// оплата действительно ретраилась (>=2 физических вызова) — иначе идемпотентность нечем показать.
	ok = assert(store.flaky[in.OrderID] >= 2,
		fmt.Sprintf("оплата ретраилась Temporal-ом (>=2 физич. вызова): получено %d", store.flaky[in.OrderID])) && ok
	ok = assert(len(store.CompLog(in.OrderID)) == 0, "компенсаций не было (успешный путь)") && ok
	return ok
}

// checkFailure проверяет ассерты сценария с откатом.
func checkFailure(store *Store, in OrderInput, result string) bool {
	ok := true
	ok = assert(result == StatusCancelled, fmt.Sprintf("результат == CANCELLED (получено %q)", result)) && ok
	ok = assert(store.Status(in.OrderID) == StatusCancelled, "статус заказа в Store == CANCELLED") && ok
	// компенсация оплаты вернула средства → баланс восстановлен.
	ok = assert(store.Balance(in.Account) == 5000,
		fmt.Sprintf("баланс возвращён к 5000 (компенсация оплаты): получено %d", store.Balance(in.Account))) && ok
	// компенсация резерва сняла резерв.
	ok = assert(store.Reserved(in.SKU) == 0,
		fmt.Sprintf("резерв снят (компенсация резерва): получено %d", store.Reserved(in.SKU))) && ok
	// ПОРЯДОК компенсаций: обратный порядку прямых шагов — сначала резерв, затем оплата.
	compLog := store.CompLog(in.OrderID)
	want := []string{"reserve", "payment"}
	ok = assert(reflect.DeepEqual(compLog, want),
		fmt.Sprintf("компенсации в ОБРАТНОМ порядке %v (снятие резерва → возврат оплаты): получено %v", want, compLog)) && ok
	return ok
}

// assert печатает результат проверки и возвращает её значение.
func assert(cond bool, msg string) bool {
	if cond {
		fmt.Printf("  [OK]   %s\n", msg)
	} else {
		fmt.Printf("  [FAIL] %s\n", msg)
	}
	return cond
}
