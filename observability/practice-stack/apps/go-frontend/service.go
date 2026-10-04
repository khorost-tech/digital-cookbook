package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Зависимости вынесены в интерфейсы не для красоты, а потому что на них держатся
// тесты этого сервиса без поднятого Java-бэкенда и NATS. Позже, в модуле
// instrumentation, ровно в эти границы вставляется инструментирование — и видно,
// что телеметрия ложится на существующие швы, а не требует переписывать логику.
type InventoryClient interface {
	Lookup(ctx context.Context, sku string) (Item, error)
}

type Publisher interface {
	Publish(ctx context.Context, subject string, payload []byte) error
}

type Item struct {
	SKU      string `json:"sku"`
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
}

type OrderRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type OrderResponse struct {
	OrderID  string `json:"order_id"`
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
	Reserved bool   `json:"reserved"`
}

// ErrNotFound и ErrUpstream различаются, потому что различаются коды ответа:
// отсутствие товара — это 404 и нормальная работа системы, отказ бэкенда — 502 и
// повод для алерта. Смешать их значит потерять смысл метрики ошибок.
var (
	ErrNotFound = errors.New("sku not found")
	ErrUpstream = errors.New("upstream failure")
)

// loadRunKey — ключ контекста для метки прогона нагрузки.
type loadRunKey struct{}

type Service struct {
	Inventory InventoryClient
	Bus       Publisher
	Subject   string
	Log       *slog.Logger
	// atomic, а не обычный счётчик: обработчики http.Server работают в разных
	// горутинах, и обычный инкремент здесь — гонка данных. Последовательные
	// тесты её не видят, `go test -race` под параллельной нагрузкой — видит.
	counter atomic.Uint64

	// Трассер и счётчик берутся из ГЛОБАЛЬНЫХ провайдеров, а не передаются
	// снаружи. Смысл именно в этом: при неинициализированном SDK глобальные
	// провайдеры no-op, код ниже работает как обычно, а телеметрия никуда не
	// уходит. Проверяется прогоном с OTEL_SDK_DISABLED=true.
	tracer   trace.Tracer
	orders   metric.Int64Counter
	orderDur metric.Float64Histogram

	// LogWithoutContext включает намеренно неправильное логирование: запись
	// делается без контекста запроса. Приложение работает как обычно, лог
	// пишется, но trace_id в нём пуст, и связь с трейсом потеряна. Это опыт
	// статьи 3, и он должен быть воспроизводимым, а не описанным словами.
	LogWithoutContext bool

	// CardinalityBombMax включает опыт статьи 8: уникальный идентификатор
	// заказа уезжает в ЛЕЙБЛ метрики. Ноль (по умолчанию) — опыт выключен, и
	// поведение стенда ровно такое же, как в волнах 1 и 2.
	//
	// Значение задаёт ВЕРХНЮЮ ГРАНИЦУ числа различных значений лейбла. Без
	// границы серии множатся с каждым запросом до исчерпания памяти Prometheus,
	// и замер не доживает до результата.
	CardinalityBombMax int
}

// logCtx возвращает контекст для записи лога. Вся разница между «лог связан с
// трейсом» и «лог висит сам по себе» — в том, доедет ли сюда контекст запроса.
func (s *Service) logCtx(ctx context.Context) context.Context {
	if s.LogWithoutContext {
		return context.Background()
	}
	return ctx
}

// initInstruments готовит ручное инструментирование. Автоматическое (спаны и
// метрики HTTP) даёт otelhttp; здесь — то, чего никакая обёртка знать не может:
// спан на бизнес-операцию и счётчик заказов в терминах предметной области.
func (s *Service) initInstruments() error {
	s.tracer = otel.Tracer("go-frontend/order")

	counter, err := otel.Meter("go-frontend/order").Int64Counter(
		"khorost_tech.orders.created",
		metric.WithDescription("Созданные заказы"),
		metric.WithUnit("{order}"),
	)
	if err != nil {
		return err
	}
	s.orders = counter

	// Гистограмма на бизнес-операцию, а не на HTTP-запрос: RED требует именно
	// длительность операции, и она короче запроса на время разбора и ответа.
	//
	// Бакеты заданы ЯВНО, и их набор — результат замера, а не вкуса.
	//
	// Первый вариант заканчивался последовательностью ..., 0.05, 0.1, 0.25, ...
	// и на прогоне в 300 запросов дал p95 = 0,164 с против 0,160 с у HTTP-запроса,
	// внутри которого эта операция целиком и происходит. Вывод «операция дольше
	// запроса» физически невозможен, но и обратный вывод из этих чисел не следует:
	// оба значения попали в бакет (0,1; 0,25], а квантиль внутри бакета не
	// измеряется, а ИНТЕРПОЛИРУЕТСЯ по прямой. Точность оценки равна ширине
	// бакета — здесь 150 мс, то есть в тридцать раз больше наблюдаемой разницы.
	//
	// Отсюда правило: границы ставятся густо там, где лежат реальные значения, и
	// редко в хвосте. Добавлены 0.075, 0.15, 0.2 — теперь в рабочей зоне ширина
	// бакета 50 мс вместо 150. Плата известна и считается: каждая новая граница —
	// это ещё одна серия на каждую комбинацию лейблов.
	dur, err := otel.Meter("go-frontend/order").Float64Histogram(
		"khorost_tech.order.duration",
		metric.WithDescription("Длительность создания заказа"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(
			0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.15, 0.2, 0.25, 0.5, 1, 2.5,
		),
	)
	if err != nil {
		return err
	}
	s.orderDur = dur
	return nil
}

func (s *Service) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /order", s.handleOrder)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	// Параметризованный маршрут. Нужен статье 4: по нему видно, что попадает в
	// лейбл метрики — ШАБЛОН пути или конкретный URL с идентификатором. Разница
	// решающая: во втором случае каждый заказ создаёт собственную серию.
	mux.HandleFunc("GET /order/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"order_id": r.PathValue("id"),
			"status":   "unknown",
		})
	})
	return mux
}

func (s *Service) handleOrder(w http.ResponseWriter, r *http.Request) {
	var req OrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.fail(w, r, http.StatusBadRequest, "некорректный JSON", err)
		return
	}
	if req.SKU == "" || req.Quantity <= 0 {
		s.fail(w, r, http.StatusBadRequest, "нужны sku и положительное quantity", nil)
		return
	}

	// Метка прогона из заголовка попадает в спан: по ней проверка стенда
	// отличает трейсы этого прогона от трейсов предыдущих.
	ctx := r.Context()
	if run := r.Header.Get("X-Load-Run"); run != "" {
		ctx = context.WithValue(ctx, loadRunKey{}, run)
	}

	resp, err := s.CreateOrder(ctx, req)
	switch {
	case errors.Is(err, ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "товар не найден", err)
		return
	case errors.Is(err, ErrUpstream):
		s.fail(w, r, http.StatusBadGateway, "бэкенд склада недоступен", err)
		return
	case err != nil:
		s.fail(w, r, http.StatusInternalServerError, "внутренняя ошибка", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Service) CreateOrder(ctx context.Context, req OrderRequest) (OrderResponse, error) {
	// Ручной спан на бизнес-операцию. Атрибут sku — низкой кардинальности и
	// потому безопасен; order_id в атрибуты спана класть можно, а в лейбл
	// метрики нельзя — об этом ст. 8.
	attrs := []attribute.KeyValue{
		attribute.String("order.sku", req.SKU),
		attribute.Int("order.quantity", req.Quantity),
	}
	if run, ok := ctx.Value(loadRunKey{}).(string); ok {
		attrs = append(attrs, attribute.String("load.run", run))
	}
	ctx, span := s.tracer.Start(ctx, "create_order", trace.WithAttributes(attrs...))
	defer span.End()

	started := time.Now()
	// Итог операции пишется один раз, в defer: иначе ветки с ошибкой пришлось бы
	// размечать по отдельности, и какая-нибудь непременно осталась бы без записи.
	outcome := "error"
	defer func() {
		s.orderDur.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(
			attribute.String("outcome", outcome),
		))
	}()

	item, err := s.Inventory.Lookup(ctx, req.SKU)
	if err != nil {
		// Статус спана — не то же самое, что запись ошибки: первый делает трейс
		// находимым по признаку ошибки (tail sampling в ст. 2), второй
		// добавляет событие с подробностями.
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return OrderResponse{}, err
	}

	seq := s.counter.Add(1)
	order := OrderResponse{
		OrderID:  fmt.Sprintf("ord-%s-%d", req.SKU, seq),
		SKU:      item.SKU,
		Quantity: req.Quantity,
		Reserved: item.Quantity >= req.Quantity,
	}

	outcome = "ok"
	// Метка прогона попадает и в лог, а не только в атрибут спана: замер объёма
	// событий (ст. 8) отбирает записи ИМЕННО своего прогона, иначе в выборку
	// попадают записи прошлых прогонов и замер описывает не тот опыт.
	logAttrs := []any{
		"order_id", order.OrderID, "sku", order.SKU,
		"quantity", order.Quantity, "reserved", order.Reserved,
	}
	if run, ok := ctx.Value(loadRunKey{}).(string); ok && run != "" {
		logAttrs = append(logAttrs, "load_run", run)
	}
	s.Log.InfoContext(s.logCtx(ctx), "заказ создан", logAttrs...)

	span.SetAttributes(
		attribute.String("order.id", order.OrderID),
		attribute.Bool("order.reserved", order.Reserved),
	)
	// Лейблы бизнес-счётчика. По умолчанию ровно один, и он низкой
	// кардинальности: reserved принимает два значения.
	orderAttrs := []attribute.KeyValue{
		attribute.Bool("reserved", order.Reserved),
	}
	// Опыт статьи 8: уникальный идентификатор в лейбле метрики. Та самая
	// ошибка, о которой предупреждает любая документация; здесь она
	// воспроизводится по команде, чтобы её можно было ИЗМЕРИТЬ, а не пересказать.
	//
	// Обратите внимание на контраст: строкой выше тот же order.OrderID уходит в
	// АТРИБУТ СПАНА, и это нормально. В лейбле метрики он же — взрыв.
	if s.CardinalityBombMax > 0 {
		orderAttrs = append(orderAttrs,
			attribute.String("order_id", fmt.Sprintf("ord-%d", seq%uint64(s.CardinalityBombMax))))
	}
	s.orders.Add(ctx, 1, metric.WithAttributes(orderAttrs...))

	payload, err := json.Marshal(order)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return OrderResponse{}, err
	}
	if err := s.Bus.Publish(ctx, s.Subject, payload); err != nil {
		// Публикация события — не причина отказать клиенту: заказ уже создан.
		// Именно такие «тихие» ветки потом плохо видно без телеметрии, поэтому
		// в статье 2 этот путь становится отдельным спаном.
		s.Log.WarnContext(s.logCtx(ctx), "событие не опубликовано",
			"order_id", order.OrderID, "err", err)
	}
	return order, nil
}

func (s *Service) fail(w http.ResponseWriter, r *http.Request, code int, msg string, err error) {
	// ErrorContext, а не Error: без контекста запроса запись не получит trace_id,
	// и потом по ошибочному трейсу не найти её лог.
	s.Log.ErrorContext(s.logCtx(r.Context()), "запрос отклонён",
		"status", code, "msg", msg, "err", errText(err), "path", r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":  msg,
		"status": strconv.Itoa(code),
	})
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
