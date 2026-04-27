package canvas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewStore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	if store == nil {
		t.Fatal("store is nil")
	}
	if store.BaseDir() == "" {
		t.Fatal("base dir is empty")
	}
}

func TestPushAndGet(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	entry, err := store.Push("test-1", "Test Canvas", "<h1>Hello</h1>", EntryTypeHTML, "agent-1")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	if entry.ID != "test-1" {
		t.Fatalf("expected id test-1, got %s", entry.ID)
	}
	if entry.Version != 1 {
		t.Fatalf("expected version 1, got %d", entry.Version)
	}

	got, ok := store.Get("test-1")
	if !ok {
		t.Fatal("entry not found")
	}
	if got.Content != "<h1>Hello</h1>" {
		t.Fatalf("expected content <h1>Hello</h1>, got %s", got.Content)
	}
}

func TestPushUpdatesVersion(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	_, err = store.Push("test-1", "Test", "v1", EntryTypeHTML, "agent-1")
	if err != nil {
		t.Fatalf("first push failed: %v", err)
	}

	entry, err := store.Push("test-1", "Test", "v2", EntryTypeHTML, "agent-1")
	if err != nil {
		t.Fatalf("second push failed: %v", err)
	}
	if entry.Version != 2 {
		t.Fatalf("expected version 2, got %d", entry.Version)
	}
	if entry.Content != "v2" {
		t.Fatalf("expected content v2, got %s", entry.Content)
	}
}

func TestReset(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	_, err = store.Push("test-1", "Test", "content", EntryTypeHTML, "agent-1")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	if err := store.Reset("test-1"); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	entry, ok := store.Get("test-1")
	if !ok {
		t.Fatal("entry not found after reset")
	}
	if entry.Content != "" {
		t.Fatalf("expected empty content, got %s", entry.Content)
	}
	if entry.Version != 2 {
		t.Fatalf("expected version 2 after reset, got %d", entry.Version)
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	_, err = store.Push("test-1", "Test", "content", EntryTypeHTML, "agent-1")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	if err := store.Delete("test-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, ok := store.Get("test-1")
	if ok {
		t.Fatal("entry still exists after delete")
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	_, _ = store.Push("a", "A", "content", EntryTypeHTML, "agent-1")
	_, _ = store.Push("b", "B", "content", EntryTypeHTML, "agent-1")

	items := store.List()
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestVersions(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	_, _ = store.Push("test-1", "Test", "v1", EntryTypeHTML, "agent-1")
	_, _ = store.Push("test-1", "Test", "v2", EntryTypeHTML, "agent-1")
	_, _ = store.Push("test-1", "Test", "v3", EntryTypeHTML, "agent-1")

	versions := store.GetVersions("test-1", 0)
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions (v1 and v2 saved before v3 push), got %d", len(versions))
	}

	v, ok := store.GetVersion("test-1", 2)
	if !ok {
		t.Fatal("version 2 not found")
	}
	if v.Content != "v2" {
		t.Fatalf("expected v2 content, got %s", v.Content)
	}
}

func TestVersionPruning(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 3)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	for i := 1; i <= 5; i++ {
		_, _ = store.Push("test-1", "Test", "v", EntryTypeHTML, "agent-1")
	}

	versions := store.GetVersions("test-1", 0)
	if len(versions) > 3 {
		t.Fatalf("expected at most 3 versions, got %d", len(versions))
	}
}

func TestPushSurfacesVersionPersistenceFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	_, err = store.Push("test-1", "Test", "v1", EntryTypeHTML, "agent-1")
	if err != nil {
		t.Fatalf("initial Push failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.BaseDir(), "versions"), []byte("not-a-dir"), 0o600); err != nil {
		t.Fatalf("create versions path conflict: %v", err)
	}

	if _, err := store.Push("test-1", "Test", "v2", EntryTypeHTML, "agent-1"); err == nil {
		t.Fatal("expected version persistence failure")
	}

	entry, ok := store.Get("test-1")
	if !ok {
		t.Fatal("entry missing after failed update")
	}
	if entry.Content != "v1" {
		t.Fatalf("expected content to remain v1 after failed version save, got %q", entry.Content)
	}
	if entry.Version != 1 {
		t.Fatalf("expected version to remain 1 after failed version save, got %d", entry.Version)
	}
}

func TestResetSurfacesVersionPersistenceFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	_, err = store.Push("test-1", "Test", "content", EntryTypeHTML, "agent-1")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.BaseDir(), "versions"), []byte("not-a-dir"), 0o600); err != nil {
		t.Fatalf("create versions path conflict: %v", err)
	}

	if err := store.Reset("test-1"); err == nil {
		t.Fatal("expected version persistence failure")
	}

	entry, ok := store.Get("test-1")
	if !ok {
		t.Fatal("entry missing after failed reset")
	}
	if entry.Content != "content" {
		t.Fatalf("expected content to remain after failed reset, got %q", entry.Content)
	}
	if entry.Version != 1 {
		t.Fatalf("expected version to remain 1 after failed reset, got %d", entry.Version)
	}
}

func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()

	store1, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	_, _ = store1.Push("persist-1", "Persist", "content", EntryTypeHTML, "agent-1")

	store2, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore reload failed: %v", err)
	}

	entry, ok := store2.Get("persist-1")
	if !ok {
		t.Fatal("entry not found after reload")
	}
	if entry.Content != "content" {
		t.Fatalf("expected content 'content', got %s", entry.Content)
	}
}

func TestA2UIParseAndRender(t *testing.T) {
	content := `{
		"version": "1.0",
		"title": "Test Page",
		"description": "Demo description",
		"theme": "light",
		"theme_vars": {
			"accent": "#0f766e"
		},
		"components": [
			{"type": "heading", "content": "Hello World", "props": {"level": 1}},
			{"type": "text", "content": "This is a test."},
			{"type": "button", "content": "Click Me"},
			{"type": "section", "props": {"title": "Details", "description": "More context"}, "children": [
				{"type": "stack", "props": {"gap": "8px"}, "children": [
					{"type": "textarea", "props": {"rows": "6", "placeholder": "Write here"}, "content": "Draft"},
					{"type": "link", "content": "AnyClaw", "props": {"href": "https://github.com/1024XEngineer/anyclaw"}}
				]}
			]}
		]
	}`

	doc, err := ParseA2UI(content)
	if err != nil {
		t.Fatalf("ParseA2UI failed: %v", err)
	}
	if doc.Title != "Test Page" {
		t.Fatalf("expected title 'Test Page', got %s", doc.Title)
	}
	if len(doc.Components) != 4 {
		t.Fatalf("expected 4 components, got %d", len(doc.Components))
	}

	renderer := NewA2UIRenderer()
	html, err := renderer.Render(doc)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if len(html) == 0 {
		t.Fatal("rendered HTML is empty")
	}
	if !containsAll(html, []string{
		`meta name="description" content="Demo description"`,
		`--accent:#0f766e`,
		`<section class="a2ui-section"`,
		`<textarea rows="6" placeholder="Write here">Draft</textarea>`,
		`<a href="https://github.com/1024XEngineer/anyclaw">AnyClaw</a>`,
	}) {
		t.Fatalf("rendered HTML missing expected upgraded A2UI content:\n%s", html)
	}
}

func TestAutoIDGeneration(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	entry, err := store.Push("", "Auto", "content", EntryTypeHTML, "agent-1")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("auto-generated ID is empty")
	}
}

func TestAutoIDGenerationDoesNotOverwriteSameMillisecondEntries(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		entry, err := store.Push("", "Auto", "content", EntryTypeHTML, "agent-1")
		if err != nil {
			t.Fatalf("Push failed: %v", err)
		}
		if seen[entry.ID] {
			t.Fatalf("duplicate auto-generated ID: %s", entry.ID)
		}
		seen[entry.ID] = true
		if entry.Version != 1 {
			t.Fatalf("expected auto-created entry version 1, got %d", entry.Version)
		}
	}

	items := store.List()
	if len(items) != 100 {
		t.Fatalf("expected 100 independently created entries, got %d", len(items))
	}
}

func TestPathTraversalProtection(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, 5)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	if _, err := store.Push("../escape", "Escape", "content", EntryTypeHTML, "agent-1"); err == nil {
		t.Fatal("expected path traversal id to be rejected")
	}
	if _, err := store.Push(`nested\escape`, "Escape", "content", EntryTypeHTML, "agent-1"); err == nil {
		t.Fatal("expected backslash path id to be rejected")
	}
	if err := store.Delete("../escape"); err == nil {
		t.Fatal("expected invalid delete id to be rejected")
	}
	if err := store.Reset("../escape"); err == nil {
		t.Fatal("expected invalid reset id to be rejected")
	}

	entryDir := filepath.Join(store.BaseDir(), "entries")
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	entries, err := os.ReadDir(entryDir)
	if err != nil {
		t.Fatalf("readdir failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty entries dir, got %d files", len(entries))
	}
}

func TestA2UIEscapesAttributesAndRejectsUnsafeNames(t *testing.T) {
	renderer := NewA2UIRenderer()
	html, err := renderer.Render(&A2UIDocument{
		Title: "Unsafe",
		Components: []A2UIComponent{
			{
				Type:    "input",
				Content: "ignored",
				Props: map[string]any{
					"inputType": `text" autofocus`,
				},
				Attributes: map[string]any{
					`onclick`:       "alert(1)",
					`bad name`:      "bad",
					`data-title`:    `hello "quoted"`,
					`x" onfocus="x`: "bad",
				},
				Styles: map[string]string{
					"color":       `red";background:url(x)`,
					`bad name`:    "bad",
					"--accent":    "#0f766e",
					"font-weight": "700",
				},
			},
			{
				Type: "unknown",
				Props: map[string]any{
					"tag": `script onclick="bad"`,
				},
				Content: `<hello>`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if strings.Contains(html, "bad name") || strings.Contains(html, `x" onfocus`) {
		t.Fatalf("expected unsafe attribute/style names to be skipped:\n%s", html)
	}
	if !strings.Contains(html, `data-title="hello &#34;quoted&#34;"`) {
		t.Fatalf("expected attribute value to be escaped:\n%s", html)
	}
	if strings.Contains(html, `<script`) {
		t.Fatalf("expected unsafe generic tag to fall back to div:\n%s", html)
	}
	if !strings.Contains(html, `&lt;hello&gt;`) {
		t.Fatalf("expected generic content to be escaped:\n%s", html)
	}
}

func TestA2UIValidatesRenderedURISchemes(t *testing.T) {
	renderer := NewA2UIRenderer()
	html, err := renderer.Render(&A2UIDocument{
		Title: "Links",
		Components: []A2UIComponent{
			{
				Type:    "link",
				Content: "Safe HTTP",
				Props: map[string]any{
					"href": "https://example.com/docs",
				},
			},
			{
				Type:    "link",
				Content: "Safe Mail",
				Props: map[string]any{
					"href": "mailto:hello@example.com",
				},
			},
			{
				Type:    "link",
				Content: "Safe Relative",
				Props: map[string]any{
					"href": "/docs/getting-started#intro",
				},
			},
			{
				Type:    "link",
				Content: "Unsafe JS",
				Props: map[string]any{
					"href": "javascript:alert(1)",
				},
			},
			{
				Type:    "link",
				Content: "Unsafe Data",
				Props: map[string]any{
					"href": "data:text/html,<script>alert(1)</script>",
				},
			},
			{
				Type:    "link",
				Content: "Protocol Relative",
				Props: map[string]any{
					"href": "//example.com/path",
				},
			},
			{
				Type: "image",
				Props: map[string]any{
					"src": "https://example.com/image.png",
					"alt": "safe",
				},
			},
			{
				Type: "image",
				Props: map[string]any{
					"src": "./images/local.png",
					"alt": "relative",
				},
			},
			{
				Type: "image",
				Props: map[string]any{
					"src": "javascript:alert(1)",
					"alt": "bad js",
				},
			},
			{
				Type: "image",
				Props: map[string]any{
					"src": "data:text/html,<script>alert(1)</script>",
					"alt": "bad data",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if !containsAll(html, []string{
		`<a href="https://example.com/docs">Safe HTTP</a>`,
		`<a href="mailto:hello@example.com">Safe Mail</a>`,
		`<a href="/docs/getting-started#intro">Safe Relative</a>`,
		`<a href="#">Unsafe JS</a>`,
		`<a href="#">Unsafe Data</a>`,
		`<a href="#">Protocol Relative</a>`,
		`<img src="https://example.com/image.png" alt="safe">`,
		`<img src="./images/local.png" alt="relative">`,
		`<img src="" alt="bad js">`,
		`<img src="" alt="bad data">`,
	}) {
		t.Fatalf("rendered HTML missing expected safe URI output:\n%s", html)
	}
	if strings.Contains(html, "javascript:") || strings.Contains(html, "data:text/html") || strings.Contains(html, "//example.com/path") {
		t.Fatalf("rendered HTML leaked unsafe URI:\n%s", html)
	}
}

func TestA2UIMergesBuiltInAndCallerAttributes(t *testing.T) {
	renderer := NewA2UIRenderer()
	html, err := renderer.Render(&A2UIDocument{
		Title: "Attributes",
		Components: []A2UIComponent{
			{
				Type: "stack",
				Props: map[string]any{
					"className": "custom-stack",
					"gap":       "4px",
				},
				Styles: map[string]string{
					"color": "red",
				},
			},
			{
				Type: "grid",
				Props: map[string]any{
					"className": "custom-grid",
					"columns":   "2",
				},
				Styles: map[string]string{
					"margin": "0",
				},
			},
			{
				Type: "card",
				Props: map[string]any{
					"className": "prop-card",
				},
				Attributes: map[string]any{
					"class": "attr-card",
				},
			},
			{
				Type: "badge",
				Props: map[string]any{
					"className": "custom-badge",
				},
				Content: "Badge",
			},
			{
				Type: "alert",
				Props: map[string]any{
					"className": "custom-alert",
					"level":     "warning",
				},
				Content: "Alert",
			},
		},
	})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	cases := []struct {
		name      string
		needle    string
		wantClass string
		wantStyle bool
	}{
		{name: "stack", needle: "a2ui-stack", wantClass: `class="a2ui-stack custom-stack"`, wantStyle: true},
		{name: "grid", needle: "a2ui-grid", wantClass: `class="a2ui-grid custom-grid"`, wantStyle: true},
		{name: "card", needle: "a2ui-card", wantClass: `class="a2ui-card attr-card prop-card"`},
		{name: "badge", needle: "a2ui-badge", wantClass: `class="a2ui-badge custom-badge"`},
		{name: "alert", needle: "a2ui-alert-warning", wantClass: `class="a2ui-alert a2ui-alert-warning custom-alert"`},
	}

	for _, tc := range cases {
		line := lineContaining(html, tc.needle)
		if line == "" {
			t.Fatalf("%s: expected rendered line containing %q:\n%s", tc.name, tc.needle, html)
		}
		if strings.Count(line, ` class=`) != 1 {
			t.Fatalf("%s: expected one class attribute, got line:\n%s", tc.name, line)
		}
		if !strings.Contains(line, tc.wantClass) {
			t.Fatalf("%s: expected merged class %s, got line:\n%s", tc.name, tc.wantClass, line)
		}
		if tc.wantStyle && strings.Count(line, ` style=`) != 1 {
			t.Fatalf("%s: expected one style attribute, got line:\n%s", tc.name, line)
		}
		if !tc.wantStyle && strings.Count(line, ` style=`) != 0 {
			t.Fatalf("%s: expected no style attribute, got line:\n%s", tc.name, line)
		}
	}
}

func containsAll(value string, snippets []string) bool {
	for _, snippet := range snippets {
		if !strings.Contains(value, snippet) {
			return false
		}
	}
	return true
}

func lineContaining(value string, needle string) string {
	for _, line := range strings.Split(value, "\n") {
		if strings.Contains(line, "<") && strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
