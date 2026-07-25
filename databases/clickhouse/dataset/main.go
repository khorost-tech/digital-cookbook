// Command dataset — детерминированный генератор общего аналитического
// датасета "события" для серии статей "ClickHouse: глубокое погружение".
//
// Один и тот же генератор (тот же seed → байт-в-байт тот же вывод, НЕ
// зависит от даты запуска — опорное время зашито константой, а не time.Now())
// используется стендами #1 (OLAP vs PostgreSQL), #3 (бенчмарк драйверов) и
// #8 (карта выбора CH/Timescale/DuckDB/PG), чтобы сравнения были
// яблоки-к-яблокам на идентичных данных.
//
// Схема (одна и та же во всех потребителях — ../compose/compose.yml,
// ../go/, ../java/):
//
//	event_time  DateTime                 — момент события
//	user_id     UInt64                   — идентификатор пользователя
//	event_type  LowCardinality(String)   — тип события
//	url         String                   — путь на сайте
//	duration_ms UInt32                   — длительность обработки/визита, мс
//	country     LowCardinality(String)   — страна (ISO alpha-2)
//	revenue     Decimal(10,2)            — выручка (0.00 кроме checkout/purchase)
//
// Вывод — CSV с заголовком (`FORMAT CSVWithNames` для ClickHouse,
// `COPY ... WITH (FORMAT csv, HEADER)` для PostgreSQL — см. ../README.md).
//
// Запуск (из контейнера golang:1.25, сеть не нужна — чистая генерация):
//
//	docker run --rm -v "$(pwd)/dataset:/app" -w /app golang:1.25 \
//	  go run . -rows=2000000 -seed=42 -out=/app/out/events.csv
//
// Быстрая проверка (Task 1): -rows=1000000..2000000. Полный прогон стендов
// (#1/#3/#8): -rows=20000000 (по умолчанию).
package main

import (
	"bufio"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"time"
)

// referenceEnd — фиксированная опорная точка отсчёта окна событий. НЕ
// time.Now(): воспроизводимость должна быть байт-в-байт независимо от того,
// в какой день запущен генератор.
var referenceEnd = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

// windowDays — окно генерации: события равномерно (с джиттером) разбросаны
// по последним windowDays дням перед referenceEnd.
const windowDays = 90

// userPoolSize — размер пула различимых пользователей. Не зависит от -rows:
// фиксирован, чтобы при любом объёме сравнения (1М vs 20М строк) сохранялась
// одна и та же "форма" повторных визитов одного user_id (в среднем rows/userPoolSize
// событий на пользователя — растёт вместе с -rows, что реалистично: больше данных
// = больше истории на того же пользователя, а не больше разных пользователей).
const userPoolSize = 500_000

type eventTypeSpec struct {
	name       string
	weight     int // из 1000
	durationLo int
	durationHi int
	hasRevenue bool
}

// eventTypes — распределение типов событий (веса из 1000, сумма = 1000).
// page_view доминирует (типично для веб-аналитики), покупки/оформления —
// малая, но ненулевая доля (даёт непустые revenue-агрегаты в стендах).
var eventTypes = []eventTypeSpec{
	{"page_view", 550, 200, 8000, false},
	{"click", 200, 50, 500, false},
	{"search", 100, 300, 3000, false},
	{"add_to_cart", 60, 100, 1500, false},
	{"checkout", 40, 500, 5000, true},
	{"purchase", 30, 500, 6000, true},
	{"signup", 10, 1000, 9000, false},
	{"logout", 10, 50, 800, false},
}

type countrySpec struct {
	code   string
	weight int // из 1000
}

// countries — географическое распределение (веса из 1000, сумма = 1000).
// Смещено к RU (сайт русскоязычный) с длинным хвостом остальных стран —
// реалистичная асимметрия для демонстрации GROUP BY country в стендах.
var countries = []countrySpec{
	{"RU", 350},
	{"US", 150},
	{"DE", 80},
	{"GB", 60},
	{"FR", 50},
	{"IN", 50},
	{"BR", 40},
	{"CN", 40},
	{"JP", 30},
	{"KZ", 30},
	{"UA", 25},
	{"PL", 20},
	{"NL", 15},
	{"CA", 15},
	{"AU", 10},
	{"ES", 10},
	{"IT", 10},
	{"TR", 5},
	{"BY", 5},
	{"AE", 5},
}

// urlTemplates — пути сайта; "%d" подставляется случайным числовым id.
var urlTemplates = []string{
	"/",
	"/catalog",
	"/catalog/electronics",
	"/catalog/books",
	"/product/%d",
	"/cart",
	"/checkout",
	"/search",
	"/account",
	"/account/orders",
	"/blog/%d",
	"/promo/%d",
	"/about",
	"/contacts",
}

func init() {
	sumCheck("event types", func() int {
		s := 0
		for _, e := range eventTypes {
			s += e.weight
		}
		return s
	}())
	sumCheck("countries", func() int {
		s := 0
		for _, c := range countries {
			s += c.weight
		}
		return s
	}())
}

func sumCheck(what string, got int) {
	if got != 1000 {
		panic(fmt.Sprintf("dataset: %s weights must sum to 1000, got %d (fix distribution table)", what, got))
	}
}

// weightedPick — выбирает индекс по кумулятивному весу (веса из 1000).
func weightedPickEventType(r *rand.Rand) eventTypeSpec {
	roll := r.Intn(1000)
	acc := 0
	for _, e := range eventTypes {
		acc += e.weight
		if roll < acc {
			return e
		}
	}
	return eventTypes[len(eventTypes)-1]
}

func weightedPickCountry(r *rand.Rand) string {
	roll := r.Intn(1000)
	acc := 0
	for _, c := range countries {
		acc += c.weight
		if roll < acc {
			return c.code
		}
	}
	return countries[len(countries)-1].code
}

func main() {
	rows := flag.Int64("rows", 2_000_000, "число строк для генерации (полный прогон стендов — 20000000)")
	seed := flag.Int64("seed", 42, "seed ГПСЧ — фиксирует детерминированный вывод")
	out := flag.String("out", "dataset.csv", "путь к выходному CSV-файлу (\"-\" — stdout)")
	flag.Parse()

	if *rows <= 0 {
		log.Fatalf("-rows must be > 0, got %d", *rows)
	}

	var w *bufio.Writer
	if *out == "-" {
		w = bufio.NewWriterSize(os.Stdout, 1<<20)
	} else {
		f, err := os.Create(*out)
		if err != nil {
			log.Fatalf("create output file: %v", err)
		}
		defer f.Close()
		w = bufio.NewWriterSize(f, 1<<20)
	}

	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"event_time", "user_id", "event_type", "url", "duration_ms", "country", "revenue"}); err != nil {
		log.Fatalf("write header: %v", err)
	}

	r := rand.New(rand.NewSource(*seed))
	windowSeconds := int64(windowDays * 24 * 3600)
	startTime := referenceEnd.Add(-time.Duration(windowSeconds) * time.Second)

	record := make([]string, 7)
	for i := int64(0); i < *rows; i++ {
		offsetSec := r.Int63n(windowSeconds)
		ts := startTime.Add(time.Duration(offsetSec) * time.Second)

		userID := uint64(r.Intn(userPoolSize)) + 1
		et := weightedPickEventType(r)
		country := weightedPickCountry(r)

		tmpl := urlTemplates[r.Intn(len(urlTemplates))]
		var url string
		if containsPercentD(tmpl) {
			url = fmt.Sprintf(tmpl, r.Intn(100_000)+1)
		} else {
			url = tmpl
		}

		durationMs := et.durationLo + r.Intn(et.durationHi-et.durationLo+1)

		var revenue string
		if et.hasRevenue {
			// checkout: 0.00..300.00 (значение корзины на оформлении),
			// purchase: 5.00..500.00 (фактическая покупка) — центы детерминированы seed'ом.
			lo, hi := 0, 30000
			if et.name == "purchase" {
				lo, hi = 500, 50000
			}
			cents := lo + r.Intn(hi-lo+1)
			revenue = strconv.FormatFloat(float64(cents)/100.0, 'f', 2, 64)
		} else {
			revenue = "0.00"
		}

		record[0] = ts.Format("2006-01-02 15:04:05")
		record[1] = strconv.FormatUint(userID, 10)
		record[2] = et.name
		record[3] = url
		record[4] = strconv.Itoa(durationMs)
		record[5] = country
		record[6] = revenue

		if err := cw.Write(record); err != nil {
			log.Fatalf("write row %d: %v", i, err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		log.Fatalf("csv flush: %v", err)
	}
	if err := w.Flush(); err != nil {
		log.Fatalf("buffer flush: %v", err)
	}

	if *out != "-" {
		fmt.Fprintf(os.Stderr, "dataset: wrote %d rows to %s (seed=%d)\n", *rows, *out, *seed)
	}
}

func containsPercentD(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '%' && s[i+1] == 'd' {
			return true
		}
	}
	return false
}
