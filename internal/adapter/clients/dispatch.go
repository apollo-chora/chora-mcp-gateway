// Package clients — dispatch.go: real MCP tool execution over the
// upstream HTTP-client seam.
//
// The Dispatcher fans each `tools/call` out to the owning Chora domain
// service over the Registry's HTTP clients and maps the upstream response
// into a tool.Result whose citations are derived from the ACTUAL returned
// entities (IMDA D4 Transparency) — never a "stub:..." placeholder.
//
// Tenant scope: the authenticated MCP partner carries the owning tenant
// of its per-tenant add-on key (auth.Identity.TenantID, ADR-132 §10). The
// dispatcher propagates it as `X-Tenant-Id` on every upstream call. An
// empty TenantID is a hard failure — never a default.
//
// Fail-loud (NO stubs, NO fabrication):
//
//   - atom_search       chora-creation gates ALL /api/* on a learner gcid
//     (tenantContext middleware); an AGID-only partner
//     holds no GCID (CLAUDE.md §1), and no tenant-only
//     atom-search route exists upstream → fail loud.
//   - governance_check  chora-governance /api/gatekeeper/evaluate likewise
//     requires a gcid the partner cannot supply → fail loud.
//   - familiar_nudge    chora-consumption exposes NO HTTP nudge route → fail loud.
//
// Wired (tenant-only, truthful):
//
//   - course_catalog       GET {delivery}/api/courses          (X-Tenant-Id)
//   - learning_path_query  GET {consumption}/api/learning-paths (X-Tenant-Id)
//
// model_broker_invoke is REMOVED entirely (ADR-146 retired the router;
// model-gateway is gRPC-only). See clients.go + tool.DefaultCatalog.
package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/auth"
	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/tool"
)

// Source labels for citations (canonical domain ids).
const (
	sourceDelivery    = "chora.delivery"
	sourceConsumption = "chora.consumption"
)

// Dispatcher executes MCP tools by fanning out over the Registry.
type Dispatcher struct {
	reg *Registry
}

// NewDispatcher wires a Dispatcher over an upstream Registry.
func NewDispatcher(reg *Registry) *Dispatcher { return &Dispatcher{reg: reg} }

// Execute runs a single MCP tool call. `id` is the authenticated partner
// (AGID + owning TenantID + scopes); `args` are the caller-supplied tool
// arguments. The return is always a tool.Result — fail-loud cases return
// an IsError result with zero citations, never an error a caller can drop.
func (d *Dispatcher) Execute(ctx context.Context, name string, args map[string]any, id auth.Identity) tool.Result {
	switch name {
	case "course_catalog":
		return d.courseCatalog(ctx, args, id)
	case "learning_path_query":
		return d.learningPathQuery(ctx, args, id)

	// ---- fail-loud: route exists but needs a gcid the partner cannot supply ----
	case "atom_search":
		return tool.ErrorResult("atom_search unavailable: chora-creation gates /api/atoms on a learner gcid " +
			"(tenantContext middleware), but an MCP partner is AGID-only and holds no gcid (CLAUDE.md §1); " +
			"no tenant-only atom-search route exists upstream — wiring it would require fabricating a gcid")
	case "governance_check":
		return tool.ErrorResult("governance_check unavailable: chora-governance /api/gatekeeper/evaluate requires " +
			"a gcid context (tenantContext middleware) that an AGID-only MCP partner cannot supply without fabrication")

	// ---- fail-loud: no upstream route exists at all ----
	case "familiar_nudge":
		return tool.ErrorResult("familiar_nudge unavailable: chora-consumption exposes no HTTP nudge route; " +
			"there is no real upstream endpoint to deliver a learner nudge over")

	default:
		return tool.ErrorResult("no executor for tool=" + name)
	}
}

// -----------------------------------------------------------------------------
// course_catalog → GET {delivery}/api/courses
// -----------------------------------------------------------------------------

func (d *Dispatcher) courseCatalog(ctx context.Context, args map[string]any, id auth.Identity) tool.Result {
	tenant := strings.TrimSpace(id.TenantID)
	if tenant == "" {
		return tool.ErrorResult("course_catalog: no tenant context on the authenticated partner " +
			"(the per-tenant MCP add-on key must resolve an owning tenant_id)")
	}
	base, httpc, err := d.upstream("course_catalog")
	if err != nil {
		return tool.ErrorResult(err.Error())
	}

	u := base + "/api/courses"
	q := url.Values{}
	if v, ok := intArg(args, "limit"); ok {
		q.Set("limit", strconv.Itoa(v))
	}
	if v, ok := intArg(args, "offset"); ok {
		q.Set("offset", strconv.Itoa(v))
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	resp, err := d.doGET(ctx, httpc, u, tenant)
	if err != nil {
		return tool.ErrorResult("course_catalog: upstream chora.delivery transport: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return upstreamStatusErr("course_catalog", sourceDelivery, resp)
	}

	var body struct {
		Items []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return tool.ErrorResult("course_catalog: decode upstream chora.delivery response: " + err.Error())
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d course(s) in the tenant catalogue:", len(body.Items))
	citations := make([]tool.Citation, 0, len(body.Items))
	for _, it := range body.Items {
		title := it.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(&sb, "\n- %s (%s)", title, it.ID)
		citations = append(citations, tool.Citation{Source: sourceDelivery, Ref: "course:" + it.ID})
	}
	if len(citations) == 0 {
		citations = append(citations, provenanceCitation(sourceDelivery, "course_catalog", tenant))
	}
	return mkResult("course_catalog", sb.String(), citations)
}

// -----------------------------------------------------------------------------
// learning_path_query → GET {consumption}/api/learning-paths
// -----------------------------------------------------------------------------

func (d *Dispatcher) learningPathQuery(ctx context.Context, args map[string]any, id auth.Identity) tool.Result {
	tenant := strings.TrimSpace(id.TenantID)
	if tenant == "" {
		return tool.ErrorResult("learning_path_query: no tenant context on the authenticated partner " +
			"(the per-tenant MCP add-on key must resolve an owning tenant_id)")
	}
	base, httpc, err := d.upstream("learning_path_query")
	if err != nil {
		return tool.ErrorResult(err.Error())
	}

	resp, err := d.doGET(ctx, httpc, base+"/api/learning-paths", tenant)
	if err != nil {
		return tool.ErrorResult("learning_path_query: upstream chora.consumption transport: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return upstreamStatusErr("learning_path_query", sourceConsumption, resp)
	}

	var body struct {
		Items []struct {
			PathID    string `json:"path_id"`
			Name      string `json:"name"`
			OwnerGCID string `json:"owner_gcid"`
			Completed bool   `json:"completed"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return tool.ErrorResult("learning_path_query: decode upstream chora.consumption response: " + err.Error())
	}

	// Optional: narrow to a single learner's paths (the partner names the
	// SUBJECT learner — this is the operation's target, not the caller's id).
	learner, _ := stringArg(args, "learner_gcid")
	learner = strings.TrimSpace(learner)

	var sb strings.Builder
	citations := make([]tool.Citation, 0, len(body.Items))
	matched := 0
	for _, it := range body.Items {
		if learner != "" && it.OwnerGCID != learner {
			continue
		}
		matched++
		status := "in-progress"
		if it.Completed {
			status = "completed"
		}
		name := it.Name
		if name == "" {
			name = "(unnamed path)"
		}
		fmt.Fprintf(&sb, "\n- %s (%s) — %s", name, it.PathID, status)
		citations = append(citations, tool.Citation{Source: sourceConsumption, Ref: "learning_path:" + it.PathID})
	}
	header := fmt.Sprintf("Found %d learning path(s)", matched)
	if learner != "" {
		header += " for learner " + learner
	}
	text := header + ":" + sb.String()
	if len(citations) == 0 {
		citations = append(citations, provenanceCitation(sourceConsumption, "learning_path_query", tenant))
	}
	return mkResult("learning_path_query", text, citations)
}

// -----------------------------------------------------------------------------
// Trace propagation (W3C traceparent/tracestate across the gateway→upstream hop)
// -----------------------------------------------------------------------------

type traceCtxKey struct{}

type traceContext struct {
	traceparent string
	tracestate  string
}

// WithTrace stashes the inbound W3C trace context so the dispatcher can
// propagate it onto upstream requests (OTLP-everywhere / mandatory
// traceparent). Empty values are ignored at send time.
func WithTrace(ctx context.Context, traceparent, tracestate string) context.Context {
	return context.WithValue(ctx, traceCtxKey{}, traceContext{
		traceparent: strings.TrimSpace(traceparent),
		tracestate:  strings.TrimSpace(tracestate),
	})
}

func traceFromContext(ctx context.Context) traceContext {
	tc, _ := ctx.Value(traceCtxKey{}).(traceContext)
	return tc
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// upstream resolves the base URL + HTTP client for a tool, or a fail-loud
// error when the upstream is unconfigured.
func (d *Dispatcher) upstream(toolName string) (string, *http.Client, error) {
	key, err := ToolUpstream(toolName)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", toolName, err)
	}
	base, err := d.reg.Lookup(key)
	if err != nil {
		return "", nil, fmt.Errorf("%s: upstream %s not configured: %w", toolName, key, err)
	}
	httpc := d.reg.HTTPClient(key)
	if httpc == nil {
		return "", nil, fmt.Errorf("%s: no HTTP client for upstream %s", toolName, key)
	}
	return base, httpc, nil
}

// doGET issues a tenant-scoped GET, propagating trace context.
func (d *Dispatcher) doGET(ctx context.Context, httpc *http.Client, u, tenant string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Tenant-Id", tenant)
	req.Header.Set("Accept", "application/json")
	if tc := traceFromContext(ctx); tc.traceparent != "" {
		req.Header.Set("traceparent", tc.traceparent)
		if tc.tracestate != "" {
			req.Header.Set("tracestate", tc.tracestate)
		}
	}
	return httpc.Do(req)
}

// mkResult builds a successful tool.Result, downgrading to a fail-loud
// error result if the (already-derived) citations somehow fail validation
// — we never emit an invalid success.
func mkResult(toolName, text string, citations []tool.Citation) tool.Result {
	res, err := tool.NewResult([]tool.ContentBlock{{Type: "text", Text: text}}, citations)
	if err != nil {
		return tool.ErrorResult(toolName + ": building result: " + err.Error())
	}
	return res
}

// upstreamStatusErr maps a non-2xx upstream response to a fail-loud result.
func upstreamStatusErr(toolName, source string, resp *http.Response) tool.Result {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = "(empty body)"
	}
	return tool.ErrorResult(fmt.Sprintf("%s: upstream %s returned HTTP %d: %s",
		toolName, source, resp.StatusCode, msg))
}

// provenanceCitation traces an empty result back to the upstream resource
// actually queried (IMDA D4 — a real, non-fabricated source reference even
// when zero entities were returned).
func provenanceCitation(source, toolName, tenant string) tool.Citation {
	return tool.Citation{Source: source, Ref: toolName + ":no-results:tenant:" + tenant}
}

// intArg coerces a JSON-decoded argument (float64 / int / numeric string)
// into an int.
func intArg(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i, true
		}
	}
	return 0, false
}

// stringArg extracts a string argument.
func stringArg(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}
