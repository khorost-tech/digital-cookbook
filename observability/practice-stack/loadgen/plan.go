package main

import (
	"fmt"
	"math/rand"
	"net/http"
)

// Сценарий нагрузки детерминирован по seed: без этого нельзя написать точный
// критерий проверки. «Данные появились» — не критерий; критерий — «трейсов
// ровно столько, сколько запросов, и ошибочных ровно столько, сколько
// ошибочных запросов было в плане».
type Step struct {
	SKU        string
	Quantity   int
	WantStatus int // ожидаемый код ответа go-frontend
}

// Состав плана. Доли подобраны так, чтобы в стенде одновременно были:
// успешные быстрые запросы, заметно медленный путь (pg_sleep в БД), нехватка
// товара, отсутствие товара и отказ обработки. Последние два нужны tail
// sampling в ст. 2 и алертам в ст. 5.
var mix = []struct {
	sku        string
	weight     int
	quantity   int
	wantStatus int
}{
	{"SKU-0001", 40, 2, http.StatusCreated},   // обычный успех
	{"SKU-0002", 20, 1, http.StatusCreated},   // обычный успех
	{"SKU-0003", 10, 5, http.StatusCreated},   // обычный успех
	{"SKU-0004", 10, 1, http.StatusCreated},   // медленный: задержка внутри БД
	{"SKU-0005", 5, 10, http.StatusCreated},   // товара меньше, чем нужно: reserved=false
	{"SKU-0006", 5, 1, http.StatusCreated},    // нулевой остаток: reserved=false
	{"SKU-NOPE", 5, 1, http.StatusNotFound},   // товара нет вовсе
	{"SKU-BOOM", 5, 1, http.StatusBadGateway}, // бэкенд отвечает 500 -> фронт 502
}

func BuildPlan(n int, seed int64) []Step {
	if n <= 0 {
		return nil
	}
	total := 0
	for _, m := range mix {
		total += m.weight
	}

	rnd := rand.New(rand.NewSource(seed))
	plan := make([]Step, 0, n)
	for i := 0; i < n; i++ {
		roll := rnd.Intn(total)
		acc := 0
		for _, m := range mix {
			acc += m.weight
			if roll < acc {
				plan = append(plan, Step{SKU: m.sku, Quantity: m.quantity, WantStatus: m.wantStatus})
				break
			}
		}
	}
	return plan
}

// BuildErrorPlan — план из одних отказов сервера. Нужен алертам статьи 5: на
// обычном плане доля ошибок около 10%, а burn rate при цели 99% доходит до
// порога 14.4 только на заметно худшей доле.
//
// Только SKU-BOOM, то есть только 502. Смешивать сюда SKU-NOPE (404) нельзя:
// в правилах RED четырёхсотые из бюджета ошибок исключены намеренно — 404 на
// несуществующий артикул это нормальная работа системы. План из 404 не зажёг бы
// алерт, и проверка молча превратилась бы в проверку ничего.
//
// Аргумент seed не используется для выбора шагов — все шаги одинаковы, — но
// оставлен в подписи: вызывающая сторона одна и та же, и расхождение сигнатур
// с BuildPlan приводило бы к путанице в скриптах.
func BuildErrorPlan(n int, _ int64) []Step {
	if n <= 0 {
		return nil
	}
	plan := make([]Step, 0, n)
	for i := 0; i < n; i++ {
		plan = append(plan, Step{SKU: "SKU-BOOM", Quantity: 1, WantStatus: http.StatusBadGateway})
	}
	return plan
}

// BuildSuccessPlan — план из одних успешных заказов. Нужен замеру wide events
// (статья 8): там сравнивается объём СОБЫТИЯ о заказе с числом серий метрик,
// описывающих те же заказы.
//
// Почему нельзя взять обычный план. Событие «заказ создан» пишется только при
// успешном заказе: на 404 и 502 заказа нет, значит нет и записи. Считать объём
// «на запрос» по смешанной нагрузке — значит делить объём событий на число
// запросов, часть которых событий не порождает, и получать величину, не
// означающую ничего.
//
// Состав повторяет успешную часть обычного плана, включая SKU-0005 и SKU-0006
// (reserved=false): заказ создаётся и там, просто товар не резервируется.
// Медленный SKU-0004 тоже оставлен — он влияет на длительность, а не на факт
// события.
func BuildSuccessPlan(n int, seed int64) []Step {
	if n <= 0 {
		return nil
	}
	// Отбор идёт из общего mix, а не из отдельного списка: иначе при правке mix
	// два места разъехались бы молча.
	var okMix []struct {
		sku      string
		weight   int
		quantity int
	}
	total := 0
	for _, m := range mix {
		if m.wantStatus != http.StatusCreated {
			continue
		}
		okMix = append(okMix, struct {
			sku      string
			weight   int
			quantity int
		}{m.sku, m.weight, m.quantity})
		total += m.weight
	}

	rnd := rand.New(rand.NewSource(seed))
	plan := make([]Step, 0, n)
	for i := 0; i < n; i++ {
		r := rnd.Intn(total)
		acc := 0
		for _, m := range okMix {
			acc += m.weight
			if r < acc {
				plan = append(plan, Step{
					SKU:        m.sku,
					Quantity:   m.quantity,
					WantStatus: http.StatusCreated,
				})
				break
			}
		}
	}
	return plan
}

// Expectations — то, что должно получиться, посчитанное ДО прогона. Проверки
// стенда сверяются с этими числами, а не с тем, что оказалось в бэкендах.
type Expectations struct {
	Total    int            `json:"total"`
	ByStatus map[int]int    `json:"by_status"`
	BySKU    map[string]int `json:"by_sku"`
	Errors   int            `json:"errors"` // 4xx и 5xx суммарно
}

func Expect(plan []Step) Expectations {
	exp := Expectations{
		Total:    len(plan),
		ByStatus: map[int]int{},
		BySKU:    map[string]int{},
	}
	for _, s := range plan {
		exp.ByStatus[s.WantStatus]++
		exp.BySKU[s.SKU]++
		if s.WantStatus >= 400 {
			exp.Errors++
		}
	}
	return exp
}

func (e Expectations) String() string {
	return fmt.Sprintf("всего=%d, 201=%d, 404=%d, 502=%d, ошибок=%d",
		e.Total, e.ByStatus[http.StatusCreated], e.ByStatus[http.StatusNotFound],
		e.ByStatus[http.StatusBadGateway], e.Errors)
}
