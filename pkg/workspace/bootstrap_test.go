package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureBootstrapCreatesOnlyAgentsFile(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureBootstrap(dir, BootstrapOptions{
		AgentName:        "assistant",
		AgentDescription: "Execution helper",
	}); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	agentsData, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("ReadFile(AGENTS.md): %v", err)
	}
	if !strings.Contains(string(agentsData), "inspect -> act -> inspect -> adapt -> verify") {
		t.Fatalf("expected AGENTS.md to describe the execution loop, got %q", string(agentsData))
	}

	for _, name := range []string{"SOUL.md", "TOOLS.md", "IDENTITY.md", "USER.md", "HEARTBEAT.md", "BOOTSTRAP.md", "MEMORY.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("did not expect legacy bootstrap file %s, stat err=%v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "memory")); !os.IsNotExist(err) {
		t.Fatalf("did not expect workspace memory directory, stat err=%v", err)
	}
}

func TestEnsureBootstrapDoesNotOverwriteExistingAgentsFile(t *testing.T) {
	dir := t.TempDir()
	custom := "# AGENTS\n\nKeep this value."
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(custom), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := EnsureBootstrap(dir, BootstrapOptions{AgentName: "assistant"}); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != custom {
		t.Fatalf("expected existing AGENTS.md to be preserved, got %q", string(data))
	}
}

func TestEnsureBootstrapRemovesLegacyBootstrapAndMemoryFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range legacyBootstrapFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("legacy"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "memory"), 0o755); err != nil {
		t.Fatalf("MkdirAll(memory): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory", "2026-05-06.md"), []byte("legacy memory"), 0o644); err != nil {
		t.Fatalf("WriteFile(memory): %v", err)
	}

	if err := EnsureBootstrap(dir, BootstrapOptions{AgentName: "assistant"}); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	for _, name := range legacyBootstrapFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("expected legacy file %s to be removed, stat err=%v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "memory")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy memory directory to be removed, stat err=%v", err)
	}
}

func TestLoadBootstrapFilesReadsOnlyAgentsFile(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureBootstrap(dir, BootstrapOptions{AgentName: "assistant"}); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	files, err := LoadBootstrapFiles(dir, BootstrapOptions{})
	if err != nil {
		t.Fatalf("LoadBootstrapFiles: %v", err)
	}
	if len(files) != 1 || files[0].Name != "AGENTS.md" || files[0].Missing {
		t.Fatalf("expected only AGENTS.md, got %#v", files)
	}
}
