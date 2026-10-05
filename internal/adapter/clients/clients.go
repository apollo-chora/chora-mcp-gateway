// Package clients holds outbound HTTP clients to upstream Chora
// services. Each client URL comes from an env var (no inline config —
// rule `feedback_no_inline_config`). Keys:
//
//	MCP_UPSTREAM_CREATION       → chora-creation     (atom_search — fail-loud, see dispatch.go)
//	MCP_UPSTREAM_DELIVERY       → chora-delivery     (course_catalog)
//	MCP_UPSTREAM_CONSUMPTION    → chora-consumption  (learning_path_query; familiar_nudge fail-loud)
//	MCP_UPSTREAM_GOVERNANCE     → chora-governance   (governance_check — fail-loud, see dispatch.go)
//
// Empty / missing env vars cause Lookup to return ErrUpstreamNotConfigured,
// which the dispatcher (dispatch.go) maps to a fail-loud tool error — it
// NEVER fabricates a success.
//
// NB: there is deliberately no MCP_UPSTREAM_MODEL_BROKER key. The
// chora-model-broker-router it pointed at is RETIRED (ADR-146); the single
// LLM chokepoint is chora-model-gateway which is gRPC-only (ADR-163, no
// HTTP invoke route reachable over this seam). The model_broker_invoke
// tool was removed rather than repointed at the dead router.
package clients

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrUpstreamNotConfigured signals a missing env-var URL.
var ErrUpstreamNotConfigured = errors.New("clients: upstream URL not configured")

// EnvKey is the env-var key for an upstream URL.
type EnvKey string

const (
	EnvCreation    EnvKey = "MCP_UPSTREAM_CREATION"
	EnvDelivery    EnvKey = "MCP_UPSTREAM_DELIVERY"
	EnvConsumption EnvKey = "MCP_UPSTREAM_CONSUMPTION"
	EnvGovernance  EnvKey = "MCP_UPSTREAM_GOVERNANCE"
)

// allEnvKeys is the canonical set of upstream env keys the Registry reads.
var allEnvKeys = []EnvKey{EnvCreation, EnvDelivery, EnvConsumption, EnvGovernance}

// ToolUpstream maps each tool to its upstream env-var key. Tools that are
// fail-loud (atom_search, governance_check, familiar_nudge) still map to
// the owning domain's env key for diagnostics; the dispatcher decides
// whether a truthful call is possible. model_broker_invoke is RETIRED
// (ADR-146) and intentionally has NO mapping.
func ToolUpstream(toolName string) (EnvKey, error) {
	switch toolName {
	case "atom_search":
		return EnvCreation, nil
	case "course_catalog":
		return EnvDelivery, nil
	case "familiar_nudge", "learning_path_query":
		return EnvConsumption, nil
	case "governance_check":
		return EnvGovernance, nil
	default:
		return "", fmt.Errorf("clients: no upstream mapping for tool %q", toolName)
	}
}

// Registry holds initialised HTTP clients per upstream env-var key.
type Registry struct {
	clients map[EnvKey]*upstreamClient
}

type upstreamClient struct {
	baseURL string
	httpc   *http.Client
}

// NewRegistry reads each known env var and builds a client for any that
// are configured. Missing entries are not errors — they surface at call
// time through Lookup → ErrUpstreamNotConfigured.
func NewRegistry() *Registry {
	urls := make(map[EnvKey]string, len(allEnvKeys))
	for _, k := range allEnvKeys {
		urls[k] = os.Getenv(string(k))
	}
	return NewRegistryFromMap(urls)
}

// NewRegistryFromMap builds a Registry from an explicit key→URL map. Used
// by NewRegistry (env-sourced) and by tests (httptest upstreams) so the
// fan-out can be exercised without mutating process env. Blank URLs are
// skipped and surface as ErrUpstreamNotConfigured at call time.
func NewRegistryFromMap(urls map[EnvKey]string) *Registry {
	r := &Registry{clients: map[EnvKey]*upstreamClient{}}
	for k, v := range urls {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		r.clients[k] = &upstreamClient{
			baseURL: strings.TrimRight(v, "/"),
			httpc: &http.Client{
				Timeout: 10 * time.Second,
			},
		}
	}
	return r
}

// Lookup returns the configured base URL for an env key, or
// ErrUpstreamNotConfigured.
func (r *Registry) Lookup(k EnvKey) (string, error) {
	c, ok := r.clients[k]
	if !ok {
		return "", ErrUpstreamNotConfigured
	}
	return c.baseURL, nil
}

// HTTPClient returns the http.Client for an env key (nil if missing).
func (r *Registry) HTTPClient(k EnvKey) *http.Client {
	c, ok := r.clients[k]
	if !ok {
		return nil
	}
	return c.httpc
}
