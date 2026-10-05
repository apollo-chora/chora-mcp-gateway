// Package capability gates MCP tool invocation by tool-NAME allowlist.
//
// The Gate checks whether the authenticated partner's allowed-tool list
// (auth.Identity.AllowedTools, resolved from chora-a2a-gateway's
// per-tenant MCPConfig registration) contains the requested tool's NAME.
// This is a tool-name allowlist, NOT a scope check: tool.Definition's
// RequiredScope (`mcp:read`/`mcp:write`) is informational metadata
// surfaced in tools/list only and is never consulted here.
//
// A prior revision of this Gate matched RequiredScope against the
// partner's allowlist, which silently rejected the intuitive
// registration path (tenants naming real tools, e.g. "course_catalog")
// and only "worked" for the misfeature of registering the literal
// strings "mcp:read"/"mcp:write". That behaviour is inverted here on
// purpose: name registration now works, scope-string registration now
// grants nothing.
package capability

import (
	"errors"
	"strings"
)

// Sentinel errors.
var (
	ErrUnknownTool    = errors.New("capability: unknown tool")
	ErrToolNotAllowed = errors.New("capability: tool not in partner's allowed-tool list")
)

// Gate enforces the tool-name allowlist policy.
type Gate struct{}

// NewGate builds a Gate.
func NewGate() *Gate {
	return &Gate{}
}

// Authorize returns nil if toolName is present in allowedTools — the
// authenticated partner's allowed-tool-name list. Mismatch →
// ErrToolNotAllowed.
func (g *Gate) Authorize(toolName string, allowedTools []string) error {
	if strings.TrimSpace(toolName) == "" {
		return ErrUnknownTool
	}
	for _, t := range allowedTools {
		if t == toolName {
			return nil
		}
	}
	return ErrToolNotAllowed
}
