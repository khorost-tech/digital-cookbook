package main

import (
	"net/http"
	"testing"
)

// Детерминизм — не деталь реализации, а условие, при котором проверки стенда
// вообще имеют смысл.
func TestPlanIsDeterministic(t *testing.T) {
	a := BuildPlan(500, 42)
	b := BuildPlan(500, 42)

	if len(a) != 500 || len(b) != 500 {
		t.Fatalf("длина плана = %d и %d, ожидалось 500", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("шаг %d различается: %+v против %+v", i, a[i], b[i])
		}
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	a := BuildPlan(200, 1)
	b := BuildPlan(200, 2)

	same := true
	for i := range a {
		if a[i] != b[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("планы с разными seed совпали — значит seed не работает")
	}
}

// Ожидания должны сходиться сами с собой: сумма по статусам равна общему числу.
func TestExpectationsConsistent(t *testing.T) {
	plan := BuildPlan(1000, 7)
	exp := Expect(plan)

	if exp.Total != 1000 {
		t.Errorf("total = %d", exp.Total)
	}

	sum := 0
	for _, n := range exp.ByStatus {
		sum += n
	}
	if sum != exp.Total {
		t.Errorf("сумма по статусам = %d, а total = %d", sum, exp.Total)
	}

	sum = 0
	for _, n := range exp.BySKU {
		sum += n
	}
	if sum != exp.Total {
		t.Errorf("сумма по SKU = %d, а total = %d", sum, exp.Total)
	}

	if exp.Errors != exp.ByStatus[http.StatusNotFound]+exp.ByStatus[http.StatusBadGateway] {
		t.Errorf("ошибок = %d, а по статусам %d + %d", exp.Errors,
			exp.ByStatus[http.StatusNotFound], exp.ByStatus[http.StatusBadGateway])
	}
}

// В плане обязаны быть все ветки: успех, отсутствие товара и отказ обработки.
// Если какой-то ветки нет, половина демонстраций серии остаётся без данных.
func TestPlanCoversAllBranches(t *testing.T) {
	exp := Expect(BuildPlan(400, 3))

	for _, want := range []int{http.StatusCreated, http.StatusNotFound, http.StatusBadGateway} {
		if exp.ByStatus[want] == 0 {
			t.Errorf("в плане нет запросов со ожидаемым статусом %d", want)
		}
	}
	for _, sku := range []string{"SKU-0004", "SKU-BOOM", "SKU-NOPE"} {
		if exp.BySKU[sku] == 0 {
			t.Errorf("в плане нет sku %s", sku)
		}
	}
}

// Режим «только ошибки» нужен алертам: чтобы SLO-алерт сработал, доля отказов
// должна выйти за цель, а обычный план даёт всего 10% ошибок — burn rate на нём
// до порога не доходит.
//
// Критерий здесь строгий: 5xx, а не «ошибки вообще». 404 на несуществующий
// артикул в бюджет ошибок не входит (в правилах RED он намеренно исключён),
// поэтому план из одних 404 алерт не зажёг бы, и проверка молча стала бы
// бессмысленной.
func TestErrorsOnlyPlanIsAllServerErrors(t *testing.T) {
	plan := BuildErrorPlan(300, 42)
	if len(plan) != 300 {
		t.Fatalf("длина плана = %d, ожидалось 300", len(plan))
	}

	exp := Expect(plan)
	if exp.ByStatus[http.StatusBadGateway] != 300 {
		t.Errorf("502 = %d, ожидалось ровно 300", exp.ByStatus[http.StatusBadGateway])
	}
	if exp.ByStatus[http.StatusCreated] != 0 {
		t.Errorf("в плане только ошибок оказалось %d успешных", exp.ByStatus[http.StatusCreated])
	}
	if exp.ByStatus[http.StatusNotFound] != 0 {
		t.Errorf("в плане только ошибок оказалось %d ответов 404: они не входят в бюджет ошибок",
			exp.ByStatus[http.StatusNotFound])
	}
}

func TestErrorsOnlyPlanIsDeterministic(t *testing.T) {
	a := BuildErrorPlan(50, 9)
	b := BuildErrorPlan(50, 9)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("шаг %d различается: %+v против %+v", i, a[i], b[i])
		}
	}
}

// План только из успехов нужен замеру wide events: событие «заказ создан»
// пишется лишь при успешном заказе, и смешанная нагрузка делала бы объём
// «на запрос» бессмысленным.
func TestSuccessOnlyPlanIsAllCreated(t *testing.T) {
	plan := BuildSuccessPlan(300, 42)
	if len(plan) != 300 {
		t.Fatalf("шагов %d, ожидалось 300", len(plan))
	}
	seen := map[string]int{}
	for i, s := range plan {
		if s.WantStatus != http.StatusCreated {
			t.Fatalf("шаг %d: статус %d, ожидался 201", i, s.WantStatus)
		}
		seen[s.SKU]++
	}
	// Ни одного отказного артикула быть не должно — иначе событие не создастся.
	for _, bad := range []string{"SKU-NOPE", "SKU-BOOM"} {
		if seen[bad] != 0 {
			t.Errorf("в плане успехов есть %s: %d раз", bad, seen[bad])
		}
	}
	// И при этом набор не должен схлопнуться в один артикул: заказы с
	// reserved=false тоже создаются, и они нужны замеру.
	if len(seen) < 3 {
		t.Errorf("разных артикулов %d, ожидалось не меньше 3: %v", len(seen), seen)
	}
}

func TestSuccessOnlyPlanIsDeterministic(t *testing.T) {
	a := BuildSuccessPlan(100, 7)
	b := BuildSuccessPlan(100, 7)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("шаг %d разошёлся при одном seed: %+v против %+v", i, a[i], b[i])
		}
	}
}
