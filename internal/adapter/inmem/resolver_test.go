package inmem_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-mcp-gateway/internal/adapter/inmem"
	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/auth"
)

func TestStaticResolverRoundTrip(t *testing.T) {
	t.Parallel()
	r := inmem.NewStaticResolver()
	id := auth.Identity{
		AGID:         "01900000-0000-7000-8000-000000000010",
		PartnerName:  "round-trip",
		AllowedTools: []string{"course_catalog"},
		Active:       true,
	}
	r.Register("k1", id)
	got, err := r.ResolveAPIKey("k1")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got.AGID != id.AGID {
		t.Errorf("agid mismatch: %s vs %s", got.AGID, id.AGID)
	}
}

func TestStaticResolverMissingKey(t *testing.T) {
	t.Parallel()
	r := inmem.NewStaticResolver()
	if _, err := r.ResolveAPIKey("missing"); !errors.Is(err, auth.ErrInvalidKey) {
		t.Errorf("expected ErrInvalidKey, got %v", err)
	}
}
