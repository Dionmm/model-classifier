package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type Metrics struct {
	meter      metric.Meter
	decisions  metric.Int64Counter
	jevLatency metric.Float64Histogram
	inflight   metric.Int64UpDownCounter
	logDropped metric.Int64Counter
}

type System struct {
	Tracer  trace.Tracer
	Metrics Metrics
	tp      *sdktrace.TracerProvider
	mp      *sdkmetric.MeterProvider
}

var exportFailures atomic.Uint64
var lastErrorLog atomic.Int64
var nowUnix = func() int64 { return time.Now().Unix() }
var errorWriter io.Writer = os.Stderr

func ExportFailureCount() uint64 { return exportFailures.Load() }

func handleExportError(err error) {
	exportFailures.Add(1)
	now := nowUnix()
	prev := lastErrorLog.Load()
	if now-prev >= 60 && lastErrorLog.CompareAndSwap(prev, now) {
		_, _ = fmt.Fprintf(errorWriter, "model-routerd: otel export failure: %v\n", err)
	}
}

func Setup(ctx context.Context) (*System, error) {
	otel.SetErrorHandler(otel.ErrorHandlerFunc(handleExportError))
	if os.Getenv("OTEL_SDK_DISABLED") == "true" {
		tr := otel.Tracer("github.com/Dionmm/model-classifier/model-routerd")
		m := otel.Meter("github.com/Dionmm/model-classifier/model-routerd")
		metrics, err := newMetrics(m)
		if err != nil {
			return nil, err
		}
		return &System{Tracer: tr, Metrics: metrics}, nil
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attribute.String("service.name", "model-routerd")))
	if err != nil {
		return nil, err
	}
	texp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	traceQueue := 2048
	traceBatchTimeout := 5 * time.Second
	metricInterval := 10 * time.Second
	metricTimeout := 2 * time.Second
	if os.Getenv("MODEL_ROUTER_OTEL_TEST_FAST") == "1" {
		traceQueue = 8
		traceBatchTimeout = time.Millisecond
		metricInterval = time.Millisecond
		metricTimeout = time.Millisecond
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(texp, sdktrace.WithMaxQueueSize(traceQueue), sdktrace.WithBatchTimeout(traceBatchTimeout)),
	)
	mexp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp, sdkmetric.WithInterval(metricInterval), sdkmetric.WithTimeout(metricTimeout))),
	)
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	metrics, err := newMetrics(mp.Meter("github.com/Dionmm/model-classifier/model-routerd"))
	if err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, err
	}
	if _, err := metrics.meter.Int64ObservableCounter("model_router.otel.export_failures", metric.WithInt64Callback(func(_ context.Context, obs metric.Int64Observer) error {
		obs.Observe(int64(exportFailures.Load()))
		return nil
	})); err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, err
	}
	return &System{Tracer: tp.Tracer("github.com/Dionmm/model-classifier/model-routerd"), Metrics: metrics, tp: tp, mp: mp}, nil
}

func newMetrics(m metric.Meter) (Metrics, error) {
	var out Metrics
	var err error
	out.meter = m
	if out.decisions, err = m.Int64Counter("model_router.decisions"); err != nil {
		return Metrics{}, err
	}
	if out.jevLatency, err = m.Float64Histogram("model_router.jev.latency", metric.WithUnit("ms")); err != nil {
		return Metrics{}, err
	}
	if out.inflight, err = m.Int64UpDownCounter("model_router.inflight"); err != nil {
		return Metrics{}, err
	}
	if out.logDropped, err = m.Int64Counter("model_router.decision_log.dropped"); err != nil {
		return Metrics{}, err
	}
	return out, nil
}

func (s *System) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var err error
	if s.tp != nil {
		err = errors.Join(err, s.tp.Shutdown(ctx))
	}
	if s.mp != nil {
		err = errors.Join(err, s.mp.Shutdown(ctx))
	}
	return err
}

func (m Metrics) RecordDecision(ctx context.Context, reason, harness string) {
	if m.decisions != nil {
		m.decisions.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason), attribute.String("harness", harness)))
	}
}

func (m Metrics) RecordJevLatency(ctx context.Context, ms float64) {
	if m.jevLatency != nil {
		m.jevLatency.Record(ctx, ms)
	}
}

func (m Metrics) AddInflight(ctx context.Context, delta int64) {
	if m.inflight != nil {
		m.inflight.Add(ctx, delta)
	}
}

func (m Metrics) RecordLogDropped(ctx context.Context) {
	if m.logDropped != nil {
		m.logDropped.Add(ctx, 1)
	}
}

func (m Metrics) RecordExportFailure(ctx context.Context) {
	exportFailures.Add(1)
}
