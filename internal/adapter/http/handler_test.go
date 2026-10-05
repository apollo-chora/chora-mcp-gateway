package httpadapter_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-mcp-gateway/internal/adapter/clients"
	httpadapter "github.com/apollo-chora/chora-mcp-gateway/internal/adapter/http"
	"github.com/apollo-chora/chora-mcp-gateway/internal/adapter/inmem"
	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/auth"
)

const testTenantID = "01900000-0000-7000-8000-0000000000bb"

func newRouter(t *testing.T) *httpadapter.Router {
	t.Helper()
	res := inmem.NewStaticResolver()
	// k-read's allowlist names exactly one tool — proves tools/list +
	// tools/call gate on tool-NAME membership, not a scope tier.
	res.Register("k-read", auth.Identity{
		AGID:         "01900000-0000-7000-8000-000000000001",
		PartnerName:  "Course-Catalog Co",
		TenantID:     testTenantID,
		AllowedTools: []string{"course_catalog"},
		Active:       true,
	})
	res.Register("k-rw", auth.Identity{
		AGID:        "01900000-0000-7000-8000-000000000002",
		PartnerName: "All-Tools Co",
		TenantID:    testTenantID,
		AllowedTools: []string{
			"atom_search", "course_catalog", "familiar_nudge",
			"governance_check", "learning_path_query",
		},
		Active: true,
	})
	res.Register("k-suspended", auth.Identity{
		AGID:         "01900000-0000-7000-8000-000000000003",
		PartnerName:  "Suspended Co",
		TenantID:     testTenantID,
		AllowedTools: []string{"course_catalog"},
		Active:       false,
	})
	return httpadapter.NewRouter(res, fakeUpstreams(t))
}

// fakeUpstreams stands up canned delivery + consumption upstreams so the
// dispatcher's wired tools succeed end-to-end through the router.
func fakeUpstreams(t *testing.T) *clients.Registry {
	t.Helper()
	delivery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"course-1","title":"Topology 101"}],"total":1}`))
	}))
	consumption := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"path_id":"lp-1","name":"Algebra","owner_gcid":"gcid-a","completed":false}]}`))
	}))
	t.Cleanup(func() {
		delivery.Close()
		consumption.Close()
	})
	return clients.NewRegistryFromMap(map[clients.EnvKey]string{
		clients.EnvDelivery:    delivery.URL,
		clients.EnvConsumption: consumption.URL,
	})
}

func doJSON(t *testing.T, r *httpadapter.Router, method, path, key string, body any) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Result(), rec.Body.Bytes()
}

func TestHealthEndpoint(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	resp, body := doJSON(t, r, http.MethodGet, "/health", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["service"] != "chora-mcp-gateway" {
		t.Errorf("unexpected service: %s", got["service"])
	}
}

func TestReadyzEndpoint(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	resp, _ := doJSON(t, r, http.MethodGet, "/readyz", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestToolsListMissingApiKey(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/list", "", body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d body=%s", resp.StatusCode, raw)
	}
}

func TestToolsListInvalidApiKey(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
	resp, _ := doJSON(t, r, http.MethodPost, "/mcp/tools/list", "bogus", body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestToolsListSuspendedPartner(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
	resp, _ := doJSON(t, r, http.MethodPost, "/mcp/tools/list", "k-suspended", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestToolsListReturnsOnlyAllowedToolNames(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/list"}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/list", "k-read", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, raw)
	}
	var rpc struct {
		Jsonrpc string         `json:"jsonrpc"`
		ID      int            `json:"id"`
		Result  map[string]any `json:"result"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rpc.Jsonrpc != "2.0" || rpc.ID != 7 {
		t.Errorf("envelope mismatch: %+v", rpc)
	}
	tools, _ := rpc.Result["tools"].([]any)
	// k-read's allowlist names exactly one tool (course_catalog); tools/list
	// must return the catalogue ∩ allowlist intersection — that one tool,
	// not a scope-tier filtered set.
	if len(tools) != 1 {
		t.Fatalf("expected exactly 1 tool for allowlist=[course_catalog], got %d (%v)", len(tools), tools)
	}
	if tools[0].(map[string]any)["name"] != "course_catalog" {
		t.Errorf("expected course_catalog, got %v", tools[0].(map[string]any)["name"])
	}
}

func TestToolsListReturnsAllWhenAllToolNamesAllowed(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/list", "k-rw", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, raw)
	}
	var rpc struct {
		Result map[string]any `json:"result"`
	}
	_ = json.Unmarshal(raw, &rpc)
	tools, _ := rpc.Result["tools"].([]any)
	// 5 tools after model_broker_invoke removal (ADR-146).
	if len(tools) != 5 {
		t.Errorf("expected 5 tools when all tool names are allowlisted, got %d", len(tools))
	}
	for _, ti := range tools {
		if ti.(map[string]any)["name"] == "model_broker_invoke" {
			t.Errorf("model_broker_invoke must not be listed (ADR-146 retired)")
		}
	}
}

// Registering the literal scope-style strings mcp:read/mcp:write — the OLD
// misfeature this fix retires — now correctly grants nothing: neither
// string matches any real tool NAME. Only the intuitive tool-name path
// (TestToolsListReturnsOnlyAllowedToolNames) works after this fix.
func TestToolsListLegacyScopeStringGrantsNothing(t *testing.T) {
	t.Parallel()
	res := inmem.NewStaticResolver()
	res.Register("k-legacy-scope", auth.Identity{
		AGID:         "01900000-0000-7000-8000-0000000000d1",
		PartnerName:  "Legacy Scope Co",
		TenantID:     testTenantID,
		AllowedTools: []string{"mcp:read", "mcp:write"},
		Active:       true,
	})
	r := httpadapter.NewRouter(res, fakeUpstreams(t))
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/list", "k-legacy-scope", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, raw)
	}
	var rpc struct {
		Result map[string]any `json:"result"`
	}
	_ = json.Unmarshal(raw, &rpc)
	tools, _ := rpc.Result["tools"].([]any)
	if len(tools) != 0 {
		t.Errorf("expected 0 tools for a scope-string allowlist, got %d (%v)", len(tools), tools)
	}
}

func TestToolsCallToolNotAllowedDenies403(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "familiar_nudge",
			"arguments": map[string]any{"learner_gcid": "01900000-0000-7000-8000-000000000099", "message": "x"},
		},
	}
	// k-read's allowlist names only course_catalog; familiar_nudge is a
	// known catalogue tool but absent from the allowlist → 403.
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/call", "k-read", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "TOOL_NOT_ALLOWED") {
		t.Errorf("expected TOOL_NOT_ALLOWED code, got %s", raw)
	}
}

// A partner whose allowlist names only a write-tier tool (familiar_nudge,
// RequiredScope=mcp:write) may call it — RequiredScope is informational
// metadata only, never consulted for authorization. (The call itself
// still fails loud at the dispatch layer because chora-consumption
// exposes no HTTP nudge route — see dispatch.go — but that is a
// DIFFERENT failure than a 403 at the gate.)
func TestToolsCallWriteToolAllowedByNameRegardlessOfTier(t *testing.T) {
	t.Parallel()
	res := inmem.NewStaticResolver()
	res.Register("k-write-only", auth.Identity{
		AGID:         "01900000-0000-7000-8000-0000000000d0",
		PartnerName:  "Write-Only Co",
		TenantID:     testTenantID,
		AllowedTools: []string{"familiar_nudge"},
		Active:       true,
	})
	r := httpadapter.NewRouter(res, fakeUpstreams(t))
	body := map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "tools/call",
		"params": map[string]any{
			"name":      "familiar_nudge",
			"arguments": map[string]any{"learner_gcid": "01900000-0000-7000-8000-000000000099", "message": "x"},
		},
	}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/call", "k-write-only", body)
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("expected authorization to succeed (tool-name allowlisted), got 403: %s", raw)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (json-rpc envelope), got %d body=%s", resp.StatusCode, raw)
	}
}

func TestToolsCallUnknownTool(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "does_not_exist",
			"arguments": map[string]any{},
		},
	}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/call", "k-read", body)
	// JSON-RPC errors return HTTP 200 with `error` envelope per JSON-RPC 2.0.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with error envelope, got %d body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `"error"`) {
		t.Errorf("expected error in response: %s", raw)
	}
}

func TestToolsCallReturnsRealCitations(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{
		"jsonrpc": "2.0", "id": 42, "method": "tools/call",
		"params": map[string]any{
			"name":      "course_catalog", // wired (tenant-only) → real fan-out
			"arguments": map[string]any{},
		},
	}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/call", "k-read", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, raw)
	}
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"Text"`
			} `json:"content"`
			Citations []struct {
				Source string `json:"Source"`
				Ref    string `json:"Ref"`
			} `json:"citations"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if rpc.Result.IsError {
		t.Fatalf("expected success result, got error: %s", raw)
	}
	if len(rpc.Result.Citations) == 0 {
		t.Fatalf("expected at least one citation per IMDA D4 transparency")
	}
	// Citation must derive from the REAL upstream entity, never a stub.
	c := rpc.Result.Citations[0]
	if c.Source != "chora.delivery" {
		t.Errorf("citation source = %s, want chora.delivery", c.Source)
	}
	if !strings.Contains(c.Ref, "course-1") || strings.HasPrefix(c.Ref, "stub:") {
		t.Errorf("citation must reference the real course id, got %q", c.Ref)
	}
	if len(rpc.Result.Content) == 0 || !strings.Contains(rpc.Result.Content[0].Text, "Topology 101") {
		t.Errorf("content must summarise the real upstream entity, got %+v", rpc.Result.Content)
	}
}

// atom_search is fail-loud (creation requires a learner gcid the AGID-only
// partner cannot supply). tools/call returns HTTP 200 with isError=true.
// Uses k-rw (allowlisted for atom_search) so the request reaches the
// dispatcher — k-read's narrower allowlist would otherwise 403 first.
func TestToolsCallAtomSearchFailsLoud(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{
			"name":      "atom_search",
			"arguments": map[string]any{"query": "topology"},
		},
	}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/call", "k-rw", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (json-rpc), got %d body=%s", resp.StatusCode, raw)
	}
	var rpc struct {
		Result struct {
			Citations []any `json:"citations"`
			IsError   bool  `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &rpc)
	if !rpc.Result.IsError {
		t.Errorf("atom_search must fail loud (isError=true), got %s", raw)
	}
	if len(rpc.Result.Citations) != 0 {
		t.Errorf("fail-loud result must carry zero citations")
	}
}

// A missing tenant on the partner makes a wired tool fail loud rather than
// calling the upstream unscoped.
func TestToolsCallNoTenantFailsLoud(t *testing.T) {
	t.Parallel()
	res := inmem.NewStaticResolver()
	res.Register("k-no-tenant", auth.Identity{
		AGID:         "01900000-0000-7000-8000-0000000000c0",
		PartnerName:  "No Tenant Co",
		AllowedTools: []string{"course_catalog"},
		Active:       true,
	})
	r := httpadapter.NewRouter(res, fakeUpstreams(t))
	body := map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "tools/call",
		"params": map[string]any{"name": "course_catalog", "arguments": map[string]any{}},
	}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/call", "k-no-tenant", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, raw)
	}
	var rpc struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &rpc)
	if !rpc.Result.IsError {
		t.Errorf("course_catalog must fail loud with no tenant context, got %s", raw)
	}
}

func TestToolsCallUnknownMethod(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
	// Sending tools/list shape to /mcp/tools/call should still work because
	// the method field is what matters; here we send a deliberately
	// malformed method to trigger the unknown-method path.
	body["method"] = "weird/method"
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/call", "k-read", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `"error"`) {
		t.Errorf("expected error envelope: %s", raw)
	}
}

func TestToolsListWrongMethodReturnsError(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call"}
	// Sending tools/call method to /mcp/tools/list endpoint.
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/list", "k-read", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (json-rpc error envelope), got %d body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `"error"`) {
		t.Errorf("expected error envelope: %s", raw)
	}
}

func TestNonPostMethodNotAllowed(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	resp, _ := doJSON(t, r, http.MethodGet, "/mcp/tools/list", "k-read", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

func TestBadJSONReturnsParseError(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/list", bytes.NewBufferString("{not-json"))
	req.Header.Set("X-API-Key", "k-read")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with json-rpc parse error, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("expected error envelope: %s", rec.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Branch completion tests — cover the remaining status/error branches of the
// MCP handlers (405 method guards, per-handler auth failures, JSON-RPC
// envelope + params validation, and the authenticate/writeJSON error tails).
// ----------------------------------------------------------------------------

// boomResolver surfaces an unexpected error from the resolver so the
// authenticate default branch (HTTP 500 INTERNAL) can be exercised — no
// in-memory resolver ever returns a non-sentinel error, but a remote
// registry failure would.
type boomResolver struct{}

func (boomResolver) ResolveAPIKey(string) (auth.Identity, error) {
	return auth.Identity{}, errors.New("partner registry unreachable")
}

// failingWriter fails every Write so writeJSON's encode-error tail
// (log.Printf) executes. WriteHeader must still work so the handler
// reaches the encoder.
type failingWriter struct {
	http.ResponseWriter
}

func (failingWriter) Write(b []byte) (int, error) { return 0, errors.New("simulated write failure") }

func TestHealthEndpointRejectsNonGet(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	// health guards on MethodGet; a non-GET hit must be 405 (not a JSON body).
	for _, p := range []string{"/health", "/healthz", "/readyz"} {
		resp, body := doJSON(t, r, http.MethodPost, p, "", nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405, got %d body=%s", p, resp.StatusCode, body)
		}
	}
}

func TestToolsCallRejectsNonPost(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	resp, _ := doJSON(t, r, http.MethodGet, "/mcp/tools/call", "", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

func TestToolsCallMissingApiKey(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call"}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/call", "", body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d body=%s", resp.StatusCode, raw)
	}
}

func TestToolsCallBadJSONReturnsParseError(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/call", bytes.NewBufferString("not-json"))
	req.Header.Set("X-API-Key", "k-read")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with json-rpc parse error, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("expected error envelope: %s", rec.Body.String())
	}
}

func TestToolsCallInvalidParams(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	// params must unmarshal into callParams{Name string}; a number for name
	// is a type error → JSON-RPC invalid params (-32602), not a 403/404.
	body := map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": 5, "arguments": map[string]any{}},
	}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/call", "k-read", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (json-rpc error envelope), got %d body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `-32602`) {
		t.Errorf("expected invalid-params code -32602, got %s", raw)
	}
}

func TestToolsListRejectsNonV2Envelope(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	// decodeRPC validates the jsonrpc field is exactly "2.0"; a v1 envelope
	// must surface as a parse error rather than being processed.
	body := map[string]any{"jsonrpc": "1.0", "id": 1, "method": "tools/list"}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/list", "k-read", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (json-rpc error envelope), got %d body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `"error"`) {
		t.Errorf("expected error envelope for jsonrpc 1.0, got %s", raw)
	}
}

func TestAuthenticateUnknownResolverErrorReturns500(t *testing.T) {
	t.Parallel()
	// A resolver error that matches no sentinel (missing/invalid/inactive)
	// routes to the authenticate default branch → HTTP 500 INTERNAL rather
	// than leaking a misleading 401.
	r := httpadapter.NewRouter(boomResolver{}, fakeUpstreams(t))
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
	resp, raw := doJSON(t, r, http.MethodPost, "/mcp/tools/list", "any-key", body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 for unexpected resolver error, got %d body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `"INTERNAL"`) {
		t.Errorf("expected INTERNAL error code, got %s", raw)
	}
}

func TestWriteJSONEncodeErrorIsLoggedNotPanicked(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(failingWriter{rec}, req)
	// The health handler must complete without a panic even when the
	// underlying writer fails during JSON encoding — the encoder error is
	// degraded to a log line.
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 before encode failure, got %d", rec.Code)
	}
}
