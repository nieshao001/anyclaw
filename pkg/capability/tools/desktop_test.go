package tools

import "testing"

func TestNormalizeDesktopOpenKindInfersURLFileAndApp(t *testing.T) {
	tests := []struct {
		name   string
		target string
		kind   string
		want   string
	}{
		{name: "explicit url", target: "snake_game.html", kind: "url", want: "url"},
		{name: "url", target: "https://example.com", want: "url"},
		{name: "html file", target: "snake_game.html", want: "file"},
		{name: "path file", target: `workflows\default\snake_game.html`, want: "file"},
		{name: "app", target: "notepad", want: "app"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeDesktopOpenKind(tt.target, tt.kind); got != tt.want {
				t.Fatalf("normalizeDesktopOpenKind(%q, %q) = %q, want %q", tt.target, tt.kind, got, tt.want)
			}
		})
	}
}

func TestDesktopOpenResultMatchesKind(t *testing.T) {
	tests := map[string]string{
		"url":  "opened url",
		"file": "opened file",
		"app":  "started app",
		"":     "opened target",
	}
	for kind, want := range tests {
		if got := desktopOpenResult(kind); got != want {
			t.Fatalf("desktopOpenResult(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestDesktopOpenRejectsSeparatorOnlyTargets(t *testing.T) {
	for _, target := range []string{`\`, `\\`, `/`, `//`} {
		t.Run(target, func(t *testing.T) {
			if !isInvalidDesktopOpenTarget(target) {
				t.Fatalf("expected %q to be rejected", target)
			}
			if looksLikeDesktopOpenFile(target) {
				t.Fatalf("did not expect %q to look like a file", target)
			}
		})
	}
}
