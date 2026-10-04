package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	otelpyroscope "github.com/grafana/otel-profiling-go"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
)

// Telemetry держит то, что нужно погасить при остановке. Порядок остановки
// важен: провайдеры должны успеть отправить накопленное, иначе последние спаны
// и метрики теряются — и выглядит это как «телеметрия работает через раз».
type Telemetry struct {
	shutdowns []func(context.Context) error
	Logs      *sdklog.LoggerProvider
}

func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs error
	// В обратном порядке: сначала то, что создано последним.
	for i := len(t.shutdowns) - 1; i >= 0; i-- {
		if err := t.shutdowns[i](ctx); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// SDKEnabled сообщает, инициализирован ли SDK. Штатная переменная
// OTEL_SDK_DISABLED=true оставляет приложение полностью рабочим: вызовы API
// (otel.Tracer(...).Start и прочие) продолжают работать, но получают no-op
// реализации и никуда ничего не отправляют. Это и есть разделение API и SDK,
// и здесь оно проверяется, а не пересказывается.
func SDKEnabled() bool {
	return os.Getenv("OTEL_SDK_DISABLED") != "true"
}

// UseHTTP сообщает, какой транспорт OTLP выбран. В Go SDK gRPC и HTTP — это
// РАЗНЫЕ модули экспортёров, а не флаг: переменная OTEL_EXPORTER_OTLP_PROTOCOL
// сама ничего не переключает, выбор делает код. У Java-агента наоборот —
// протокол выбирается переменной, и по умолчанию там http/protobuf.
func UseHTTP() bool {
	switch os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL") {
	case "http/protobuf", "http/json", "http":
		return true
	default:
		return false
	}
}

func InitTelemetry(ctx context.Context, serviceName, serviceVersion string) (*Telemetry, error) {
	t := &Telemetry{}

	// Пропагаторы ставятся ВСЕГДА, даже при выключенном SDK: они относятся к API
	// и стоят копейки. Без них не будет проброса контекста между сервисами.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if !SDKEnabled() {
		return t, nil
	}

	// Ресурсные атрибуты: WithFromEnv подхватывает OTEL_RESOURCE_ATTRIBUTES и
	// OTEL_SERVICE_NAME, дальше явные значения их дополняют. Без service.name
	// сигналы приходят от «unknown_service» и не связываются между собой.
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithProcessRuntimeDescription(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		// Расхождение версий semconv между SDK и ресурсом даёт именно
		// «частичную» ошибку, при которой ресурс всё же годен.
		if !errors.Is(err, resource.ErrSchemaURLConflict) {
			return nil, fmt.Errorf("ресурс: %w", err)
		}
	}

	// --- трейсы (стабильный API, v1.x) ---
	var traceExp sdktrace.SpanExporter
	if UseHTTP() {
		traceExp, err = otlptracehttp.New(ctx)
	} else {
		traceExp, err = otlptracegrpc.New(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("экспортёр трейсов: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExp, sdktrace.WithBatchTimeout(2*time.Second)),
		// Доля семплинга берётся из OTEL_TRACES_SAMPLER/ARG; по умолчанию
		// parentbased_always_on. В статье 2 сюда подставляется 0.1.
	)
	// Связка профиля со спаном. Обёртка otelpyroscope не экспортирует телеметрию
	// и не меняет содержимое спанов — она навешивает на активную горутину
	// pprof-метки со span_id, и в профиле появляется разрез «по спану». Именно
	// это делает возможным переход «из трейса в профиль» в Grafana.
	//
	// Обёртка ставится ВОКРУГ готового провайдера и до SetTracerProvider: если
	// зарегистрировать голый tp, метки не появятся, а библиотека при этом
	// «подключена» — проверять надо по наличию меток в профиле, а не по импорту.
	//
	// Включается той же переменной, что и сам профилировщик: без Pyroscope метки
	// никуда не поедут, и класть их незачем.
	if os.Getenv("PROFILING_ENABLED") == "true" {
		otel.SetTracerProvider(otelpyroscope.NewTracerProvider(tp))
	} else {
		otel.SetTracerProvider(tp)
	}
	t.shutdowns = append(t.shutdowns, tp.Shutdown)

	// --- метрики (стабильный API, v1.x) ---
	var metricExp sdkmetric.Exporter
	if UseHTTP() {
		metricExp, err = otlpmetrichttp.New(ctx)
	} else {
		metricExp, err = otlpmetricgrpc.New(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("экспортёр метрик: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(5*time.Second))),
	)
	otel.SetMeterProvider(mp)
	t.shutdowns = append(t.shutdowns, mp.Shutdown)

	// Метрики рантайма Go — то, что в терминах USE описывает «утилизацию»
	// самого процесса: горутины, GC, память.
	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		return nil, fmt.Errorf("метрики рантайма: %w", err)
	}

	// --- логи (ВНИМАНИЕ: v0.x) ---
	// Трейсы и метрики в Go SDK давно на v1.45.0, а логовая часть —
	// go.opentelemetry.io/otel/sdk/log и otlploggrpc — всё ещё v0.21.0. Три
	// сигнала выглядят единообразно в документации, но по зрелости различаются,
	// и это стоит знать до того, как закладывать логи через OTLP в прод.
	var logExp sdklog.Exporter
	if UseHTTP() {
		logExp, err = otlploghttp.New(ctx)
	} else {
		logExp, err = otlploggrpc.New(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("экспортёр логов: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)
	t.Logs = lp
	t.shutdowns = append(t.shutdowns, lp.Shutdown)

	return t, nil
}
