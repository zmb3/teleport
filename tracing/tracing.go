package tracing

import (
	"context"
	"io"
	"net/url"
	"os"
	"path"

	"github.com/gravitational/teleport"
	client2 "github.com/gravitational/teleport/lib/client"
	"github.com/gravitational/trace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	oteltrace "go.opentelemetry.io/otel/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Config struct {
	Service     string
	AgentAddr   string
	Directory   string
	Attributes  []attribute.KeyValue
	SampleRatio float64
	ProxyClient *client2.ProxyClient
}

func TracesClient(ctx context.Context) (coltracepb.TraceServiceClient, error) {
	agentAddr := os.Getenv("TELEPORT_TRACING_ADDR")
	addr, err := url.Parse(agentAddr)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	switch addr.Scheme {
	case "http":
		return nil, trace.NotImplemented("http tracing is not supported yet")
	case "https":
		return nil, trace.NotImplemented("https tracing is not supported yet")
	case "grpc":
		conn, err := grpc.DialContext(
			ctx,
			agentAddr[len("grpc://"):],
			grpc.WithBlock(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, err
		}

		return coltracepb.NewTraceServiceClient(conn), nil
	case "":
		conn, err := grpc.DialContext(
			ctx,
			agentAddr,
			grpc.WithBlock(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, err
		}

		return coltracepb.NewTraceServiceClient(conn), nil
	default:
		return nil, trace.BadParameter("unsupported exporter scheme: %q", addr.Scheme)
	}
}

var _ sdktrace.SpanExporter = (*SpanExporter)(nil)

type SpanExporter struct {
	exporter sdktrace.SpanExporter
	closer   io.Closer
}

func (e SpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	return trace.Wrap(e.exporter.ExportSpans(ctx, spans))
}

func (e SpanExporter) Shutdown(ctx context.Context) error {
	return trace.NewAggregate(e.exporter.Shutdown(ctx), e.closer.Close())
}

func NewExporter(ctx context.Context, cfg Config) (*SpanExporter, error) {
	switch {
	case cfg.ProxyClient != nil:
		conn, err := cfg.ProxyClient.AuthConn(ctx)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		traceClient := otlptracegrpc.NewClient(
			otlptracegrpc.WithGRPCConn(conn),
			otlptracegrpc.WithDialOption(grpc.WithBlock()),
		)
		exporter, err := otlptrace.New(ctx, traceClient)
		if err != nil {
			return nil, trace.NewAggregate(err, conn.Close())
		}

		return &SpanExporter{
			exporter: exporter,
			closer:   conn,
		}, nil

	case cfg.AgentAddr != "":
		addr, err := url.Parse(cfg.AgentAddr)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		var traceClient otlptrace.Client
		switch addr.Scheme {
		case "http":
		case "https":
		case "grpc", "":
			addr := cfg.AgentAddr[len("grpc://"):]
			traceClient = otlptracegrpc.NewClient(
				otlptracegrpc.WithInsecure(),
				otlptracegrpc.WithEndpoint(addr),
				otlptracegrpc.WithDialOption(grpc.WithBlock()),
			)
		default:
			return nil, trace.BadParameter("unsupported exporter scheme: %q", addr.Scheme)
		}

		exporter, err := otlptrace.New(ctx, traceClient)
		if err != nil {
			return nil, trace.NewAggregate(err, traceClient.Stop(context.Background()))
		}

		return &SpanExporter{
			exporter: exporter,
			closer:   io.NopCloser(nil),
		}, nil
	case cfg.Directory != "":
		f, err := os.OpenFile(path.Join(cfg.Directory, "tracing"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		exporter, err := stdouttrace.New(stdouttrace.WithWriter(f))
		if err != nil {
			return nil, trace.Wrap(err, f.Close())
		}

		return &SpanExporter{
			exporter: exporter,
			closer:   f,
		}, nil
	default:
		return nil, trace.BadParameter("invalid tracing configuration")
	}
}

var _ oteltrace.TracerProvider = (*Provider)(nil)

type Provider struct {
	provider *sdktrace.TracerProvider
}

func (p *Provider) Tracer(instrumentationName string, opts ...oteltrace.TracerOption) oteltrace.Tracer {
	opts = append(opts, oteltrace.WithInstrumentationVersion(teleport.Version))

	return p.provider.Tracer(instrumentationName, opts...)
}

func (p *Provider) Shutdown(ctx context.Context) error {
	return trace.Wrap(p.provider.ForceFlush(ctx), p.provider.Shutdown(ctx))
}

// NewTraceProvider creates and configures the corresponding trace provider.
func NewTraceProvider(ctx context.Context, cfg Config) (*Provider, error) {
	exporter, err := NewExporter(ctx, cfg)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	attrs := []attribute.KeyValue{
		// the service name used to display traces in backends
		semconv.ServiceNameKey.String(cfg.Service),
		attribute.String("teleport.version", teleport.Version),
	}
	attrs = append(attrs, cfg.Attributes...)

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(attrs...),
	)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	processor := sdktrace.NewBatchSpanProcessor(exporter)
	otelProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRatio)),
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(processor),
	)

	// set global propagator the default is no-op.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	// set global provider to our provider to have all tracers use the common TracerOptions
	provider := &Provider{provider: otelProvider}
	otel.SetTracerProvider(provider)

	return provider, nil
}
