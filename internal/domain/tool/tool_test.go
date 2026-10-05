package tool_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/tool"
)

func TestNewToolValidatesName(t *testing.T) {
	t.Parallel()
	if _, err := tool.New(tool.Params{Name: "", Description: "x", RequiredScope: "mcp:read"}); err == nil {
		t.Fatalf("expected error for empty name, got nil")
	}
	if _, err := tool.New(tool.Params{Name: "atom_search", Description: "", RequiredScope: "mcp:read"}); err == nil {
		t.Fatalf("expected error for empty description, got nil")
	}
	if _, err := tool.New(tool.Params{Name: "atom_search", Description: "x", RequiredScope: ""}); err == nil {
		t.Fatalf("expected error for empty required scope, got nil")
	}
}

func TestNewToolDefaultsInputSchema(t *testing.T) {
	t.Parallel()
	tl, err := tool.New(tool.Params{Name: "atom_search", Description: "x", RequiredScope: "mcp:read"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if tl.InputSchema == nil {
		t.Fatalf("expected default input schema, got nil")
	}
	if tl.InputSchema["type"] != "object" {
		t.Fatalf("expected type=object, got %v", tl.InputSchema["type"])
	}
}

func TestCatalogContainsExpectedTools(t *testing.T) {
	t.Parallel()
	cat := tool.DefaultCatalog()
	got := cat.Names()
	want := []string{
		"atom_search",
		"course_catalog",
		"familiar_nudge",
		"governance_check",
		"learning_path_query",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d tools, got %d (%v)", len(want), len(got), got)
	}
	for _, w := range want {
		if _, ok := cat.Get(w); !ok {
			t.Errorf("missing tool: %s", w)
		}
	}
	// model_broker_invoke is RETIRED (ADR-146) and must NOT be published.
	if _, ok := cat.Get("model_broker_invoke"); ok {
		t.Errorf("model_broker_invoke must be removed from the catalogue (ADR-146 retired)")
	}
}

func TestCatalogFilterByAllowedNames(t *testing.T) {
	t.Parallel()
	cat := tool.DefaultCatalog()
	allowed := cat.FilterByAllowedNames([]string{"course_catalog"})
	if len(allowed) != 1 {
		t.Fatalf("expected exactly 1 tool for allowlist=[course_catalog], got %d (%v)", len(allowed), allowed)
	}
	if allowed[0].Name != "course_catalog" {
		t.Errorf("expected course_catalog, got %s", allowed[0].Name)
	}
	denied := cat.FilterByAllowedNames([]string{"unknown_tool"})
	if len(denied) != 0 {
		t.Errorf("expected no tools for an unknown tool name, got %d", len(denied))
	}
	// A legacy scope-style entry (e.g. "mcp:read") is not a tool NAME, so it
	// matches nothing — the intentional inversion this fix introduces.
	scopeStyle := cat.FilterByAllowedNames([]string{"mcp:read"})
	if len(scopeStyle) != 0 {
		t.Errorf("expected 0 tools for a scope-style allowlist entry, got %d", len(scopeStyle))
	}
}

func TestNewResultRequiresContent(t *testing.T) {
	t.Parallel()
	if _, err := tool.NewResult(nil, []tool.Citation{{Source: "chora.creation", Ref: "atom-1"}}); err == nil {
		t.Fatalf("expected error for empty content")
	}
}

func TestNewResultSetsCitations(t *testing.T) {
	t.Parallel()
	res, err := tool.NewResult(
		[]tool.ContentBlock{{Type: "text", Text: "hello"}},
		[]tool.Citation{{Source: "chora.creation", Ref: "atom-1"}},
	)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if res.IsError {
		t.Errorf("expected isError=false")
	}
	if len(res.Citations) != 1 {
		t.Fatalf("expected 1 citation, got %d", len(res.Citations))
	}
	if res.Citations[0].Source != "chora.creation" {
		t.Errorf("citation source mismatch: %s", res.Citations[0].Source)
	}
}

func TestErrorResultMarksIsError(t *testing.T) {
	t.Parallel()
	res := tool.ErrorResult("boom")
	if !res.IsError {
		t.Errorf("expected isError=true")
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(res.Content))
	}
	if !strings.Contains(res.Content[0].Text, "boom") {
		t.Errorf("expected text to include error msg, got %q", res.Content[0].Text)
	}
}

func TestCitationConfidenceClamped(t *testing.T) {
	t.Parallel()
	c := tool.Citation{Source: "x", Ref: "y", Confidence: floatPtr(1.5)}
	if err := c.Validate(); err == nil {
		t.Errorf("expected error for confidence > 1")
	}
	c.Confidence = floatPtr(-0.1)
	if err := c.Validate(); err == nil {
		t.Errorf("expected error for confidence < 0")
	}
	c.Confidence = floatPtr(0.5)
	if err := c.Validate(); err != nil {
		t.Errorf("expected no error for valid confidence: %v", err)
	}
}

func floatPtr(f float32) *float32 { return &f }

func TestCatalogAllReturnsCopy(t *testing.T) {
	t.Parallel()
	cat := tool.DefaultCatalog()
	all := cat.All()
	if len(all) != 5 {
		t.Fatalf("expected 5, got %d", len(all))
	}
	all[0].Name = "mutated"
	if d, _ := cat.Get("atom_search"); d.Name != "atom_search" {
		t.Errorf("All() must return a defensive copy; original was mutated")
	}
}

func TestCitationValidateRequiresFields(t *testing.T) {
	t.Parallel()
	if err := (tool.Citation{Source: "", Ref: "x"}).Validate(); err == nil {
		t.Errorf("expected source-required error")
	}
	if err := (tool.Citation{Source: "x", Ref: ""}).Validate(); err == nil {
		t.Errorf("expected ref-required error")
	}
}

func TestNewResultRejectsBadCitation(t *testing.T) {
	t.Parallel()
	_, err := tool.NewResult(
		[]tool.ContentBlock{{Type: "text", Text: "x"}},
		[]tool.Citation{{Source: "", Ref: "y"}},
	)
	if err == nil {
		t.Errorf("expected validation error from bad citation")
	}
}
