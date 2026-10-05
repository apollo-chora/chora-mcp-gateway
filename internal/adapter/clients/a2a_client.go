// Package clients — a2a_client.go: resolves MCP API keys against
// chora-a2a-gateway's per-tenant MCPConfig registry.
//
// Per ADR-132 §10 + the A2A skill: MCP-as-a-Service is an A2A add-on. The
// MCP gateway authenticates inbound MCP clients (Claude Desktop, Cursor,
// etc.) by submitting their X-API-Key to chora-a2a-gateway, which holds
// the per-tenant MCP API key hash + tool allowlist (the
// `MCPConfig` aggregate in chora-a2a-gateway's repo).
//
// The wire contract here is the simplest possible HTTP envelope; M12
// upgrades to a gRPC contract from chora-contracts/proto/services/a2a/v1.
//
// Env var: MCP_UPSTREAM_A2A_GATEWAY (e.g. https://chora-a2a-gateway.run.app).
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/auth"
)

// EnvA2AGateway is the env var key for the chora-a2a-gateway base URL.
const EnvA2AGateway = "MCP_UPSTREAM_A2A_GATEWAY"

// A2AClient resolves MCP API keys against chora-a2a-gateway.
//
// Implements auth.Resolver. The Authenticator (in domain/auth) wraps it
// with the missing-key + inactive-partner gates.
type A2AClient struct {
	baseURL string
	httpc   *http.Client

	// MockResolver is an optional in-memory fallback for tests + skeleton.
	// When non-nil and baseURL is empty, ResolveAPIKey delegates to it.
	MockResolver auth.Resolver
}

// NewA2AClientFromEnv reads MCP_UPSTREAM_A2A_GATEWAY and constructs a
// client. Returns nil baseURL when the env var is missing, in which case
// the caller should set MockResolver for tests / skeleton operation.
func NewA2AClientFromEnv() *A2AClient {
	base := strings.TrimSpace(os.Getenv(EnvA2AGateway))
	c := &A2AClient{}
	if base != "" {
		c.baseURL = strings.TrimRight(base, "/")
		c.httpc = &http.Client{Timeout: 5 * time.Second}
	}
	return c
}

// resolverResponse mirrors the body of POST /admin/mcp/{tenant_id}/resolve.
//
// In production chora-a2a-gateway's MCP resolver endpoint returns:
//
//	{
//	  "agid": "agid:...",
//	  "partner_name": "Tenant ABC",
//	  "tenant_id": "0190...",
//	  "allowed_scopes": ["course_catalog", ...],
//	  "active": true
//	}
//
// Despite the wire field name `allowed_scopes` — kept as-is here so no
// chora-a2a-gateway coordination is required; renaming the wire field to
// `allowed_tools` is a separate follow-up — the values are tool NAMES,
// not OAuth-style scope strings: chora-a2a-gateway's /admin/mcp/{tenant_id}
// registration endpoint takes `allowed_tools` (tool names) and surfaces
// them back out verbatim under this stale key. chora-mcp-gateway's
// capability.Gate authorizes by tool-name membership in this list (see
// internal/domain/capability), so the Go field here is named AllowedTools
// to match what it actually carries.
//
// tenant_id is the owning tenant of the per-tenant MCP add-on key (the
// data scope the partner acts within). The dispatcher propagates it to
// upstream domain services as X-Tenant-Id and fails loud when it is empty.
type resolverResponse struct {
	AGID         string   `json:"agid"`
	PartnerName  string   `json:"partner_name"`
	TenantID     string   `json:"tenant_id"`
	AllowedTools []string `json:"allowed_scopes"`
	Active       bool     `json:"active"`
}

// ResolveAPIKey looks up the supplied MCP API key against chora-a2a-gateway.
// Returns auth.ErrInvalidKey on 401/404, auth.ErrPartnerInactive when the
// returned identity is inactive, or a wrapped network error on transport
// failure.
func (c *A2AClient) ResolveAPIKey(key string) (auth.Identity, error) {
	if strings.TrimSpace(key) == "" {
		return auth.Identity{}, auth.ErrMissingKey
	}
	// Skeleton fallback: delegate to MockResolver when no baseURL.
	if c.baseURL == "" {
		if c.MockResolver != nil {
			return c.MockResolver.ResolveAPIKey(key)
		}
		return auth.Identity{}, errors.New("a2a-client: not configured (set MCP_UPSTREAM_A2A_GATEWAY or wire MockResolver)")
	}

	u, err := url.Parse(c.baseURL + "/admin/mcp/_resolve")
	if err != nil {
		return auth.Identity{}, fmt.Errorf("a2a-client: bad base URL: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u.String(), nil)
	if err != nil {
		return auth.Identity{}, fmt.Errorf("a2a-client: build request: %w", err)
	}
	req.Header.Set("X-API-Key", key)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return auth.Identity{}, fmt.Errorf("a2a-client: transport: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		// fall-through
	case http.StatusUnauthorized, http.StatusNotFound:
		return auth.Identity{}, auth.ErrInvalidKey
	case http.StatusForbidden:
		return auth.Identity{}, auth.ErrPartnerInactive
	default:
		return auth.Identity{}, fmt.Errorf("a2a-client: HTTP %d", resp.StatusCode)
	}
	var out resolverResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return auth.Identity{}, fmt.Errorf("a2a-client: decode: %w", err)
	}
	return auth.Identity{
		AGID:         out.AGID,
		PartnerName:  out.PartnerName,
		TenantID:     out.TenantID,
		AllowedTools: out.AllowedTools,
		Active:       out.Active,
	}, nil
}
