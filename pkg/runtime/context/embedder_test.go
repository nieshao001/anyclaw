package context

import (
	"testing"
	"time"

	"github.com/1024XEngineer/anyclaw/pkg/state/policy/secrets"
)

func TestResolveSecretUsesStrictTemplateResolution(t *testing.T) {
	snap := testRuntimeSnapshot(map[string]string{
		"llm_api_key": "llm-key",
	})

	got := resolveSecret(snap, "Bearer ${SECRET:llm_api_key}", "llm_api_key")
	if got != "Bearer llm-key" {
		t.Fatalf("expected resolved template, got %q", got)
	}
}

func TestResolveSecretFallsBackToNamedSecretOnMissingReference(t *testing.T) {
	snap := testRuntimeSnapshot(map[string]string{
		"llm_api_key": "llm-key",
	})

	got := resolveSecret(snap, "${SECRET:missing_key}", "llm_api_key")
	if got != "llm-key" {
		t.Fatalf("expected fallback secret value, got %q", got)
	}
}

func TestResolveSecretPreservesPlaintextWhenNothingResolves(t *testing.T) {
	snap := testRuntimeSnapshot(map[string]string{
		"another_key": "another-value",
	})

	got := resolveSecret(snap, "${SECRET:missing_key}", "llm_api_key")
	if got != "${SECRET:missing_key}" {
		t.Fatalf("expected unresolved placeholder to remain unchanged, got %q", got)
	}
}

func testRuntimeSnapshot(values map[string]string) *secrets.RuntimeSnapshot {
	now := time.Now().UTC()
	entries := make(map[string]*secrets.SecretEntry, len(values))
	for key, value := range values {
		entries[key] = &secrets.SecretEntry{
			ID:        key + "-id",
			Key:       key,
			Value:     value,
			Scope:     secrets.ScopeGlobal,
			Source:    secrets.SourceManual,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	return secrets.NewRuntimeSnapshot(entries, "test")
}
