// Package tool defines the MCP tool catalogue exposed by the
// chora-mcp-gateway service.
//
// MCP tools are the externally callable capabilities partners may invoke
// via JSON-RPC 2.0 (`tools/list` + `tools/call`). Each tool declares a
// `RequiredScope`, which is INFORMATIONAL metadata surfaced in
// tools/list only — it is NOT the authorization key. Access is gated by
// tool-NAME membership in the partner's allowed-tool list
// (auth.Identity.AllowedTools, resolved by the auth package via the
// partner registry maintained in chora-a2a-gateway). See
// internal/domain/capability for the Gate that enforces this.
//
// Per IMDA D4 Transparency dimension, all tool results carry a
// `Citations` array — calls with no upstream citation are rejected by
// the adapter layer before serialisation.
//
// Five tools ship today (model_broker_invoke was removed — see below):
//
//   - atom_search           — search published learning atoms
//   - course_catalog        — list public Course/Class catalogue
//   - familiar_nudge        — emit a Familiar nudge to a learner
//   - governance_check      — invoke the Governance Gatekeeper screen
//   - learning_path_query   — fetch a learner's path progress
//
// model_broker_invoke was RETIRED (ADR-146): it proxied the deleted
// chora-model-broker-router. The single LLM chokepoint is now
// chora-model-gateway, which is gRPC-only (ADR-163) with no HTTP invoke
// route reachable over the MCP gateway's HTTP-client seam, so the tool was
// removed rather than repointed at a dead router.
package tool

import (
	"errors"
	"fmt"
	"strings"
)

// Definition describes an MCP tool published in /mcp/tools/list.
type Definition struct {
	Name          string
	Description   string
	InputSchema   map[string]any
	RequiredScope string
}

// Params is the input shape for New.
type Params struct {
	Name          string
	Description   string
	InputSchema   map[string]any
	RequiredScope string
}

// New constructs a tool definition enforcing required fields.
func New(p Params) (Definition, error) {
	if strings.TrimSpace(p.Name) == "" {
		return Definition{}, errors.New("tool: name required")
	}
	if strings.TrimSpace(p.Description) == "" {
		return Definition{}, errors.New("tool: description required")
	}
	if strings.TrimSpace(p.RequiredScope) == "" {
		return Definition{}, errors.New("tool: required scope must be set")
	}
	schema := p.InputSchema
	if schema == nil {
		schema = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	return Definition{
		Name:          p.Name,
		Description:   p.Description,
		InputSchema:   schema,
		RequiredScope: p.RequiredScope,
	}, nil
}

// ContentBlock is a single MCP content block (text-only in M11).
type ContentBlock struct {
	Type string
	Text string
}

// Citation traces a result element back to its upstream source per IMDA
// D4 Transparency. Every Result MUST carry at least one citation.
type Citation struct {
	Source     string
	Ref        string
	Confidence *float32
}

// Validate ensures Citation fields are well-formed.
func (c Citation) Validate() error {
	if strings.TrimSpace(c.Source) == "" {
		return errors.New("citation: source required")
	}
	if strings.TrimSpace(c.Ref) == "" {
		return errors.New("citation: ref required")
	}
	if c.Confidence != nil {
		v := *c.Confidence
		if v < 0 || v > 1 {
			return fmt.Errorf("citation: confidence must be in [0,1], got %f", v)
		}
	}
	return nil
}

// Result is the payload returned by tools/call.
type Result struct {
	Content   []ContentBlock
	Citations []Citation
	IsError   bool
}

// NewResult wraps tool output with mandatory citations.
func NewResult(content []ContentBlock, citations []Citation) (Result, error) {
	if len(content) == 0 {
		return Result{}, errors.New("result: at least one content block required")
	}
	for _, c := range citations {
		if err := c.Validate(); err != nil {
			return Result{}, err
		}
	}
	cp := make([]Citation, len(citations))
	copy(cp, citations)
	return Result{Content: content, Citations: cp, IsError: false}, nil
}

// ErrorResult builds a non-citation MCP error payload.
func ErrorResult(msg string) Result {
	return Result{
		Content:   []ContentBlock{{Type: "text", Text: "error: " + msg}},
		Citations: []Citation{},
		IsError:   true,
	}
}

// Catalog is an immutable lookup over the published tool set.
type Catalog struct {
	tools []Definition
	byKey map[string]Definition
}

// NewCatalog builds a Catalog from definitions.
func NewCatalog(defs []Definition) Catalog {
	cp := make([]Definition, len(defs))
	copy(cp, defs)
	idx := make(map[string]Definition, len(defs))
	for _, d := range cp {
		idx[d.Name] = d
	}
	return Catalog{tools: cp, byKey: idx}
}

// Names returns the registered tool names in catalogue order.
func (c Catalog) Names() []string {
	out := make([]string, len(c.tools))
	for i, t := range c.tools {
		out[i] = t.Name
	}
	return out
}

// Get returns a definition by name (and a presence flag).
func (c Catalog) Get(name string) (Definition, bool) {
	d, ok := c.byKey[name]
	return d, ok
}

// All returns all definitions (defensive copy).
func (c Catalog) All() []Definition {
	out := make([]Definition, len(c.tools))
	copy(out, c.tools)
	return out
}

// FilterByAllowedNames returns the subset of tools whose Name is present
// in `allowed` — the partner's allowed-tool-name list. RequiredScope is
// informational metadata and is NOT consulted here.
func (c Catalog) FilterByAllowedNames(allowed []string) []Definition {
	set := make(map[string]struct{}, len(allowed))
	for _, n := range allowed {
		set[n] = struct{}{}
	}
	out := make([]Definition, 0, len(c.tools))
	for _, t := range c.tools {
		if _, ok := set[t.Name]; ok {
			out = append(out, t)
		}
	}
	return out
}

// DefaultCatalog returns the published MCP tool set. (model_broker_invoke
// was removed — ADR-146; see the package doc.)
func DefaultCatalog() Catalog {
	mk := func(name, desc, scope string) Definition {
		d, _ := New(Params{Name: name, Description: desc, RequiredScope: scope})
		return d
	}
	return NewCatalog([]Definition{
		mk("atom_search", "Search published learning atoms by query/topic.", "mcp:read"),
		mk("course_catalog", "List public Course/Class catalogue entries.", "mcp:read"),
		mk("familiar_nudge", "Emit a Familiar nudge to a learner.", "mcp:write"),
		mk("governance_check", "Invoke the Governance Gatekeeper screen on input.", "mcp:read"),
		mk("learning_path_query", "Fetch a learner's path progress.", "mcp:read"),
	})
}
