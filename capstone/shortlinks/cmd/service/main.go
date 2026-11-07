// Сервис коротких ссылок: API, редирект, клики в брокер.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"shortlinks/internal/cache"
	"shortlinks/internal/config"
	"shortlinks/internal/events"
	"shortlinks/internal/httpapi"
	"shortlinks/internal/links"
	"shortlinks/internal/store"
	"shortlinks/internal/telemetry"
	"shortlinks/migrations"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := config.LoadService()
	if err != nil {
		// неверная конфигурация — выход до того, как открыт порт
		slog.Error("старт отменён", "err", err)
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		slog.Error("сервис остановлен с ошибкой", "err", err)
		os.Exit(1)
	}
}

// telemetryShare — доля срока остановки на выгрузку трейсов; config проверяет, что она в сроке помещается.
const telemetryShare = time.Second

type noClicks struct{}

func (noClicks) Click(context.Context, string, time.Time) {}

func run(cfg config.Service) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	telemetry.Propagate = os.Getenv("PROPAGATE") != "false"
	telemetry.Canonical = os.Getenv("NATS_HEADER_CARRIER") == "canonical"
	shutdownTracing, err := telemetry.Setup(ctx, "shortlinks", cfg.OtelEndpoint)
	if err != nil {
		return err
	}
	tracingDone := false
	defer func() {
		if !tracingDone { // выход по ошибке старта: выгрузка трейсов тоже не дольше секунды
			c, cancel := context.WithTimeout(context.Background(), telemetryShare)
			defer cancel()
			_ = shutdownTracing(c)
		}
	}()

	pool, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrations.Apply(ctx, pool); err != nil {
		return err
	}
	st := &store.Postgres{Pool: pool, Outbox: cfg.EventMode == "outbox"}
	reg := prometheus.NewRegistry()

	var rdb *redis.Client
	if cfg.RedisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
		defer rdb.Close()
	}
	res := &cache.Resolver{Redis: rdb, Store: st, TTL: cfg.CacheTTL, Singleflight: cfg.Singleflight, SlowLoad: slowLoad(),
		LoadTimeout: 2 * time.Second}

	var clicks links.Clicks = noClicks{}
	var pub *events.Publisher
	if cfg.NatsURL != "" {
		nc, js, err := events.Connect(ctx, cfg.NatsURL)
		if err != nil {
			return err
		}
		defer nc.Close()
		pub = events.NewPublisher(js, cfg.PublishMode, cfg.PublishBuffer)
		pub.Inject = telemetry.InjectNATS
		clicks = pub
		relay := &events.Relay{Store: st, JS: js}
		if cfg.EventMode == "outbox" {
			go relay.Run(ctx)
		} else {
			st.OnWrite = relay.PublishDirect // после коммита, отдельным шагом
		}
		reg.MustRegister(pubMetrics(pub, relay)...)
	}
	if cfg.CrashAfterInsert {
		// падение процесса после коммита ссылки, до публикации события (опыт статьи 2):
		// в режиме direct событие теряется, в режиме outbox оно уже лежит в базе
		st.OnWrite = func(l links.Link) {
			slog.Warn("падение после записи ссылки", "code", l.Code)
			os.Exit(3)
		}
	}
	reg.MustRegister(cacheMetrics(res, st)...)

	svc := &links.Service{Store: st, Resolver: res, Clicks: clicks, Now: time.Now}
	ready := &atomic.Bool{}
	api := httpapi.New(svc, cfg.BaseURL, ready, reg)
	api.SlowRed = slowRedirect()
	mux := http.NewServeMux()
	api.Routes(mux)
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	// контексты всех запросов — от base: когда срок остановки истёк, его отмена прерывает обработчики,
	// в том числе запросы, ждущие PostgreSQL, и соединения пула освобождаются
	base, abort := context.WithCancel(context.Background())
	defer abort()
	srv := &http.Server{Addr: cfg.Addr, Handler: telemetry.HTTP(mux), ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return base }}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	ready.Store(true)
	slog.Info("сервис готов", "addr", cfg.Addr, "publish", cfg.PublishMode, "events", cfg.EventMode)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	if !cfg.Graceful {
		slog.Warn("остановка без дренажа")
		os.Exit(0)
	}
	// 1. снять готовность: балансировщик перестаёт слать новые запросы;
	// 2. подождать, пока он это заметит (в Kubernetes — период опроса readiness);
	// 3. закрыть приём и дождаться незавершённых запросов;
	// 4. опубликовать клики, оставшиеся в буфере, и только потом закрыть соединения.
	// Всё — в одном сроке SHUTDOWN_TIMEOUT: что не уложилось, прерывается и считается.
	ready.Store(false)
	slog.Info("готовность снята", "delay", cfg.ShutdownDelay)
	time.Sleep(cfg.ShutdownDelay)
	// Бюджет остановки делится: HTTP и публикатор — до deadline-telemetryShare, выгрузка трейсов — последняя
	// доля. Иначе медленный Collector держал бы процесс после SHUTDOWN_TIMEOUT: Shutdown SDK ждёт выгрузку.
	deadline := time.Now().Add(cfg.ShutdownTimeout - cfg.ShutdownDelay)
	reserve := time.Duration(0)
	if cfg.OtelEndpoint != "" {
		reserve = telemetryShare
	}
	sctx, cancel := context.WithDeadline(context.Background(), deadline.Add(-reserve))
	defer cancel()
	inTime := true
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		// срок истёк, а запросы ещё в работе: отменить их контексты и закрыть соединения. Без этого
		// отложенный pool.Close() ждал бы запрос, застрявший в базе, сколько бы тот ни длился.
		inTime = false
		abort()
		_ = srv.Close()
		slog.Warn("срок остановки истёк: незавершённые запросы прерваны", "err", err)
	}
	if pub != nil {
		// тот же срок sctx: если он уже истёк, остаток буфера не публикуется, а считается
		if unsent := pub.Close(sctx); unsent > 0 {
			slog.Warn("клики не опубликованы до срока остановки", "unsent", unsent)
		}
	}
	tctx, tcancel := context.WithDeadline(context.Background(), deadline)
	defer tcancel()
	tracingDone = true
	if err := shutdownTracing(tctx); err != nil {
		slog.Warn("трейсы не выгружены до срока остановки", "err", err)
	}
	if inTime {
		slog.Info("остановлен штатно")
	} else {
		slog.Info("остановлен по сроку")
	}
	return nil // отложенные Close пула, Redis и NATS — после того, как обработчики прерваны
}
