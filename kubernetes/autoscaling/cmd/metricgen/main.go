// Command metricgen — учебный HTTP-сервис для стенда autoscaling.
//
// Отдаёт одну управляемую бизнес-метрику demo_queue_depth в формате экспозиции
// Prometheus (простой текстовый протокол — без клиентской библиотеки, только stdlib).
// На эту метрику в следующих задачах серии навешиваются HPA (через Prometheus
// Adapter) и KEDA ScaledObject: значение метрики двигают руками через /push,
// а /work умеет реально грузить CPU, если понадобится демо на ресурсных метриках.
//
// Никаких внешних зависимостей: net/http, fmt, os, strconv, sync/atomic, log —
// собирается и запускается без сети к модульному прокси.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
)

// queueDepth — искусственная глубина очереди. Читается/пишется атомарно,
// т.к. HTTP-хендлеры выполняются в разных горутинах одновременно.
var queueDepth int64

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", handleMetrics)
	mux.HandleFunc("/push", handlePush)
	mux.HandleFunc("/work", handleWork)
	mux.HandleFunc("/healthz", handleHealthz)

	log.Printf("metricgen: слушаю %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("metricgen: сервер упал: %v", err)
	}
}

// handleMetrics отдаёт текущее значение demo_queue_depth в формате экспозиции
// Prometheus. Формат — обычный текст, клиентская библиотека prometheus/client_golang
// не нужна: HELP/TYPE — комментарии, дальше имя метрики и значение через пробел.
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	depth := atomic.LoadInt64(&queueDepth)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP demo_queue_depth Искусственная глубина очереди для демонстрации автоскейлинга\n")
	fmt.Fprintf(w, "# TYPE demo_queue_depth gauge\n")
	fmt.Fprintf(w, "demo_queue_depth %d\n", depth)
}

// handlePush выставляет глубину очереди в значение из query-параметра n.
// Используется читателем/скриптами стенда, чтобы вручную двигать метрику и
// смотреть, как на это реагирует HPA/KEDA.
func handlePush(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("n")
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("bad n=%q: %v", raw, err), http.StatusBadRequest)
		return
	}
	atomic.StoreInt64(&queueDepth, n)
	fmt.Fprintf(w, "queue_depth=%d\n", n)
}

// handleWork жжёт CPU детерминированным busy-loop (без сна, без syscall) —
// для демонстрации автоскейлинга по ресурсным метрикам (не только по
// кастомной demo_queue_depth). Результат выводится в ответ, чтобы компилятор
// не оптимизировал цикл в пустоту.
func handleWork(w http.ResponseWriter, r *http.Request) {
	var acc uint64 = 1
	const iterations = 200_000_000
	for i := uint64(1); i <= iterations; i++ {
		acc = acc*i + i
	}
	fmt.Fprintf(w, "work_done iterations=%d result=%d\n", iterations, acc)
}

// handleHealthz — простой liveness/readiness-эндпоинт.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}
