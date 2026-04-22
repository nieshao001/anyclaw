package tools

import "testing"

func TestRegisterBuiltinsRegistersExpectedTools(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry, BuiltinOptions{WorkingDir: t.TempDir()})

	expected := []string{
		"read_file",
		"write_file",
		"memory_search",
		"web_search",
		"fetch_url",
		"desktop_open",
		"desktop_screenshot",
	}
	for _, name := range expected {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("expected builtin tool %q to be registered", name)
		}
	}
}
