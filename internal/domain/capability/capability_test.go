package capability_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/capability"
)

func TestGateAllowsWhenToolNameInAllowedList(t *testing.T) {
	t.Parallel()
	g := capability.NewGate()
	if err := g.Authorize("course_catalog", []string{"course_catalog"}); err != nil {
		t.Errorf("expected allow, got %v", err)
	}
}

func TestGateAllowsWhenOneOfSeveralAllowedNamesMatches(t *testing.T) {
	t.Parallel()
	g := capability.NewGate()
	if err := g.Authorize("learning_path_query", []string{"course_catalog", "learning_path_query"}); err != nil {
		t.Errorf("expected allow, got %v", err)
	}
}

func TestGateDeniesToolNameNotInAllowedList(t *testing.T) {
	t.Parallel()
	g := capability.NewGate()
	err := g.Authorize("familiar_nudge", []string{"course_catalog"})
	if !errors.Is(err, capability.ErrToolNotAllowed) {
		t.Fatalf("expected ErrToolNotAllowed, got %v", err)
	}
}

func TestGateDeniesEmptyToolName(t *testing.T) {
	t.Parallel()
	g := capability.NewGate()
	err := g.Authorize("", []string{"course_catalog"})
	if !errors.Is(err, capability.ErrUnknownTool) {
		t.Fatalf("expected ErrUnknownTool, got %v", err)
	}
}

func TestGateDeniesEmptyAllowedList(t *testing.T) {
	t.Parallel()
	g := capability.NewGate()
	err := g.Authorize("course_catalog", nil)
	if !errors.Is(err, capability.ErrToolNotAllowed) {
		t.Fatalf("expected ErrToolNotAllowed, got %v", err)
	}
}

// Registering the literal scope-style strings mcp:read/mcp:write — the OLD
// misfeature this fix retires — now correctly grants nothing: neither
// string is a real tool name, so it never matches. This behaviour
// inversion (scope-strings now denied, tool-names now allowed) is
// intentional; see the package doc.
func TestGateDeniesLegacyScopeStringRegistration(t *testing.T) {
	t.Parallel()
	g := capability.NewGate()
	err := g.Authorize("course_catalog", []string{"mcp:read"})
	if !errors.Is(err, capability.ErrToolNotAllowed) {
		t.Fatalf("expected ErrToolNotAllowed for a scope-string allowlist, got %v", err)
	}
}

// A partner whose allowlist names a write-tier tool (RequiredScope=
// mcp:write) may call it outright — the allowlist has no read/write
// tiering, only name membership. RequiredScope is metadata, never
// consulted here.
func TestGateAllowsWriteToolByNameRegardlessOfTier(t *testing.T) {
	t.Parallel()
	g := capability.NewGate()
	if err := g.Authorize("familiar_nudge", []string{"familiar_nudge"}); err != nil {
		t.Errorf("expected allow, got %v", err)
	}
}
