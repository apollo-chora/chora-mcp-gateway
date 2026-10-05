// Package auth implements MCP partner-key authentication delegated to
// the chora-a2a-gateway/partners registry.
//
// External MCP callers send an `X-API-Key` header. The Authenticator
// resolves the key against a Resolver (real impl calls
// chora-a2a-gateway over gRPC; tests use in-memory stubs) and returns
// an Identity. Identity carries:
//
//   - AGID            (UUIDv7, distinct from GCID)
//   - PartnerName     (display only, never an authority claim)
//   - TenantID        (owning tenant of the per-tenant MCP add-on)
//   - AllowedTools    (capability gate input — a tool-NAME allowlist,
//     NOT OAuth-style scopes; see capability.Gate)
//   - Active          (false → reject)
//
// TenantID is the tenant that purchased the MCP-as-a-Service add-on and
// minted this API key (`POST /admin/mcp/{tenant_id}` in
// chora-a2a-gateway, ADR-132 §10). It is the data scope the partner is
// authorised to read/act within and is propagated to upstream domain
// services as the `X-Tenant-Id` header. TenantID is NOT a GCID and NOT a
// TenantMembership of the agent — it is the subscriber's tenant. The
// dispatcher MUST treat an empty TenantID as a hard failure (fail loud),
// never substituting a default.
//
// Identity intentionally has NO Gcid field. AGID is distinct from GCID
// per CLAUDE.md §1 + .claude/rules/ddd-enforcement.md §10 — agents
// CANNOT hold TenantMembership/GCID. The IdentityHasNoGCID predicate is
// a runtime guard verified by tests. (tenant_id ≠ gcid, so the guard is
// unaffected.)
package auth

import (
	"errors"
	"reflect"
	"strings"
)

// Sentinel errors.
var (
	ErrMissingKey      = errors.New("auth: API key required")
	ErrInvalidKey      = errors.New("auth: API key invalid")
	ErrPartnerInactive = errors.New("auth: partner inactive (suspended or deleted)")
)

// Identity is the authenticated MCP partner.
//
// CLAUDE.md §1 / ddd-enforcement.md §10: agents are AGID-only — there
// is intentionally NO Gcid field on this struct.
type Identity struct {
	AGID         string
	PartnerName  string
	TenantID     string   // owning tenant of the MCP add-on (data scope; NOT a GCID)
	AllowedTools []string // tool-NAME allowlist (capability.Gate input)
	Active       bool
}

// HasTool reports whether the partner's allowlist includes the given
// tool name.
func (i Identity) HasTool(name string) bool {
	for _, t := range i.AllowedTools {
		if t == name {
			return true
		}
	}
	return false
}

// Resolver maps an opaque API key to an Identity. Real implementations
// call into the partner registry (chora-a2a-gateway). Tests use a stub.
type Resolver interface {
	ResolveAPIKey(key string) (Identity, error)
}

// Authenticator validates inbound API keys.
type Authenticator struct {
	resolver Resolver
}

// NewAuthenticator wires a Resolver.
func NewAuthenticator(r Resolver) *Authenticator {
	return &Authenticator{resolver: r}
}

// Authenticate trims, looks up, and gates an inbound API key.
func (a *Authenticator) Authenticate(key string) (Identity, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Identity{}, ErrMissingKey
	}
	id, err := a.resolver.ResolveAPIKey(key)
	if err != nil {
		return Identity{}, err
	}
	if !id.Active {
		return Identity{}, ErrPartnerInactive
	}
	return id, nil
}

// IdentityHasNoGCID is a reflective guard against accidentally adding a
// Gcid field to Identity in the future. Verified by the auth tests.
func IdentityHasNoGCID(i Identity) bool {
	t := reflect.TypeOf(i)
	for n := 0; n < t.NumField(); n++ {
		name := strings.ToLower(t.Field(n).Name)
		if name == "gcid" || name == "globalchoraid" {
			return false
		}
	}
	return true
}
