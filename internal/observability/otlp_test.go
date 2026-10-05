package observability_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/apollo-chora/chora-mcp-gateway/internal/observability"
)

func TestServiceNameConstant(t *testing.T) {
	t.Parallel()
	if observability.ServiceName != "chora-mcp-gateway" {
		t.Errorf("expected service.name=chora-mcp-gateway, got %s", observability.ServiceName)
	}
}

func TestServiceVersionConstant(t *testing.T) {
	t.Parallel()
	if observability.ServiceVersion == "" {
		t.Errorf("expected a non-empty service.version, got empty")
	}
}

// TestInitAsyncFailSoftHandle asserts the service's OTLP init mirrors the
// shared bootstrap fail-soft contract: InitAsync returns a non-nil handle
// and WaitContext yields a result whose Shutdown is ALWAYS non-nil + safe
// to defer unconditionally. stdout export mode is forced so the test never
// attempts a network export. The deep timeout behaviour is covered in
// chora-common; here we only prove the wiring.
func TestInitAsyncFailSoftHandle(t *testing.T) {
	t.Setenv("OTEL_EXPORTER", "stdout")

	ctx := context.Background()
	handle := observability.InitAsync(ctx)
	if handle == nil {
		t.Fatalf("InitAsync returned nil handle")
	}

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res := handle.WaitContext(waitCtx)
	if res.Shutdown == nil {
		t.Fatalf("OTLPResult.Shutdown must be non-nil (fail-soft contract)")
	}
	if err := res.Shutdown(waitCtx); err != nil {
		t.Errorf("Shutdown returned error: %v", err)
	}
}

func TestMiddlewareEchoesTraceparent(t *testing.T) {
	t.Parallel()
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	mw := observability.MiddlewareTraceparent(next)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if !called {
		t.Errorf("next handler was not invoked")
	}
	if rec.Header().Get("X-Echoed-Traceparent") == "" {
		t.Errorf("expected echoed traceparent header")
	}
}

func TestMiddlewareNoTraceparent(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	mw := observability.MiddlewareTraceparent(next)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Header().Get("X-Echoed-Traceparent") != "" {
		t.Errorf("did not expect echo header without traceparent")
	}
}

// HTTPMiddleware starts an OTel server-side span per inbound request so
// /mcp/tools/* + /health + /readyz actually reach the configured OTLP
// collector — previously
// only the legacy MiddlewareTraceparent (header echo + log line, no span)
// wrapped the router. Mirrors the identical regression guard in
// chora-wbl/internal/observability (TestHTTPMiddleware_EmitsSpanForWBLRequest).
func TestHTTPMiddleware_EmitsServerSpan(t *testing.T) {
	// not Parallel — mutates the global OTel TracerProvider.
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	mw := observability.HTTPMiddleware()
	if mw == nil {
		t.Fatal("HTTPMiddleware() returned nil")
	}

	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/list", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if !called {
		t.Fatal("inner handler not invoked")
	}
	spans := rec.Ended()
	if len(spans) == 0 {
		t.Fatal("expected a server-side span; HTTPMiddleware likely degraded to no-op")
	}
	if got := spans[0].Name(); got != "POST /mcp/tools/list" {
		t.Errorf("span name=%q want \"POST /mcp/tools/list\"", got)
	}
}

// HTTPMiddleware must compose with the existing MiddlewareTraceparent
// (kept per the task brief) rather than replace it — both run.
func TestHTTPMiddleware_ComposesWithMiddlewareTraceparent(t *testing.T) {
	// not Parallel — mutates the global OTel TracerProvider.
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := observability.HTTPMiddleware()(observability.MiddlewareTraceparent(inner))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)

	if rec2.Header().Get("X-Echoed-Traceparent") == "" {
		t.Errorf("MiddlewareTraceparent's echo header must still fire when wrapped by HTTPMiddleware")
	}
	if len(rec.Ended()) == 0 {
		t.Errorf("expected a server-side span even when composed with MiddlewareTraceparent")
	}
}
