package clients_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-mcp-gateway/internal/adapter/clients"
)

func TestToolUpstreamMaps(t *testing.T) {
	t.Parallel()
	cases := map[string]clients.EnvKey{
		"atom_search":         clients.EnvCreation,
		"course_catalog":      clients.EnvDelivery,
		"familiar_nudge":      clients.EnvConsumption,
		"learning_path_query": clients.EnvConsumption,
		"governance_check":    clients.EnvGovernance,
	}
	for tool, want := range cases {
		got, err := clients.ToolUpstream(tool)
		if err != nil {
			t.Errorf("%s: unexpected err: %v", tool, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %s want %s", tool, got, want)
		}
	}
}

func TestToolUpstreamUnknownReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := clients.ToolUpstream("nope"); err == nil {
		t.Errorf("expected error for unknown tool")
	}
}

func TestRegistryLookupMissingReturnsErrUpstreamNotConfigured(t *testing.T) {
	// Note: cannot use t.Parallel() — t.Setenv mutates process env.
	t.Setenv(string(clients.EnvCreation), "")
	t.Setenv(string(clients.EnvDelivery), "")
	t.Setenv(string(clients.EnvConsumption), "")
	t.Setenv(string(clients.EnvGovernance), "")
	r := clients.NewRegistry()
	if _, err := r.Lookup(clients.EnvCreation); !errors.Is(err, clients.ErrUpstreamNotConfigured) {
		t.Errorf("expected ErrUpstreamNotConfigured, got %v", err)
	}
	if c := r.HTTPClient(clients.EnvCreation); c != nil {
		t.Errorf("expected nil client for unconfigured upstream")
	}
}

func TestRegistryLookupConfigured(t *testing.T) {
	// Note: cannot use t.Parallel() — t.Setenv mutates process env.
	t.Setenv(string(clients.EnvCreation), "https://creation.example")
	r := clients.NewRegistry()
	url, err := r.Lookup(clients.EnvCreation)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if url != "https://creation.example" {
		t.Errorf("got %s", url)
	}
	if c := r.HTTPClient(clients.EnvCreation); c == nil {
		t.Errorf("expected non-nil http.Client for configured upstream")
	}
}
