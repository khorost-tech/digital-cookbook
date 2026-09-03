package main

import (
	"encoding/json"
	"fmt"
)

// totalWire — сырая wire-форма одной строки customer.totals для разбора.
// Total — *float64, а не float64 (в отличие от total.Total ниже по
// конвейеру, см. clickhouse.go): нужно отличить ОТСУТСТВУЮЩЕЕ поле (nil) от
// явно присланного total:0 (валидного значения, см. комментарий у
// parseValidTotal ниже). encoding/json для *float64 оставляет nil, если
// поле в JSON отсутствует или явно null, и указывает на float64-значение
// (в том числе на 0.0), если поле присутствует — то самое различие,
// которое здесь нужно и которого НЕТ у обычного float64 (там "отсутствует"
// и "явный 0" неразличимы, оба дают нулевое значение типа).
type totalWire struct {
	CustomerID uint64   `json:"customer_id"`
	Orders     uint64   `json:"orders"`
	Total      *float64 `json:"total"`
}

// parseValidTotal разбирает и семантически валидирует одно сообщение
// totals-режима (топик customer.totals). Возвращает распарсенный агрегат и
// nil, если запись пригодна для витрины; иначе — error с точной причиной,
// по которой запись должна уйти в DLQ, а не в ClickHouse.
//
// json.Unmarshal ловит только СИНТАКСИС JSON: "{}" и "{"customer_id":1}"
// синтаксически валидны и анмаршалятся БЕЗ единой ошибки — первое в
// customer_id=0, orders=0, второе в customer_id=1, orders=0 (поле orders
// просто отсутствует — Go оставляет ему нулевое значение типа,
// encoding/json это не считает ошибкой). Оба семантически невалидны для
// реального снимка customer.totals и без проверок ниже тихо уехали бы в
// витрину как мусор.
//
// Диапазон клиентов этого стенда — 1..5 (см. scripts/seed.sh: "cust := 1 +
// (i % 5)") — customer_id вне этого диапазона недостижим ни для одного
// настоящего события.
//
// orders==0 отдельно и всегда невалиден: реальный агрегат customer.totals
// (TotalsProcessor.process(), см. streams/src/main/java/StreamsApp.java)
// инициализирует первую запись клиента уже с orders=1 ("long orders = 1" в
// начале метода) — нулевого orders в настоящем агрегате не бывает никогда.
// Различать в Go через отдельную структуру с указателями "поле отсутствует"
// от "поле явно равно нулю" здесь не нужно: orders==0 семантически
// невалиден одинаково в обоих случаях.
//
// total — ДРУГОЙ случай: разбирается через totalWire.Total (*float64,
// см. выше), а не сразу в float64, потому что здесь ОТСУТСТВИЕ поля и явный
// ноль СЕМАНТИЧЕСКИ РАЗНЫЕ вещи. total — такое же обязательное поле
// реального агрегата customer.totals, как и orders (см. TotalsProcessor.
// process(): out.put("total", ...) пишется безусловно вместе с orders) —
// отсутствующий total (nil-указатель) отвергается ниже отдельной проверкой,
// тем же путём в DLQ. Но total:0 — валидное ЗНАЧЕНИЕ (например, округление
// Math.round(total*100.0)/100.0 до ровно 0.0) и НЕ отвергается: в отличие
// от orders, здесь важно ПРИСУТСТВИЕ поля, а не то, что оно не равно нулю —
// проверки "total>0" нет и не нужно.
func parseValidTotal(raw []byte) (total, error) {
	var w totalWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return total{}, err
	}
	if w.CustomerID < 1 || w.CustomerID > 5 {
		return total{}, fmt.Errorf("семантически невалидная запись: customer_id=%d вне диапазона клиентов стенда 1..5 (см. scripts/seed.sh)", w.CustomerID)
	}
	if w.Orders == 0 {
		return total{}, fmt.Errorf("семантически невалидная запись: orders=0 у customer_id=%d (реальный агрегат customer.totals всегда orders>=1, см. TotalsProcessor.process())", w.CustomerID)
	}
	if w.Total == nil {
		return total{}, fmt.Errorf("семантически невалидная запись: total отсутствует у customer_id=%d; обязательное поле реального агрегата (см. TotalsProcessor.process())", w.CustomerID)
	}
	return total{CustomerID: w.CustomerID, Orders: w.Orders, Total: *w.Total}, nil
}

// parseValidOrderEvent — тот же принцип, что и parseValidTotal, но для
// dedup-demo режима (топик orders.events, см. main.go): order_id — bigserial
// PK PostgreSQL (sql/01-postgres.sql), начинается с 1 и уникален глобально;
// customer_id — тот же диапазон 1..5, что и в totals-режиме
// (scripts/seed.sh). order_id==0/customer_id вне диапазона недостижимы для
// настоящего события orders.events.
func parseValidOrderEvent(raw []byte) (orderEvent, error) {
	var o orderEvent
	if err := json.Unmarshal(raw, &o); err != nil {
		return orderEvent{}, err
	}
	if o.OrderID == 0 {
		return orderEvent{}, fmt.Errorf("семантически невалидная запись: order_id=0 (order_id — bigserial PK PG, начинается с 1, customer_id=%d)", o.CustomerID)
	}
	if o.CustomerID < 1 || o.CustomerID > 5 {
		return orderEvent{}, fmt.Errorf("семантически невалидная запись: customer_id=%d вне диапазона клиентов стенда 1..5 (см. scripts/seed.sh), order_id=%d", o.CustomerID, o.OrderID)
	}
	return o, nil
}

// dupFactor — сколько раз вставлять КАЖДОЕ хорошее сообщение пачки: 1 в
// обычном режиме, 2 под -dup (имитация повторной доставки, at-least-once).
// Вынесено отдельной функцией ради unit-теста (см. validate_test.go) —
// раньше это было инлайновое "n := 1; if *dup { n = 2 }" в двух местах
// main.go без единой точки проверки.
func dupFactor(dup bool) int {
	if dup {
		return 2
	}
	return 1
}
