package auth_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/auth"
)

type stubResolver struct {
	keys map[string]auth.Identity
}

func (s *stubResolver) ResolveAPIKey(key string) (auth.Identity, error) {
	id, ok := s.keys[key]
	if !ok {
		return auth.Identity{}, auth.ErrInvalidKey
	}
	return id, nil
}

func TestAuthenticatorRejectsEmptyKey(t *testing.T) {
	t.Parallel()
	a := auth.NewAuthenticator(&stubResolver{keys: map[string]auth.Identity{}})
	if _, err := a.Authenticate(""); !errors.Is(err, auth.ErrMissingKey) {
		t.Fatalf("expected ErrMissingKey, got %v", err)
	}
}

func TestAuthenticatorRejectsUnknownKey(t *testing.T) {
	t.Parallel()
	a := auth.NewAuthenticator(&stubResolver{keys: map[string]auth.Identity{}})
	if _, err := a.Authenticate("nope"); !errors.Is(err, auth.ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

func TestAuthenticatorReturnsIdentity(t *testing.T) {
	t.Parallel()
	id := auth.Identity{
		AGID:         "01900000-0000-7000-8000-000000000001",
		PartnerName:  "Test Co",
		AllowedTools: []string{"course_catalog"},
		Active:       true,
	}
	a := auth.NewAuthenticator(&stubResolver{keys: map[string]auth.Identity{"k1": id}})
	got, err := a.Authenticate("k1")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got.AGID != id.AGID {
		t.Errorf("agid mismatch: %s vs %s", got.AGID, id.AGID)
	}
	if got.HasTool("course_catalog") != true {
		t.Errorf("expected HasTool(course_catalog)=true")
	}
	if got.HasTool("familiar_nudge") != false {
		t.Errorf("expected HasTool(familiar_nudge)=false")
	}
}

func TestAuthenticatorRejectsInactivePartner(t *testing.T) {
	t.Parallel()
	id := auth.Identity{
		AGID:         "01900000-0000-7000-8000-000000000002",
		PartnerName:  "Inactive Co",
		AllowedTools: []string{"course_catalog"},
		Active:       false,
	}
	a := auth.NewAuthenticator(&stubResolver{keys: map[string]auth.Identity{"k2": id}})
	if _, err := a.Authenticate("k2"); !errors.Is(err, auth.ErrPartnerInactive) {
		t.Fatalf("expected ErrPartnerInactive, got %v", err)
	}
}

func TestIdentityRejectsGCID(t *testing.T) {
	t.Parallel()
	// Compile-time assertion: Identity intentionally has no Gcid field.
	// We assert by interface — if a Gcid field were ever added, the
	// reflective check would fail at runtime. This guards CLAUDE.md §1
	// and ddd-enforcement.md #10 (AGID distinct from GCID; agents
	// CANNOT hold TenantMembership/GCID).
	id := auth.Identity{}
	if !auth.IdentityHasNoGCID(id) {
		t.Errorf("Identity must NOT carry a GCID — agents are AGID-only")
	}
}
