package extension

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinExtensionCatalogHasRecommendedCount(t *testing.T) {
	if got := len(builtinExtensionManifests()); got != 22 {
		t.Fatalf("expected 22 builtin extensions, got %d", got)
	}
}

func TestDiscoverIncludesBuiltinExtensionsWithoutDirectory(t *testing.T) {
	registry := NewRegistry("missing-dir")
	items, err := registry.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(items) != 22 {
		t.Fatalf("expected 22 builtin extensions from discover, got %d", len(items))
	}
}

func TestLoadAllRegistersBuiltinExtensionsWithoutDirectory(t *testing.T) {
	registry := NewRegistry("missing-dir")
	if err := registry.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := len(registry.List()); got != 22 {
		t.Fatalf("expected 22 loaded builtin extensions, got %d", got)
	}
	ext, ok := registry.Get("zoom")
	if !ok {
		t.Fatal("expected zoom builtin extension")
	}
	if !ext.Manifest.Builtin {
		t.Fatal("expected builtin flag on zoom manifest")
	}
}

func TestRegistryReturnsDefensiveCopies(t *testing.T) {
	registry := NewRegistry("missing-dir")
	ext := &Extension{
		Manifest: Manifest{
			ID:       "custom",
			Name:     "Custom",
			Version:  "1.0.0",
			Kind:     "tool",
			Channels: []string{"original"},
			ConfigSchema: map[string]any{
				"properties": map[string]any{
					"token": map[string]any{
						"type": "string",
					},
				},
				"required": []any{"token"},
			},
		},
		Enabled: true,
		Config: map[string]any{
			"mode": "safe",
			"nested": map[string]any{
				"level": "one",
			},
			"steps": []any{"prepare", map[string]any{"name": "run"}},
		},
	}
	if err := registry.Register(ext); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ext.Manifest.Name = "mutated"
	ext.Manifest.ConfigSchema["required"].([]any)[0] = "mutated"
	ext.Config["mode"] = "changed"
	ext.Config["nested"].(map[string]any)["level"] = "mutated"
	ext.Config["steps"].([]any)[1].(map[string]any)["name"] = "mutated"

	got, ok := registry.Get("custom")
	if !ok {
		t.Fatal("expected registered extension")
	}
	got.Manifest.Name = "external mutation"
	got.Manifest.Channels[0] = "changed"
	got.Manifest.ConfigSchema["properties"].(map[string]any)["token"].(map[string]any)["type"] = "integer"
	got.Config["mode"] = "external"
	got.Config["nested"].(map[string]any)["level"] = "external"
	got.Config["steps"].([]any)[1].(map[string]any)["name"] = "external"

	again, ok := registry.Get("custom")
	if !ok {
		t.Fatal("expected registered extension")
	}
	if again.Manifest.Name != "Custom" {
		t.Fatalf("expected stored manifest name to remain Custom, got %q", again.Manifest.Name)
	}
	if again.Manifest.Channels[0] != "original" {
		t.Fatalf("expected stored channel to remain original, got %q", again.Manifest.Channels[0])
	}
	required := again.Manifest.ConfigSchema["required"].([]any)
	if required[0] != "token" {
		t.Fatalf("expected stored schema required value to remain token, got %v", required[0])
	}
	tokenType := again.Manifest.ConfigSchema["properties"].(map[string]any)["token"].(map[string]any)["type"]
	if tokenType != "string" {
		t.Fatalf("expected stored schema token type to remain string, got %v", tokenType)
	}
	if again.Config["mode"] != "safe" {
		t.Fatalf("expected stored config to remain safe, got %v", again.Config["mode"])
	}
	if again.Config["nested"].(map[string]any)["level"] != "one" {
		t.Fatalf("expected nested config to remain one, got %v", again.Config["nested"].(map[string]any)["level"])
	}
	step := again.Config["steps"].([]any)[1].(map[string]any)["name"]
	if step != "run" {
		t.Fatalf("expected nested slice config to remain run, got %v", step)
	}
}

func TestDiscoverRejectsDuplicateManifestID(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, filepath.Join(dir, "zoom"), Manifest{
		ID:      "zoom",
		Name:    "Duplicate Zoom",
		Version: "1.0.0",
		Kind:    "tool",
	})

	registry := NewRegistry(dir)
	if _, err := registry.Discover(); err == nil {
		t.Fatal("expected duplicate manifest ID error")
	}
}

func TestLoadAllUsesManifestDirectoryNotManifestID(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, filepath.Join(dir, "custom-dir"), Manifest{
		ID:         "custom-extension",
		Name:       "Custom Extension",
		Version:    "1.0.0",
		Kind:       "tool",
		Entrypoint: "bin/custom",
	})

	registry := NewRegistry(dir)
	if err := registry.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	ext, ok := registry.Get("custom-extension")
	if !ok {
		t.Fatal("expected custom extension")
	}
	if ext.Path != filepath.Join(dir, "custom-dir") {
		t.Fatalf("expected extension path to use containing directory, got %q", ext.Path)
	}
}

func TestLoadExtensionRejectsUnsafeEntrypoint(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, Manifest{
		ID:         "unsafe",
		Name:       "Unsafe",
		Version:    "1.0.0",
		Kind:       "tool",
		Entrypoint: "../outside",
	})

	if _, err := LoadExtension(dir); err == nil {
		t.Fatal("expected unsafe entrypoint error")
	}
}

func writeManifest(t *testing.T, dir string, manifest Manifest) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
