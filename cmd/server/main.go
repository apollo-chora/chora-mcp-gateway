// Package main is the chora-mcp-gateway HTTP entrypoint.
//
// MCP-as-a-Service: exposes Chora capabilities to external AI agents
// over JSON-RPC 2.0 (`tools/list`, `tools/call`). API keys delegate to
// the chora-a2a-gateway/partners registry. Citation chains accompany
// every successful tool result per IMDA D4 Transparency.
//
// All upstream URLs come from env vars (no inline config). See
// internal/adapter/clients/clients.go for the keys.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/apollo-chora/chora-mcp-gateway/internal/adapter/clients"
	httpadapter "github.com/apollo-chora/chora-mcp-gateway/internal/adapter/http"
	"github.com/apollo-chora/chora-mcp-gateway/internal/adapter/inmem"
	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/auth"
	"github.com/apollo-chora/chora-mcp-gateway/internal/observability"
)

const (
	defaultPort = "8080"
	version     = "0.1.0"

	// envDevSeedAPIKeys is the local-dev-only API-key seed. Format:
	// "<api_key>:<agid>:<tenant_id>:<tool1,tool2>" — the trailing field
	// is a comma-separated allowed-TOOL-NAME list (e.g.
	// "course_catalog,learning_path_query"), matched against the
	// catalogue by capability.Gate. Production deployments NEVER set
	// this — partners come from the real chora-a2a-gateway registry
	// (see wireResolver).
	envDevSeedAPIKeys = "MCP_DEV_SEED_API_KEYS"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// OTLP wiring — traces go to the endpoint in
	// OTEL_EXPORTER_OTLP_ENDPOINT. OTLP init runs in its own goroutine with
	// its own (env-tunable, default 15s) deadline + fail-soft semantics, so
	// a slow collector handshake never swallows startup. Mirrors
	// chora-a2a-gateway.
	otlpHandle := observability.InitAsync(ctx)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		res := otlpHandle.WaitContext(shutdownCtx)
		if err := res.Shutdown(shutdownCtx); err != nil {
			log.Printf("trace shutdown error: %v", err)
		}
	}()

	// Resolver — real chora-a2a-gateway client in production; local-dev
	// mock fallback only when explicitly seeded. See wireResolver: a
	// silently-empty resolver would accept zero API keys while looking
	// "up", so an unconfigured process refuses to boot.
	resolver, err := wireResolver()
	if err != nil {
		log.Fatalf("chora-mcp-gateway: %v", err)
	}

	// Outbound clients — env-var driven, no inline URLs. The dispatcher
	// fans tools/call out over this Registry (see internal/adapter/clients
	// for the MCP_UPSTREAM_* keys the live deployment must set).
	registry := clients.NewRegistry()

	router := httpadapter.NewRouter(resolver, registry)
	// HTTPMiddleware (outermost) starts the OTel server-side span;
	// MiddlewareTraceparent (kept for its log-line + header echo) runs
	// inside that span.
	handler := observability.HTTPMiddleware()(observability.MiddlewareTraceparent(router))

	addr := ":" + port
	log.Printf("service=%s version=%s listening on %s",
		observability.ServiceName, version, addr)

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down...")

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
}

// wireResolver selects the MCP API-key resolver for this process:
//
//   - MCP_UPSTREAM_A2A_GATEWAY set → the real A2AClient dials
//     chora-a2a-gateway's POST /admin/mcp/_resolve (production path).
//     Takes precedence even if a dev seed is also set.
//   - Unset, but MCP_DEV_SEED_API_KEYS set → local dev: a StaticResolver
//     seeded from the env var is wired as the A2AClient's MockResolver
//     fallback (baseURL is empty, so ResolveAPIKey delegates to it).
//   - Neither configured → error. A silently-empty resolver would accept
//     zero API keys while looking "up" — worse than refusing to boot.
//
// Extracted from main() so the branching + fail-loud contract is unit
// testable without spawning a process; main() only wraps this in
// log.Fatalf.
func wireResolver() (auth.Resolver, error) {
	client := clients.NewA2AClientFromEnv()
	gateway := strings.TrimSpace(os.Getenv(clients.EnvA2AGateway))
	seed := strings.TrimSpace(os.Getenv(envDevSeedAPIKeys))

	switch {
	case gateway != "":
		return client, nil
	case seed != "":
		mock := inmem.NewStaticResolver()
		if err := seedDevPartner(mock, seed); err != nil {
			return nil, err
		}
		client.MockResolver = mock
		return client, nil
	default:
		return nil, fmt.Errorf("resolver not configured: set %s (production) or %s (local dev only)",
			clients.EnvA2AGateway, envDevSeedAPIKeys)
	}
}

// seedDevPartner registers a single development AGID for local CLI
// testing. The env var format is
// "<api_key>:<agid>:<tenant_id>:<tool1,tool2>". The tenant_id is the
// owning tenant the partner acts within and is propagated to upstreams as
// X-Tenant-Id (without it, tenant-scoped tools fail loud). Production
// deployments NEVER set this — partners come from the chora-a2a-gateway
// registry. AGID is distinct from GCID per CLAUDE.md §1.
//
// Splits on the first 3 colons only (SplitN, not Split): the trailing
// field is a comma-separated allowed-tool-name list, and SplitN keeps it
// intact even if a legacy scope-style value (mcp:read — itself
// colon-namespaced) is seeded; an unbounded Split would over-split such a
// value and truncate it to its prefix before the colon.
//
// Returns an error on a malformed value rather than logging + skipping —
// a skipped seed left the resolver silently empty (same failure class
// wireResolver's fail-loud contract closes for the unset case).
func seedDevPartner(r *inmem.StaticResolver, raw string) error {
	parts := strings.SplitN(raw, ":", 4)
	if len(parts) < 4 {
		return fmt.Errorf("%s malformed (expected key:agid:tenant_id:tool1,tool2), got %d field(s)",
			envDevSeedAPIKeys, len(parts))
	}
	allowedTools := strings.Split(parts[3], ",")
	r.Register(parts[0], auth.Identity{
		AGID:         parts[1],
		PartnerName:  "dev-seed",
		TenantID:     parts[2],
		AllowedTools: allowedTools,
		Active:       true,
	})
	log.Printf("seeded dev partner agid=%s tenant=%s allowed_tools=%v", parts[1], parts[2], allowedTools)
	return nil
}
