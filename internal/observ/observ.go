package observ

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"net/http"
)

// Options configures logging, Sentry-compatible error reporting, and OTEL.
type Options struct {
	ServiceName   string
	LogFile       string
	LogLevel      string
	SentryDSN     string
	SentryEnv     string
	PrivacyMode   bool
	OTLPEndpoint  string
	OTLPInsecure  bool
	EnableMetrics bool
}

// Runtime holds observability handles.
type Runtime struct {
	Registry   *prometheus.Registry
	Requests   *prometheus.CounterVec
	Latency    *prometheus.HistogramVec
	InFlight   prometheus.Gauge
	Shutdowns  []func(context.Context) error
	logCloser  io.Closer
	sentryHub  bool
}

// Setup configures slog, optional Sentry/GlitchTip/BugSinks, Prometheus, and OTLP.
func Setup(opts Options) (*Runtime, error) {
	level := slog.LevelInfo
	switch strings.ToLower(opts.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	var writers []io.Writer
	writers = append(writers, os.Stdout)
	rt := &Runtime{}
	if opts.LogFile != "" {
		f, err := os.OpenFile(opts.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			return nil, fmt.Errorf("log file: %w", err)
		}
		rt.logCloser = f
		writers = append(writers, f)
	}
	handler := slog.NewJSONHandler(io.MultiWriter(writers...), &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if !opts.PrivacyMode {
				return a
			}
			switch a.Key {
			case "ip", "remote_ip", "email", "authorization", "token", "api_key":
				return slog.String(a.Key, "[redacted]")
			case "ua", "user_agent":
				s := a.Value.String()
				if len(s) > 40 {
					s = s[:40]
				}
				return slog.String(a.Key, s)
			}
			return a
		},
	})
	slog.SetDefault(slog.New(handler))

	if opts.SentryDSN != "" {
		err := sentry.Init(sentry.ClientOptions{
			Dsn:              opts.SentryDSN,
			Environment:      opts.SentryEnv,
			Release:          opts.ServiceName,
			SendDefaultPII:   false,
			AttachStacktrace: true,
			BeforeSend: func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
				if opts.PrivacyMode && event != nil {
					event.User = sentry.User{}
					event.Request = nil
					event.Contexts = nil
					event.Tags = map[string]string{}
				}
				return event
			},
		})
		if err != nil {
			return nil, fmt.Errorf("sentry: %w", err)
		}
		rt.sentryHub = true
		rt.Shutdowns = append(rt.Shutdowns, func(ctx context.Context) error {
			sentry.Flush(2 * time.Second)
			return nil
		})
		slog.Info("error reporting enabled", "backend", "sentry-compatible")
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	rt.Registry = reg
	rt.Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rss_discovery_http_requests_total",
		Help: "HTTP requests",
	}, []string{"method", "path", "status", "level"})
	rt.Latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "rss_discovery_http_request_duration_seconds",
		Help:    "HTTP latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
	rt.InFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rss_discovery_http_in_flight",
		Help: "In-flight HTTP requests",
	})
	reg.MustRegister(rt.Requests, rt.Latency, rt.InFlight)

	if opts.OTLPEndpoint != "" {
		res, err := resource.Merge(
			resource.Default(),
			resource.NewWithAttributes(
				semconv.SchemaURL,
				semconv.ServiceName(opts.ServiceName),
			),
		)
		if err != nil {
			return nil, err
		}
		ctx := context.Background()
		texp, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpointURL(normalizeOTLP(opts.OTLPEndpoint)),
			otlptracehttp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("otlp trace: %w", err)
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(texp),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagation.TraceContext{})
		rt.Shutdowns = append(rt.Shutdowns, tp.Shutdown)

		mexp, err := otlpmetrichttp.New(ctx,
			otlpmetrichttp.WithEndpointURL(normalizeOTLP(opts.OTLPEndpoint)),
			otlpmetrichttp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("otlp metric: %w", err)
		}
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp)),
			sdkmetric.WithResource(res),
		)
		otel.SetMeterProvider(mp)
		rt.Shutdowns = append(rt.Shutdowns, mp.Shutdown)
		slog.Info("otel enabled", "endpoint", opts.OTLPEndpoint)
	}

	return rt, nil
}

func normalizeOTLP(ep string) string {
	if strings.HasPrefix(ep, "http://") || strings.HasPrefix(ep, "https://") {
		return ep
	}
	return "http://" + ep
}

func (rt *Runtime) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(rt.Registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

func (rt *Runtime) Close(ctx context.Context) {
	for i := len(rt.Shutdowns) - 1; i >= 0; i-- {
		_ = rt.Shutdowns[i](ctx)
	}
	if rt.logCloser != nil {
		_ = rt.logCloser.Close()
	}
}

// CaptureException sends an error to Sentry-compatible backends when configured.
func (rt *Runtime) CaptureException(err error) {
	if rt == nil || !rt.sentryHub || err == nil {
		return
	}
	sentry.CaptureException(err)
}
