// Package clients_test — a2a_client_test.go: tests for the A2AClient
// adapter that resolves MCP API keys against chora-a2a-gateway.
package clients_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-mcp-gateway/internal/adapter/clients"
	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/auth"
)

// stubResolver is a minimal Resolver for the MockResolver fallback.
type stubResolver struct {
	id  auth.Identity
	err error
}

func (s *stubResolver) ResolveAPIKey(_ string) (auth.Identity, error) {
	return s.id, s.err
}

func TestA2AClient_RejectsEmptyKey(t *testing.T) {
	t.Parallel()
	c := &clients.A2AClient{}
	if _, err := c.ResolveAPIKey(""); !errors.Is(err, auth.ErrMissingKey) {
		t.Errorf("expected ErrMissingKey; got %v", err)
	}
}

func TestA2AClient_NotConfiguredAndNoMock(t *testing.T) {
	t.Parallel()
	c := &clients.A2AClient{}
	if _, err := c.ResolveAPIKey("k"); err == nil {
		t.Error("expected error")
	}
}

func TestA2AClient_DelegatesToMockResolver(t *testing.T) {
	t.Parallel()
	want := auth.Identity{
		AGID:         "agid:abc",
		PartnerName:  "Acme",
		AllowedTools: []string{"course_catalog"},
		Active:       true,
	}
	c := &clients.A2AClient{MockResolver: &stubResolver{id: want}}
	got, err := c.ResolveAPIKey("test-key")
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	if got.AGID != want.AGID || got.PartnerName != want.PartnerName || got.Active != want.Active || len(got.AllowedTools) != len(want.AllowedTools) {
		t.Errorf("identity mismatch: got=%+v want=%+v", got, want)
	}
}

func TestA2AClient_HTTPHappyPath(t *testing.T) {
	// not Parallel — uses t.Setenv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/mcp/_resolve" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "k1" {
			t.Errorf("api key = %q", r.Header.Get("X-API-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"agid":"agid:test","partner_name":"P","allowed_scopes":["mcp:read"],"active":true}`))
	}))
	defer srv.Close()

	t.Setenv("MCP_UPSTREAM_A2A_GATEWAY", srv.URL)
	c := clients.NewA2AClientFromEnv()
	id, err := c.ResolveAPIKey("k1")
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	if id.AGID != "agid:test" {
		t.Errorf("AGID = %q", id.AGID)
	}
	if !id.Active {
		t.Error("Active = false")
	}
}

func TestA2AClient_HTTPHappyPath_MapsTenantID(t *testing.T) {
	// not Parallel — uses t.Setenv
	const wantTenant = "01900000-0000-7000-8000-0000000000aa"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"agid":"agid:test","partner_name":"P","tenant_id":"` + wantTenant +
			`","allowed_scopes":["mcp:read"],"active":true}`))
	}))
	defer srv.Close()

	t.Setenv("MCP_UPSTREAM_A2A_GATEWAY", srv.URL)
	c := clients.NewA2AClientFromEnv()
	id, err := c.ResolveAPIKey("k1")
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	// tenant_id is the owning tenant of the per-tenant MCP add-on key — the
	// dispatcher propagates it to upstream domain services as X-Tenant-Id
	// (see dispatch.go) and fails loud when it is empty, so a resolver that
	// drops it silently breaks every tenant-scoped tool.
	if id.TenantID != wantTenant {
		t.Errorf("TenantID = %q, want %q (resolver response tenant_id was dropped)", id.TenantID, wantTenant)
	}
}

func TestA2AClient_HTTPUnauthorized(t *testing.T) {
	// not Parallel — uses t.Setenv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("MCP_UPSTREAM_A2A_GATEWAY", srv.URL)
	c := clients.NewA2AClientFromEnv()
	if _, err := c.ResolveAPIKey("bad"); !errors.Is(err, auth.ErrInvalidKey) {
		t.Errorf("expected ErrInvalidKey; got %v", err)
	}
}

func TestA2AClient_HTTPForbidden_PartnerInactive(t *testing.T) {
	// not Parallel — uses t.Setenv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	t.Setenv("MCP_UPSTREAM_A2A_GATEWAY", srv.URL)
	c := clients.NewA2AClientFromEnv()
	if _, err := c.ResolveAPIKey("k"); !errors.Is(err, auth.ErrPartnerInactive) {
		t.Errorf("expected ErrPartnerInactive; got %v", err)
	}
}

func TestA2AClient_TransportError(t *testing.T) {
	// not Parallel — uses t.Setenv
	t.Setenv("MCP_UPSTREAM_A2A_GATEWAY", "http://127.0.0.1:1") // refused
	c := clients.NewA2AClientFromEnv()
	_, err := c.ResolveAPIKey("k")
	if err == nil {
		t.Error("expected transport error")
	}
	// Should not be one of the sentinel errors.
	if errors.Is(err, auth.ErrInvalidKey) || errors.Is(err, auth.ErrPartnerInactive) || errors.Is(err, auth.ErrMissingKey) {
		t.Errorf("transport error mapped to sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "a2a-client") {
		t.Errorf("error message missing tag: %v", err)
	}
}

func TestA2AClient_HTTPUnexpectedStatus(t *testing.T) {
	// not Parallel — uses t.Setenv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("MCP_UPSTREAM_A2A_GATEWAY", srv.URL)
	c := clients.NewA2AClientFromEnv()
	_, err := c.ResolveAPIKey("k")
	if err == nil {
		t.Fatal("expected error for an unexpected (5xx) upstream status")
	}
	// 5xx is a gateway/registry outage, NOT an invalid-key signal — it must
	// not be mapped onto any sentinel.
	if errors.Is(err, auth.ErrInvalidKey) || errors.Is(err, auth.ErrPartnerInactive) {
		t.Errorf("5xx mapped to auth sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error must surface the upstream status, got %v", err)
	}
}

func TestA2AClient_HTTPDecodeError(t *testing.T) {
	// not Parallel — uses t.Setenv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not a valid resolver response`))
	}))
	defer srv.Close()
	t.Setenv("MCP_UPSTREAM_A2A_GATEWAY", srv.URL)
	c := clients.NewA2AClientFromEnv()
	_, err := c.ResolveAPIKey("k")
	if err == nil {
		t.Fatal("expected error for an undecodable 200 body")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error must mention the decode failure, got %v", err)
	}
}

func TestA2AClient_HTTPBadBaseURL(t *testing.T) {
	// not Parallel — uses t.Setenv
	// A base URL with an invalid host character fails url.Parse inside
	// ResolveAPIKey and must surface as a wrapped error, not a panic.
	t.Setenv("MCP_UPSTREAM_A2A_GATEWAY", "http://exa mple")
	c := clients.NewA2AClientFromEnv()
	_, err := c.ResolveAPIKey("k")
	if err == nil {
		t.Fatal("expected error for an unparseable base URL")
	}
	if !strings.Contains(err.Error(), "a2a-client") {
		t.Errorf("error message missing tag: %v", err)
	}
}
