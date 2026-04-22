package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

type hostRewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if clone.URL.Host == "skills.sh" || clone.URL.Host == "raw.githubusercontent.com" {
		clone.URL.Scheme = t.target.Scheme
		clone.URL.Host = t.target.Host
		clone.Host = t.target.Host
	}
	return t.base.RoundTrip(clone)
}

func TestSkillsHubRemoteFlows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/search":
			_, _ = w.Write([]byte(`{"skills":[{"name":"weather","full_name":"Weather Helper","description":"Forecasts","url":"https://skills.sh/weather","version":"2.0.0","permissions":["net"],"entrypoint":"run.py"}]}`))
		case "/api/skills/acme/toolbox/weather":
			_, _ = w.Write([]byte(`{"name":"weather","full_name":"Weather Helper","description":"Forecasts","summary":"Daily weather","version":"2.0.0","permissions":["net"],"entrypoint":"run.py","registry":"skills.sh","homepage":"https://skills.sh/weather"}`))
		case "/acme/toolbox/main/skills/weather/SKILL.md":
			http.NotFound(w, r)
		case "/acme/toolbox/master/skills/weather/SKILL.md":
			_, _ = w.Write([]byte("**Weather Helper**\n- fetch weather\nAnswer clearly.\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	targetURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	originalTransport := http.DefaultTransport
	http.DefaultTransport = hostRewriteTransport{target: targetURL, base: originalTransport}
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})

	results, err := SearchSkills(context.Background(), "weather", 0)
	if err != nil {
		t.Fatalf("SearchSkills: %v", err)
	}
	if len(results) != 1 || results[0].Name != "weather" {
		t.Fatalf("unexpected search results: %#v", results)
	}

	catalog, err := SearchCatalog(context.Background(), "weather", 0)
	if err != nil {
		t.Fatalf("SearchCatalog: %v", err)
	}
	if len(catalog) != 1 || catalog[0].InstallHint != "anyclaw skill install weather" {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}

	detail, err := GetSkillDetail(context.Background(), "acme", "toolbox", "weather")
	if err != nil {
		t.Fatalf("GetSkillDetail: %v", err)
	}
	if detail.Entrypoint != "run.py" {
		t.Fatalf("unexpected detail: %#v", detail)
	}

	md, err := GetSkillMarkdown(context.Background(), "acme", "toolbox", "weather")
	if err != nil {
		t.Fatalf("GetSkillMarkdown: %v", err)
	}
	if md == "" {
		t.Fatal("expected markdown content")
	}

	destDir := t.TempDir()
	if err := InstallSkillFromGitHub(context.Background(), "acme", "toolbox", "weather", destDir); err != nil {
		t.Fatalf("InstallSkillFromGitHub: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "weather", "skill.json")); err != nil {
		t.Fatalf("expected installed skill.json: %v", err)
	}
}

func TestGetSkillMarkdownAndRemoteErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/search" {
			http.Error(w, "bad", http.StatusBadGateway)
			return
		}
		http.Error(w, "bad", http.StatusInternalServerError)
	}))
	defer server.Close()

	targetURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	originalTransport := http.DefaultTransport
	http.DefaultTransport = hostRewriteTransport{target: targetURL, base: originalTransport}
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})

	if _, err := SearchSkills(context.Background(), "weather", 5); err == nil {
		t.Fatal("expected SearchSkills error")
	}
	if _, err := GetSkillMarkdown(context.Background(), "acme", "toolbox", "weather"); err == nil {
		t.Fatal("expected GetSkillMarkdown non-404 error")
	}
}
