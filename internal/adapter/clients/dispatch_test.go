// Package clients_test — dispatch_test.go: per-tool upstream fan-out tests.
//
// Each wired tool stands up a fake upstream (httptest) and asserts the
// gateway issues the correct request (method/path/X-Tenant-Id) and maps
// the upstream response into a real tool.Result with citations derived
// from the actual returned entities (never "stub:..."). Tools that cannot
// be wired truthfully (creation/governance require a learner gcid the
// AGID-only partner lacks; consumption exposes no nudge route) must FAIL
// LOUD — IsError=true, zero citations, never a fabricated success.
package clients_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-mcp-gateway/internal/adapter/clients"
	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/auth"
)

const testTenant = "01900000-0000-7000-8000-0000000000aa"

func rwIdentity() auth.Identity {
	return auth.Identity{
		AGID:        "agid:test",
		PartnerName: "Acme",
		TenantID:    testTenant,
		AllowedTools: []string{
			"atom_search", "course_catalog", "familiar_nudge",
			"governance_check", "learning_path_query",
		},
		Active: true,
	}
}

// ----------------------------------------------------------------------------
// course_catalog → GET {delivery}/api/courses (tenant-only)
// ----------------------------------------------------------------------------

func TestExecuteCourseCatalogWiresDeliveryUpstream(t *testing.T) {
	t.Parallel()
	var gotMethod, gotPath, gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotTenant = r.Method, r.URL.Path, r.Header.Get("X-Tenant-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"course-7","title":"Intro to Topology"},{"id":"course-8","title":"Algebra"}],"total":2}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: srv.URL})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog", map[string]any{}, rwIdentity())

	if res.IsError {
		t.Fatalf("expected success, got error result: %+v", res.Content)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("upstream method = %s, want GET", gotMethod)
	}
	if gotPath != "/api/courses" {
		t.Errorf("upstream path = %s, want /api/courses", gotPath)
	}
	if gotTenant != testTenant {
		t.Errorf("X-Tenant-Id = %q, want %q", gotTenant, testTenant)
	}
	if len(res.Citations) < 2 {
		t.Fatalf("expected >=2 entity citations, got %d", len(res.Citations))
	}
	refs := ""
	for _, c := range res.Citations {
		if c.Source != "chora.delivery" {
			t.Errorf("citation source = %s, want chora.delivery", c.Source)
		}
		if strings.HasPrefix(c.Ref, "stub:") {
			t.Errorf("stub citation leaked: %+v", c)
		}
		refs += c.Ref + " "
	}
	if !strings.Contains(refs, "course-7") || !strings.Contains(refs, "course-8") {
		t.Errorf("citations must reference real course ids, got %q", refs)
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "Intro to Topology") {
		t.Errorf("content must summarise real upstream entities, got %+v", res.Content)
	}
}

func TestExecuteCourseCatalogForwardsPaging(t *testing.T) {
	t.Parallel()
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: srv.URL})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog",
		map[string]any{"limit": float64(5), "offset": float64(10)}, rwIdentity())

	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	if !strings.Contains(gotQuery, "limit=5") || !strings.Contains(gotQuery, "offset=10") {
		t.Errorf("paging not forwarded, query = %q", gotQuery)
	}
	// Empty catalogue still carries a provenance citation (IMDA D4).
	if len(res.Citations) == 0 {
		t.Errorf("empty result must still carry a provenance citation")
	}
}

func TestExecuteCourseCatalogForwardsStringPaging(t *testing.T) {
	t.Parallel()
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: srv.URL})
	// Some MCP clients send numeric args as strings.
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog",
		map[string]any{"limit": "25"}, rwIdentity())
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	if !strings.Contains(gotQuery, "limit=25") {
		t.Errorf("string paging not forwarded, query = %q", gotQuery)
	}
}

func TestExecuteCourseCatalogUpstream5xxFailsLoud(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: srv.URL})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog", nil, rwIdentity())

	if !res.IsError {
		t.Fatalf("expected fail-loud on upstream 5xx")
	}
	if len(res.Citations) != 0 {
		t.Errorf("error result must carry zero citations, got %d", len(res.Citations))
	}
	if !strings.Contains(res.Content[0].Text, "500") {
		t.Errorf("error must surface upstream status, got %q", res.Content[0].Text)
	}
}

func TestExecuteCourseCatalogUnreachableFailsLoud(t *testing.T) {
	t.Parallel()
	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: "http://127.0.0.1:1"})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog", nil, rwIdentity())
	if !res.IsError {
		t.Fatalf("expected fail-loud on transport error")
	}
}

func TestExecuteCourseCatalogUnconfiguredFailsLoud(t *testing.T) {
	t.Parallel()
	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{}) // delivery URL absent
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog", nil, rwIdentity())
	if !res.IsError {
		t.Fatalf("expected fail-loud when upstream URL not configured")
	}
}

func TestExecuteCourseCatalogNoTenantFailsLoud(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("upstream MUST NOT be called without a tenant context")
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: srv.URL})
	id := rwIdentity()
	id.TenantID = ""
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog", nil, id)
	if !res.IsError {
		t.Fatalf("expected fail-loud with no tenant context")
	}
}

func TestExecuteCourseCatalogPropagatesTraceparent(t *testing.T) {
	t.Parallel()
	var gotTP, gotTS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTP, gotTS = r.Header.Get("traceparent"), r.Header.Get("tracestate")
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: srv.URL})
	ctx := clients.WithTrace(context.Background(), "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "vendor=abc")
	res := clients.NewDispatcher(reg).Execute(ctx, "course_catalog", nil, rwIdentity())

	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	if gotTP != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Errorf("traceparent not propagated to upstream, got %q", gotTP)
	}
	if gotTS != "vendor=abc" {
		t.Errorf("tracestate not propagated to upstream, got %q", gotTS)
	}
}

// ----------------------------------------------------------------------------
// learning_path_query → GET {consumption}/api/learning-paths (tenant-only)
// ----------------------------------------------------------------------------

func TestExecuteLearningPathQueryWiresConsumptionUpstream(t *testing.T) {
	t.Parallel()
	var gotMethod, gotPath, gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotTenant = r.Method, r.URL.Path, r.Header.Get("X-Tenant-Id")
		_, _ = w.Write([]byte(`{"items":[{"path_id":"lp-1","name":"Calculus","owner_gcid":"gcid-a","completed":true,"atom_ids":["a1","a2"]},{"path_id":"lp-2","name":"Stats","owner_gcid":"gcid-b","completed":false}]}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvConsumption: srv.URL})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "learning_path_query", nil, rwIdentity())

	if res.IsError {
		t.Fatalf("expected success, got error result: %+v", res.Content)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/learning-paths" {
		t.Errorf("upstream request = %s %s, want GET /api/learning-paths", gotMethod, gotPath)
	}
	if gotTenant != testTenant {
		t.Errorf("X-Tenant-Id = %q, want %q", gotTenant, testTenant)
	}
	if len(res.Citations) < 2 {
		t.Fatalf("expected >=2 entity citations, got %d", len(res.Citations))
	}
	refs := ""
	for _, c := range res.Citations {
		if c.Source != "chora.consumption" {
			t.Errorf("citation source = %s, want chora.consumption", c.Source)
		}
		if strings.HasPrefix(c.Ref, "stub:") {
			t.Errorf("stub citation leaked: %+v", c)
		}
		refs += c.Ref + " "
	}
	if !strings.Contains(refs, "lp-1") || !strings.Contains(refs, "lp-2") {
		t.Errorf("citations must reference real path ids, got %q", refs)
	}
}

func TestExecuteLearningPathQueryFiltersByLearner(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"path_id":"lp-1","name":"Calculus","owner_gcid":"gcid-a"},{"path_id":"lp-2","name":"Stats","owner_gcid":"gcid-b"}]}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvConsumption: srv.URL})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "learning_path_query",
		map[string]any{"learner_gcid": "gcid-b"}, rwIdentity())

	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	if len(res.Citations) != 1 {
		t.Fatalf("expected exactly 1 citation after learner filter, got %d", len(res.Citations))
	}
	if !strings.Contains(res.Citations[0].Ref, "lp-2") {
		t.Errorf("expected lp-2 after filter, got %s", res.Citations[0].Ref)
	}
}

func TestExecuteLearningPathQuery5xxFailsLoud(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvConsumption: srv.URL})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "learning_path_query", nil, rwIdentity())
	if !res.IsError {
		t.Fatalf("expected fail-loud on upstream 502")
	}
	if len(res.Citations) != 0 {
		t.Errorf("error result must carry zero citations")
	}
}

// ----------------------------------------------------------------------------
// FAIL-LOUD tools (no truthful wiring possible)
// ----------------------------------------------------------------------------

func TestExecuteAtomSearchFailsLoud(t *testing.T) {
	t.Parallel()
	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvCreation: "http://example.invalid"})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "atom_search",
		map[string]any{"query": "topology"}, rwIdentity())
	if !res.IsError {
		t.Fatalf("atom_search must fail loud: chora-creation requires a learner gcid the AGID-only partner cannot supply")
	}
	if len(res.Citations) != 0 {
		t.Errorf("fail-loud result must carry zero citations")
	}
	if !strings.Contains(strings.ToLower(res.Content[0].Text), "gcid") {
		t.Errorf("error must explain the missing-gcid contract gap, got %q", res.Content[0].Text)
	}
}

func TestExecuteGovernanceCheckFailsLoud(t *testing.T) {
	t.Parallel()
	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvGovernance: "http://example.invalid"})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "governance_check",
		map[string]any{"action": "read"}, rwIdentity())
	if !res.IsError {
		t.Fatalf("governance_check must fail loud: /api/gatekeeper/evaluate requires a gcid context")
	}
	if !strings.Contains(strings.ToLower(res.Content[0].Text), "gcid") {
		t.Errorf("error must explain the missing-gcid contract gap, got %q", res.Content[0].Text)
	}
}

func TestExecuteFamiliarNudgeFailsLoud(t *testing.T) {
	t.Parallel()
	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvConsumption: "http://example.invalid"})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "familiar_nudge",
		map[string]any{"learner_gcid": "gcid-x", "message": "keep going"}, rwIdentity())
	if !res.IsError {
		t.Fatalf("familiar_nudge must fail loud: consumption exposes no HTTP nudge route")
	}
	if !strings.Contains(strings.ToLower(res.Content[0].Text), "route") {
		t.Errorf("error must explain the missing upstream route, got %q", res.Content[0].Text)
	}
}

func TestExecuteUnknownToolFailsLoud(t *testing.T) {
	t.Parallel()
	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "does_not_exist", nil, rwIdentity())
	if !res.IsError {
		t.Fatalf("unknown tool must fail loud")
	}
}

// model_broker_invoke is RETIRED (ADR-146 — chora-model-broker-router gone;
// model-gateway is gRPC-only). It must no longer have an upstream mapping
// and must fail loud if dispatched.
func TestExecuteModelBrokerInvokeRemoved(t *testing.T) {
	t.Parallel()
	if _, err := clients.ToolUpstream("model_broker_invoke"); err == nil {
		t.Errorf("model_broker_invoke must have NO upstream mapping (ADR-146 retired)")
	}
	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "model_broker_invoke", nil, rwIdentity())
	if !res.IsError {
		t.Fatalf("model_broker_invoke must fail loud (removed)")
	}
}

// ----------------------------------------------------------------------------
// Branch completion — remaining fail-loud + formatting branches of the two
// wired tools (decode failures, fallback display names, tenant/upstream
// guards for learning_path_query, empty-result provenance, numeric arg
// coercion variants, malformed upstream URLs).
// ----------------------------------------------------------------------------

func TestExecuteCourseCatalogDecodeErrorFailsLoud(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`this is not json`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: srv.URL})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog", nil, rwIdentity())

	if !res.IsError {
		t.Fatalf("expected fail-loud on undecodable upstream body")
	}
	if !strings.Contains(strings.ToLower(res.Content[0].Text), "decode") {
		t.Errorf("error must mention the decode failure, got %q", res.Content[0].Text)
	}
}

func TestExecuteCourseCatalogFallsBackToUntitled(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"course-x","title":""}],"total":1}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: srv.URL})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog", nil, rwIdentity())

	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "(untitled)") {
		t.Errorf("expected (untitled) fallback for a blank title, got %q", res.Content[0].Text)
	}
}

// A registry base URL that url.Parse rejects cascades into doGET's request
// build failure, which must surface as a fail-loud result rather than a
// panic or a dropped error.
func TestExecuteCourseCatalogInvalidUpstreamURLFailsLoud(t *testing.T) {
	t.Parallel()
	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: "http://exa mple"})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog", nil, rwIdentity())
	if !res.IsError {
		t.Fatalf("expected fail-loud on unparseable upstream URL")
	}
	if len(res.Citations) != 0 {
		t.Errorf("error result must carry zero citations")
	}
}

func TestExecuteCourseCatalogCoercesIntPaging(t *testing.T) {
	t.Parallel()
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: srv.URL})
	// Some callers produce Go-native int/int64 values (not JSON float64) —
	// intArg must coerce both.
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog",
		map[string]any{"limit": 5, "offset": int64(10)}, rwIdentity())

	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	if !strings.Contains(gotQuery, "limit=5") || !strings.Contains(gotQuery, "offset=10") {
		t.Errorf("int paging not forwarded, query = %q", gotQuery)
	}
}

func TestExecuteCourseCatalogIgnoresNonNumericPaging(t *testing.T) {
	t.Parallel()
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvDelivery: srv.URL})
	// An unparseable numeric string must be ignored (no limit param), never
	// coerced to a bogus value or turned into an error.
	res := clients.NewDispatcher(reg).Execute(context.Background(), "course_catalog",
		map[string]any{"limit": "not-a-number"}, rwIdentity())

	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	if strings.Contains(gotQuery, "limit=") {
		t.Errorf("non-numeric limit must be dropped, query = %q", gotQuery)
	}
}

func TestExecuteLearningPathQueryNoTenantFailsLoud(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("upstream MUST NOT be called without a tenant context")
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvConsumption: srv.URL})
	id := rwIdentity()
	id.TenantID = ""
	res := clients.NewDispatcher(reg).Execute(context.Background(), "learning_path_query", nil, id)
	if !res.IsError {
		t.Fatalf("expected fail-loud with no tenant context")
	}
}

func TestExecuteLearningPathQueryUnconfiguredFailsLoud(t *testing.T) {
	t.Parallel()
	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{}) // consumption URL absent
	res := clients.NewDispatcher(reg).Execute(context.Background(), "learning_path_query", nil, rwIdentity())
	if !res.IsError {
		t.Fatalf("expected fail-loud when upstream URL not configured")
	}
	if !strings.Contains(res.Content[0].Text, "not configured") {
		t.Errorf("error must diagnose the missing upstream, got %q", res.Content[0].Text)
	}
}

func TestExecuteLearningPathQueryTransportErrorFailsLoud(t *testing.T) {
	t.Parallel()
	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvConsumption: "http://127.0.0.1:1"})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "learning_path_query", nil, rwIdentity())
	if !res.IsError {
		t.Fatalf("expected fail-loud on transport error")
	}
}

func TestExecuteLearningPathQueryDecodeErrorFailsLoud(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json either`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvConsumption: srv.URL})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "learning_path_query", nil, rwIdentity())

	if !res.IsError {
		t.Fatalf("expected fail-loud on undecodable upstream body")
	}
	if !strings.Contains(strings.ToLower(res.Content[0].Text), "decode") {
		t.Errorf("error must mention the decode failure, got %q", res.Content[0].Text)
	}
}

func TestExecuteLearningPathQueryUnnamedPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"path_id":"lp-x","name":"","owner_gcid":"gcid-a"}]}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvConsumption: srv.URL})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "learning_path_query", nil, rwIdentity())

	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "(unnamed path)") {
		t.Errorf("expected (unnamed path) fallback for a blank name, got %q", res.Content[0].Text)
	}
}

func TestExecuteLearningPathQueryEmptyResultsProvenance(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()

	reg := clients.NewRegistryFromMap(map[clients.EnvKey]string{clients.EnvConsumption: srv.URL})
	res := clients.NewDispatcher(reg).Execute(context.Background(), "learning_path_query", nil, rwIdentity())

	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	// Zero matched paths must still carry a provenance citation pointing at
	// the real upstream resource (IMDA D4).
	if len(res.Citations) != 1 {
		t.Fatalf("expected exactly 1 provenance citation for empty results, got %d", len(res.Citations))
	}
	if !strings.Contains(res.Citations[0].Ref, "no-results") {
		t.Errorf("provenance citation must mark the empty result, got %+v", res.Citations[0])
	}
}
