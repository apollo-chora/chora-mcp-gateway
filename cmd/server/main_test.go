// main_test.go — unit tests for wireResolver, the testable extraction of
// main()'s API-key resolver selection. Exercises the three-way branch
// (real A2AClient / dev-seed MockResolver fallback / fail-loud) without
// spawning a process — main() only wraps wireResolver in log.Fatalf, which
// is why the branching logic lives in its own func per the M12 task brief.
package main

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-mcp-gateway/internal/adapter/clients"
	"github.com/apollo-chora/chora-mcp-gateway/internal/adapter/inmem"
	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/auth"
)

// clearResolverEnv guarantees both resolver env vars are empty for the
// duration of the test, regardless of what the invoking shell happens to
// export (mirrors clients_test.go's TestRegistryLookupMissingReturnsErrUpstreamNotConfigured).
func clearResolverEnv(t *testing.T) {
	t.Helper()
	t.Setenv(clients.EnvA2AGateway, "")
	t.Setenv(envDevSeedAPIKeys, "")
}

func TestWireResolver_UsesRealClientWhenGatewayConfigured(t *testing.T) {
	// not Parallel — uses t.Setenv
	clearResolverEnv(t)
	t.Setenv(clients.EnvA2AGateway, "https://chora-a2a-gateway.example")

	r, err := wireResolver()
	if err != nil {
		t.Fatalf("wireResolver: unexpected error: %v", err)
	}
	if _, ok := r.(*clients.A2AClient); !ok {
		t.Fatalf("expected *clients.A2AClient when %s is set, got %T", clients.EnvA2AGateway, r)
	}
}

func TestWireResolver_UsesMockFallbackWhenOnlyDevSeedConfigured(t *testing.T) {
	// not Parallel — uses t.Setenv
	clearResolverEnv(t)
	t.Setenv(envDevSeedAPIKeys, "k1:agid:tenant-1:mcp:read,mcp:write")

	r, err := wireResolver()
	if err != nil {
		t.Fatalf("wireResolver: unexpected error: %v", err)
	}
	// Local dev must resolve the seeded key through the real client's
	// MockResolver fallback (baseURL empty -> ResolveAPIKey delegates).
	id, err := r.ResolveAPIKey("k1")
	if err != nil {
		t.Fatalf("ResolveAPIKey(seeded key): unexpected error: %v", err)
	}
	if id.AGID != "agid" || id.TenantID != "tenant-1" || !id.Active {
		t.Errorf("seeded identity mismatch: %+v", id)
	}
	if !id.HasTool("mcp:write") {
		t.Errorf("expected seeded allowed-tools to include mcp:write, got %v", id.AllowedTools)
	}
}

func TestWireResolver_GatewayTakesPrecedenceOverDevSeed(t *testing.T) {
	// not Parallel — uses t.Setenv
	clearResolverEnv(t)
	t.Setenv(clients.EnvA2AGateway, "https://chora-a2a-gateway.example")
	t.Setenv(envDevSeedAPIKeys, "k1:agid:tenant-1:mcp:read")

	r, err := wireResolver()
	if err != nil {
		t.Fatalf("wireResolver: unexpected error: %v", err)
	}
	if _, ok := r.(*clients.A2AClient); !ok {
		t.Fatalf("expected *clients.A2AClient when both env vars set, got %T", r)
	}
}

func TestWireResolver_FailsLoudWhenNeitherConfigured(t *testing.T) {
	// not Parallel — uses t.Setenv
	clearResolverEnv(t)

	_, err := wireResolver()
	if err == nil {
		t.Fatal("expected an error when neither MCP_UPSTREAM_A2A_GATEWAY nor MCP_DEV_SEED_API_KEYS is set")
	}
	if !strings.Contains(err.Error(), clients.EnvA2AGateway) || !strings.Contains(err.Error(), envDevSeedAPIKeys) {
		t.Errorf("error should name both env vars so the operator knows how to fix it: %v", err)
	}
}

func TestWireResolver_FailsLoudWhenDevSeedMalformed(t *testing.T) {
	// not Parallel — uses t.Setenv
	clearResolverEnv(t)
	t.Setenv(envDevSeedAPIKeys, "not-enough-fields")

	_, err := wireResolver()
	if err == nil {
		t.Fatal("expected an error for a malformed MCP_DEV_SEED_API_KEYS (must not silently run with an empty resolver)")
	}
}

func TestSeedDevPartner_RegistersIdentity(t *testing.T) {
	t.Parallel()
	r := inmem.NewStaticResolver()
	if err := seedDevPartner(r, "k1:agid:tenant-9:mcp:read,mcp:write"); err != nil {
		t.Fatalf("seedDevPartner: unexpected error: %v", err)
	}
	id, err := r.ResolveAPIKey("k1")
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	if id.AGID != "agid" || id.TenantID != "tenant-9" || id.PartnerName != "dev-seed" || !id.Active {
		t.Errorf("unexpected identity: %+v", id)
	}
	// Regression: allowlist entries may themselves contain colons (e.g. a
	// legacy scope-style value such as mcp:read); an unbounded
	// strings.Split over the whole raw seed used to truncate them to "mcp"
	// at the first extra colon. SplitN keeps the trailing allowed-tool
	// list intact regardless of what its entries look like.
	if !id.HasTool("mcp:read") || !id.HasTool("mcp:write") {
		t.Errorf("expected allowed-tools [mcp:read mcp:write] to survive intact, got %v", id.AllowedTools)
	}
}

func TestSeedDevPartner_MalformedReturnsError(t *testing.T) {
	t.Parallel()
	r := inmem.NewStaticResolver()
	err := seedDevPartner(r, "too:few:fields")
	if err == nil {
		t.Fatal("expected error for malformed seed (fewer than 4 colon-separated fields)")
	}
	// Must not have registered anything under a partial parse.
	if _, resolveErr := r.ResolveAPIKey("too"); !errors.Is(resolveErr, auth.ErrInvalidKey) {
		t.Errorf("malformed seed must not register any key; ResolveAPIKey(\"too\") = %v", resolveErr)
	}
}

// TestMain_BootsAndServesUntilSignal — composition-root smoke test.
//
// main() is a write-only wiring function: env → signal ctx → OTLP init →
// resolver → router → listener, then it blocks on ctx.Done(). On this
// platform there is no reliable in-process way to deliver that shutdown
// signal (os.Interrupt is unimplemented on Windows; console-ctrl-event
// tricks need a subprocess with its own console), so the test runs main()
// in a goroutine and proves the whole wiring came up by dialing the bound
// listener — main() then faithfully blocks on ctx.Done() until process
// exit, which is harmless for a leaked goroutine in a test binary.
//
// This executes the bootstrap path (PORT/env resolution, signal ctx, OTLP
// handle creation, wireResolver dev-seed fallback, router + middleware
// construction, ListenAndServe). The graceful-shutdown tail
// (srv.Shutdown + the error-only branches) runs only under real deployment
// signalling and stays uncovered.
func TestMain_BootsAndServesUntilSignal(t *testing.T) {
	// Reserve a free port so the composition root binds something harmless
	// instead of the default :8080, then release it for main() to bind.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	clearResolverEnv(t)
	t.Setenv("PORT", strconv.Itoa(port))
	t.Setenv(envDevSeedAPIKeys, "k1:agid:tenant-1:course_catalog")

	go main()

	// Poll-dial the listener: success proves wireResolver + NewRouter +
	// ListenAndServe all returned and the server is accepting connections.
	deadline := time.Now().Add(5 * time.Second)
	addr := "127.0.0.1:" + strconv.Itoa(port)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return // main() is up and blocked on ctx.Done until process exit
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("main() never bound %s", addr)
}
