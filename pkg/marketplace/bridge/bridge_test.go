package bridge

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1024XEngineer/anyclaw/pkg/marketplace"
	marketregistry "github.com/1024XEngineer/anyclaw/pkg/marketplace/registry"
)

func TestBridgeSearchInstallAndBind(t *testing.T) {
	archive := bridgeArchive(t, "cloud.skill.release-notes", marketplace.ArtifactKindSkill, "1.0.0")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/artifacts":
			writeBridgeJSON(t, w, map[string]any{"data": map[string]any{
				"items": []map[string]any{{
					"id":             "cloud.skill.release-notes",
					"kind":           "skill",
					"name":           "Release Notes",
					"summary":        "Writes notes.",
					"latest_version": "1.0.0",
					"source":         "anyclaw-cloud",
				}},
				"total": 1,
			}})
		case strings.HasSuffix(r.URL.Path, "/resolve"):
			writeBridgeJSON(t, w, map[string]any{"data": map[string]any{
				"artifact_id":     "cloud.skill.release-notes",
				"version":         "1.0.0",
				"download_url":    "http://" + r.Host + "/v1/download/cloud.skill.release-notes/1.0.0",
				"checksum_sha256": bridgeSHA256(archive),
				"size_bytes":      len(archive),
				"risk_level":      "low",
				"trust_level":     "verified",
				"permissions":     []string{"fs.read"},
				"kind":            "skill",
				"name":            "Release Notes",
			}})
		case strings.Contains(r.URL.Path, "/v1/download/"):
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := marketplace.NewStore(t.TempDir())
	b := New(Options{
		Store:            store,
		Registry:         marketregistry.NewClient(marketregistry.ClientConfig{Endpoint: server.URL}),
		AutoInstallSkill: true,
	})
	search, err := b.Search(context.Background(), SearchRequest{Query: "release", Kind: marketplace.ArtifactKindSkill, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Cloud) != 1 || search.Cloud[0].ID != "cloud.skill.release-notes" {
		t.Fatalf("unexpected cloud search: %#v", search.Cloud)
	}
	install, err := b.Install(context.Background(), marketplace.InstallRequest{ArtifactID: "cloud.skill.release-notes", InstalledBy: "agent", UserConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if install.Job == nil || install.Job.State != marketplace.JobSucceeded {
		t.Fatalf("unexpected install result: %#v", install)
	}
	binding, err := b.Bind(context.Background(), marketplace.BindingRequest{ArtifactID: "cloud.skill.release-notes", TargetType: marketplace.TargetRuntimeGlobal})
	if err != nil {
		t.Fatal(err)
	}
	if binding.ArtifactID != "cloud.skill.release-notes" {
		t.Fatalf("unexpected binding: %#v", binding)
	}
}

func bridgeArchive(t *testing.T, id string, kind marketplace.ArtifactKind, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	w, err := writer.Create("anyclaw.artifact.json")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"id": id, "kind": kind, "name": id, "version": version})
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func bridgeSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeBridgeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatal(err)
	}
}
