// Package inmem provides in-memory adapters for the M11 skeleton.
//
// Real implementations land in M12:
//   - StaticResolver → gRPC client to chora-a2a-gateway/partners
//   - RateLimiter    → token-bucket backed by a shared store
//   - EventPublisher → NATS JetStream publisher via chora-common/eventbus
package inmem

import (
	"sync"

	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/auth"
)

// StaticResolver resolves API keys from an in-memory map. Registered
// partners live for the process lifetime.
type StaticResolver struct {
	mu   sync.RWMutex
	keys map[string]auth.Identity
}

// NewStaticResolver constructs an empty resolver.
func NewStaticResolver() *StaticResolver {
	return &StaticResolver{keys: map[string]auth.Identity{}}
}

// Register associates a key with an identity (idempotent; last write wins).
func (s *StaticResolver) Register(key string, id auth.Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[key] = id
}

// ResolveAPIKey implements auth.Resolver.
func (s *StaticResolver) ResolveAPIKey(key string) (auth.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.keys[key]
	if !ok {
		return auth.Identity{}, auth.ErrInvalidKey
	}
	return id, nil
}
