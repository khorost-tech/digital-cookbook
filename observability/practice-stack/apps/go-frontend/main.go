package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// envInt читает целое из переменной окружения. Неразбираемое значение — это
// НОЛЬ, то есть выключенный опыт, а не аварийный останов: стенд должен
// подниматься даже с опечаткой в переменной, иначе опечатка выглядит как
// поломка приложения.
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	ctx := context.Background()

	// Телеметрия инициализируется ДО всего остального: логгер должен получить
	// провайдер логов, иначе первые записи уйдут только в stdout.
	telemetry, err := InitTelemetry(ctx, envOr("OTEL_SERVICE_NAME", "go-frontend"), "1.0.0")
	if err != nil {
		// Отказ инициализации SDK — это отказ старта. Молча продолжать без
		// телеметрии значит получить «пустые дашборды» и долго искать причину.
		slog.New(slog.NewJSONHandler(os.Stdout, nil)).
			Error("телеметрия не инициализирована", "err", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(shutdownCtx); err != nil {
			slog.Default().Warn("телеметрия остановлена с ошибкой", "err", err)
		}
	}()

	// JSON-логи с самого начала: в статье 3 к этим же записям добавляется
	// trace_id, и видно, что структурность — предусловие корреляции, а не
	// украшение.
	logger := newLogger(telemetry.Logs)
	slog.SetDefault(logger)
	logger.Info("режим телеметрии", "sdk_enabled", SDKEnabled())

	// Профилирование — четвёртый сигнал, и включается он отдельно от первых трёх:
	// у него своя цена и свой бэкенд. Отказ профилировщика НЕ останавливает
	// сервис, в отличие от отказа SDK: без профилей приложение работает и
	// остаётся наблюдаемым, а вот без телеметрии — нет.
	profiler, err := startProfiling(logger, envOr("OTEL_SERVICE_NAME", "go-frontend"))
	if err != nil {
		logger.Warn("профилирование не запустилось", "err", err)
	}
	if profiler != nil {
		defer func() {
			// Stop дожидается отправки последней порции. Без него профиль
			// последних десяти секунд теряется — и это ровно те секунды, которые
			// интересны, если процесс останавливают из-за проблемы.
			if err := profiler.Stop(); err != nil {
				logger.Warn("профилировщик остановлен с ошибкой", "err", err)
			}
		}()
	}

	addr := envOr("LISTEN_ADDR", ":8080")
	backend := envOr("INVENTORY_URL", "http://java-backend:8080")
	natsURL := envOr("NATS_URL", "nats://nats:4222")
	subject := envOr("NATS_SUBJECT", "orders.created")

	var bus Publisher = noopPublisher{}
	conn, connErr := nats.Connect(natsURL,
		nats.Name("go-frontend"),
		nats.Timeout(3*time.Second),
		nats.MaxReconnects(-1),
	)
	if connErr != nil {
		logger.Warn("NATS недоступен, события публиковаться не будут", "url", natsURL, "err", connErr)
	} else {
		defer conn.Close()
		// PLAIN_NATS_PUBLISH=true публикует событие без заголовков — опыт со
		// разрывом трейса на асинхронной границе.
		propagate := os.Getenv("PLAIN_NATS_PUBLISH") != "true"
		bus = &natsPublisher{conn: conn, propagate: propagate}
		logger.Info("подключён к NATS", "url", natsURL, "subject", subject,
			"propagate_context", propagate)
	}

	// Клиент к бэкенду: инструментированный по умолчанию. Переменная
	// PLAIN_HTTP_CLIENT=true возвращает обычный http.Client — тот самый опыт из
	// ст. 2, где трейс рвётся на границе сервисов.
	inventory := newInstrumentedHTTPInventory(backend, 5*time.Second)
	if os.Getenv("PLAIN_HTTP_CLIENT") == "true" {
		inventory = newHTTPInventory(backend, 5*time.Second)
		logger.Warn("HTTP-клиент без инструментирования: контекст трейса не будет передан")
	}

	svc := &Service{
		Inventory: inventory,
		Bus:       bus,
		Subject:   subject,
		Log:       logger,
		// Опыт статьи 3: логи без контекста запроса.
		LogWithoutContext: os.Getenv("LOG_WITHOUT_CONTEXT") == "true",
		// Опыт статьи 8: взрыв кардинальности метрики. Ноль — выключено.
		CardinalityBombMax: envInt("CARDINALITY_BOMB_MAX", 0),
	}
	if svc.LogWithoutContext {
		logger.Warn("логи пишутся БЕЗ контекста запроса: trace_id будет пуст")
	}
	if err := svc.initInstruments(); err != nil {
		logger.Error("инструменты не созданы", "err", err)
		os.Exit(1)
	}

	// otelhttp на входе даёт серверный спан и метрики HTTP автоматически, и он
	// же ИЗВЛЕКАЕТ traceparent из заголовков — без этого запрос от внешнего
	// клиента всегда начинал бы новый трейс.
	handler := otelhttp.NewHandler(svc.Routes(), "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool {
			// Проверки живости не нужны ни в трейсах, ни в метриках: они
			// составляют большинство запросов и ничего не сообщают.
			return r.URL.Path != "/health"
		}),
	)

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("go-frontend слушает", "addr", addr, "backend", backend)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("сервер остановлен с ошибкой", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("остановка с ошибкой", "err", err)
	}
	logger.Info("go-frontend остановлен")
}
