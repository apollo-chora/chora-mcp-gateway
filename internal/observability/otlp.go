// Package observability wires OTLP tracing for chora-mcp-gateway.
//
// Every service emits OTLP traces using GenAI semantic conventions. The
// legacy MiddlewareTraceparent (below) reads incoming W3C `traceparent`
// headers, logs them, and echoes them on outbound responses so log
// correlation works even before the SDK init lands.
//
// InitAsync is the real SDK wiring. It delegates to
// commonobs.InitOTLPAsync so the OTel SDK installs the OTLP exporter
// (endpoint from OTEL_EXPORTER_OTLP_ENDPOINT) in its own goroutine with
// its own deadline (CHORA_OTLP_INIT_TIMEOUT_SECONDS, default 15s) and
// fail-soft semantics. The rest of bootstrap gets the FULL bootstrap
// budget. Spans reach the configured collector over OTLP/gRPC, mirroring
// chora-a2a-gateway.
package observability

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-common/bootstrap"
	commonobs "github.com/apollo-chora/chora-common/observability"
)

const (
	headerTraceparent = "traceparent"
	headerTracestate  = "tracestate"
)

// MiddlewareTraceparent reads + echoes the W3C traceparent header.
func MiddlewareTraceparent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tp := strings.TrimSpace(r.Header.Get(headerTraceparent))
		if tp != "" {
			w.Header().Set("X-Echoed-Traceparent", tp)
			ts := strings.TrimSpace(r.Header.Get(headerTracestate))
			log.Printf("trace=incoming traceparent=%s tracestate=%s method=%s path=%s",
				tp, ts, r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}

// HTTPMiddleware returns net/http middleware that starts an OTel
// server-side span per inbound request, extracts inbound W3C TraceContext
// via the OTel propagator, and stamps the outgoing traceparent onto the
// response. Delegates to the shared chora-go-common tracing.Middleware —
// the same middleware chora-gateway / chora-identity / chora-tenancy /
// chora-wbl use — so /mcp/tools/* + /health + /readyz actually reach the
// configured OTLP collector instead of only the local traceparent-echo
// logging above (MiddlewareTraceparent).
//
// Mount as the OUTERMOST middleware in main.go (wrapping
// MiddlewareTraceparent, not wrapped by it), so the legacy traceparent log
// line happens inside the request span.
func HTTPMiddleware() func(next http.Handler) http.Handler {
	return commonobs.HTTPMiddleware()
}

// ServiceName is the OTel service.name attribute.
const ServiceName = "chora-mcp-gateway"

// ServiceVersion follows semver per OpenInference convention. Kept in sync
// with the version const in cmd/server/main.go.
const ServiceVersion = "0.1.0"

// InitAsync is the fail-soft non-blocking OTLP init. Delegates to
// commonobs.InitOTLPAsync so OTLP init runs in its own goroutine with its
// own deadline (CHORA_OTLP_INIT_TIMEOUT_SECONDS, default 15s). Timeout /
// init-error degrade to a no-op shutdown so the rest of bootstrap gets the
// FULL bootstrap budget. Mirrors chora-a2a-gateway.
func InitAsync(ctx context.Context) *bootstrap.OTLPHandle {
	return commonobs.InitOTLPAsync(ctx, ServiceName, ServiceVersion)
}
