package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdvanceBootstrapRitualIsDisabledAndClearsLegacyState(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, ".anyclaw-bootstrap-state.json")
	if err := os.WriteFile(statePath, []byte(`{"step":1}`), 0o644); err != nil {
		t.Fatalf("WriteFile(state): %v", err)
	}

	result, err := AdvanceBootstrapRitual(dir, "please help me", BootstrapRitualOptions{
		AgentName:        "assistant",
		AgentDescription: "Execution helper",
	})
	if err != nil {
		t.Fatalf("AdvanceBootstrapRitual: %v", err)
	}
	if result == nil || result.Active || result.Completed || result.Response != "" {
		t.Fatalf("expected deleted bootstrap ritual to be inactive, got %#v", result)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("expected bootstrap state to be removed, stat err=%v", err)
	}
}

func TestBootstrapPendingIsAlwaysFalseAfterRitualRemoval(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".anyclaw-bootstrap-state.json"), []byte(`{"step":1}`), 0o644); err != nil {
		t.Fatalf("WriteFile(state): %v", err)
	}
	if BootstrapPending(dir) {
		t.Fatal("expected bootstrap ritual to stay disabled")
	}
}
