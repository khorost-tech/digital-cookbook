// Package obs — экспорт метрик SDK в Prometheus.
//
// ВАЖНО про источники. У Temporal два семейства метрик с похожими
// именами: метрики СЕРВИСА (их отдают роли frontend/history/matching/
// worker на своём порту 8000) и метрики SDK (их отдаёт наш собственный
// воркер вот этим кодом). Числа из них разные по смыслу, и в фикстурах
// у каждого значения проставляется источник — sdk или service.
//
// Отсюда же берутся счётчики sticky-кэша: temporal_sticky_cache_hit и
// temporal_sticky_cache_miss существуют ТОЛЬКО на стороне SDK — сервер
// про кэш воркера ничего не знает.
package obs

import (
	"log"
	"net/http"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/uber-go/tally/v4"
	tallyprom "github.com/uber-go/tally/v4/prometheus"
	sdktally "go.temporal.io/sdk/contrib/tally"
	"go.temporal.io/sdk/client"
)

// NewPrometheusHandler поднимает HTTP-эндпоинт /metrics на указанном
// адресе и возвращает обработчик метрик для client.Options.
//
// listenAddr — вида ":8077". Именно этот порт скрейпит Prometheus стенда;
// имя цели в compose/prometheus.yml должно совпадать с ИМЕНЕМ КОНТЕЙНЕРА
// воркера, иначе цель не резолвится и ряда в Prometheus просто не будет.
func NewPrometheusHandler(listenAddr string) (client.MetricsHandler, error) {
	reporter := tallyprom.NewReporter(tallyprom.Options{
		Registerer: promclient.DefaultRegisterer,
	})

	scope, _ := tally.NewRootScope(tally.ScopeOptions{
		CachedReporter:  reporter,
		Separator:       tallyprom.DefaultSeparator,
		SanitizeOptions: &sdktally.PrometheusSanitizeOptions,
	}, time.Second)
	scope = sdktally.NewPrometheusNamingScope(scope)

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		srv := &http.Server{
			Addr:              listenAddr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[metrics] %v", err)
		}
	}()
	log.Printf("[metrics] метрики SDK на %s/metrics", listenAddr)

	return sdktally.NewMetricsHandler(scope), nil
}
