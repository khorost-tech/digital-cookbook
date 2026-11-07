package main

import (
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"shortlinks/internal/cache"
	"shortlinks/internal/events"
	"shortlinks/internal/store"
)

func counter(name, help string, f func() float64) prometheus.Collector {
	return prometheus.NewCounterFunc(prometheus.CounterOpts{Name: name, Help: help}, f)
}

func cacheMetrics(r *cache.Resolver, st *store.Postgres) []prometheus.Collector {
	pool := st.Pool
	return []prometheus.Collector{
		counter("db_pool_acquire_total", "Взятия соединения из пула", func() float64 { return float64(pool.Stat().AcquireCount()) }),
		counter("db_pool_empty_acquire_total", "Взятия, которым пришлось ждать свободное соединение",
			func() float64 { return float64(pool.Stat().EmptyAcquireCount()) }),
		counter("db_pool_acquire_wait_seconds_total", "Суммарное ожидание соединения из пула",
			func() float64 { return pool.Stat().AcquireDuration().Seconds() }),
		counter("cache_hits_total", "Попадания в кэш", func() float64 { return float64(r.Hits.Load()) }),
		counter("cache_misses_total", "Промахи кэша", func() float64 { return float64(r.Misses.Load()) }),
		counter("db_link_reads_total", "Чтения ссылки из базы", func() float64 { return float64(st.Reads.Load()) }),
	}
}

func pubMetrics(p *events.Publisher, r *events.Relay) []prometheus.Collector {
	return []prometheus.Collector{
		counter("clicks_published_total", "Опубликованные клики", func() float64 { return float64(p.Published.Load()) }),
		counter("clicks_dropped_total", "Клики, отброшенные при полном буфере", func() float64 { return float64(p.Dropped.Load()) }),
		counter("clicks_publish_failed_total", "Ошибки публикации кликов", func() float64 { return float64(p.Failed.Load()) }),
		counter("clicks_late_after_close_total", "Клики, пришедшие после закрытия публикатора", func() float64 { return float64(p.Late.Load()) }),
		counter("clicks_unsent_on_shutdown_total", "Клики, не опубликованные до срока остановки", func() float64 { return float64(p.Unsent.Load()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "clicks_buffered", Help: "Клики в буфере публикации"},
			func() float64 { return float64(p.Pending()) }),
		counter("link_events_published_total", "Опубликованные события создания ссылок", func() float64 { return float64(r.Published.Load()) }),
		counter("link_events_failed_total", "Ошибки публикации событий создания", func() float64 { return float64(r.Failed.Load()) }),
	}
}

// slowRedirect — SLOW_REDIRECT: искусственная задержка редиректа для опытов с остановкой.
func slowRedirect() time.Duration {
	d, _ := time.ParseDuration(os.Getenv("SLOW_REDIRECT"))
	return d
}

// slowLoad — искусственно медленное чтение ссылки при промахе кэша (опыт со stampede): база под
// нагрузкой отвечает не за миллисекунду, и весь залп успевает прийти, пока идёт первая загрузка.
func slowLoad() time.Duration {
	d, _ := time.ParseDuration(os.Getenv("SLOW_LOAD"))
	return d
}
