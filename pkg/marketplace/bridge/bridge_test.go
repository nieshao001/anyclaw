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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/1024XEngineer/anyclaw/pkg/config"
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

func TestBridgeCatalogCloudStatusAndInstallPlanning(t *testing.T) {
	archive := bridgeArchive(t, "cloud.skill.release-notes", marketplace.ArtifactKindSkill, "1.0.0")
	server := bridgeRegistryServer(t, archive)
	defer server.Close()

	store := marketplace.NewStore(t.TempDir())
	if err := store.SaveReceipt(&marketplace.InstallReceipt{
		ID:            "cloud.skill.release-notes@0.9.0",
		ArtifactID:    "cloud.skill.release-notes",
		Kind:          marketplace.ArtifactKindSkill,
		Name:          "Release Notes",
		Version:       "0.9.0",
		Source:        marketplace.SourceCloud,
		InstalledPath: filepath.Join(t.TempDir(), "installed"),
		InstalledBy:   "user",
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agent.Name = "Main"
	localCatalog := marketplace.NewLocalCatalog(marketplace.LocalCatalogDeps{Config: cfg})
	b := New(Options{
		Store:            store,
		Registry:         marketregistry.NewClient(marketregistry.ClientConfig{Endpoint: server.URL}),
		LocalCatalog:     localCatalog,
		AutoInstallSkill: true,
	})

	local, err := b.List(context.Background(), marketplace.Filter{Source: marketplace.SourceLocal})
	if err != nil {
		t.Fatal(err)
	}
	if local.Result.Total == 0 || local.Result.Items[0].Source != marketplace.SourceLocal {
		t.Fatalf("unexpected local list: %#v", local.Result)
	}
	cloud, err := b.List(context.Background(), marketplace.Filter{Source: marketplace.SourceCloud, Status: marketplace.StatusInstalled})
	if err != nil {
		t.Fatal(err)
	}
	if cloud.CloudErr != "" || cloud.Result.Total != 1 || cloud.Result.Items[0].Status != marketplace.StatusInstalled {
		t.Fatalf("unexpected cloud list overlay: %#v cloudErr=%q", cloud.Result, cloud.CloudErr)
	}
	artifact, err := b.Get(context.Background(), "cloud.skill.release-notes", marketplace.SourceCloud)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != marketplace.StatusInstalled {
		t.Fatalf("expected installed overlay, got %#v", artifact)
	}
	versions, err := b.Versions(context.Background(), "cloud.skill.release-notes", marketplace.SourceCloud)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Version != "1.0.0" {
		t.Fatalf("unexpected versions: %#v", versions)
	}
	plan, err := b.PlanInstall(context.Background(), marketplace.InstallRequest{ArtifactID: " cloud.skill.release-notes ", UserConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Request.ArtifactID != "cloud.skill.release-notes" || plan.Decision.Decision != marketplace.DecisionAuto {
		t.Fatalf("unexpected install plan: %#v", plan)
	}
	if _, err := b.PlanInstall(context.Background(), marketplace.InstallRequest{}); err == nil {
		t.Fatal("expected missing artifact_id error")
	}
}

func TestBridgeJobsBindingsEventsAndUninstallHooks(t *testing.T) {
	archive := bridgeArchive(t, "cloud.skill.release-notes", marketplace.ArtifactKindSkill, "1.0.0")
	server := bridgeRegistryServer(t, archive)
	defer server.Close()

	store := marketplace.NewStore(t.TempDir())
	installedPath := filepath.Join(store.InstalledDir(), "skill", "cloud-skill-release-notes", "1-0-0")
	if err := os.MkdirAll(installedPath, 0o755); err != nil {
		t.Fatal(err)
	}
	receipt := &marketplace.InstallReceipt{
		ID:            "cloud.skill.release-notes@1.0.0",
		ArtifactID:    "cloud.skill.release-notes",
		Kind:          marketplace.ArtifactKindSkill,
		Name:          "Release Notes",
		Version:       "1.0.0",
		Source:        marketplace.SourceCloud,
		InstalledPath: installedPath,
		InstalledBy:   "user",
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := store.SaveReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	var bindHookCount int
	var beforeUninstallReceipt string
	var afterUninstallReceipt string
	b := New(Options{
		Store:    store,
		Registry: marketregistry.NewClient(marketregistry.ClientConfig{Endpoint: server.URL}),
		AfterBind: func(ctx context.Context, binding *marketplace.Binding) error {
			bindHookCount++
			return nil
		},
		BeforeUninstall: func(ctx context.Context, receipt *marketplace.InstallReceipt) error {
			beforeUninstallReceipt = receipt.ID
			return nil
		},
		AfterUninstall: func(ctx context.Context, result *marketplace.UninstallResult) error {
			afterUninstallReceipt = result.ReceiptID
			return nil
		},
	})

	upgrade, err := b.StartUpgrade(context.Background(), marketplace.UpgradeRequest{ArtifactID: receipt.ArtifactID, InstalledBy: "tester", IdempotencyKey: "upgrade-1"})
	if err != nil {
		t.Fatal(err)
	}
	if upgrade.Job == nil || upgrade.Job.Type != "upgrade" || upgrade.Job.Metadata["previous_version"] != "1.0.0" {
		t.Fatalf("unexpected upgrade job: %#v", upgrade.Job)
	}
	jobs, err := b.ListJobs(10)
	if err != nil || jobs.Total == 0 {
		t.Fatalf("jobs = %#v err=%v", jobs, err)
	}
	if _, err := b.GetJob(upgrade.Job.ID); err != nil {
		t.Fatal(err)
	}
	binding, err := b.Bind(context.Background(), marketplace.BindingRequest{ArtifactID: receipt.ArtifactID, TargetType: marketplace.TargetRuntimeGlobal})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := b.DeleteBinding(context.Background(), binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted == nil || deleted.ID != binding.ID || bindHookCount != 2 {
		t.Fatalf("delete binding hook mismatch deleted=%#v count=%d", deleted, bindHookCount)
	}
	events, err := b.ListEvents(20)
	if err != nil || events.Total == 0 {
		t.Fatalf("events = %#v err=%v", events, err)
	}
	result, err := b.Uninstall(context.Background(), marketplace.UninstallRequest{ReceiptID: receipt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReceiptID != receipt.ID || beforeUninstallReceipt != receipt.ID || afterUninstallReceipt != receipt.ID {
		t.Fatalf("uninstall hooks/result mismatch result=%#v before=%q after=%q", result, beforeUninstallReceipt, afterUninstallReceipt)
	}
	if _, err := os.Stat(installedPath); !os.IsNotExist(err) {
		t.Fatalf("installed path err=%v, want not exist", err)
	}
}

func TestBridgeConfigurationErrorsAndCloudDegrade(t *testing.T) {
	if _, err := (*DefaultBridge)(nil).Search(context.Background(), SearchRequest{}); err == nil {
		t.Fatal("expected nil bridge search error")
	}
	if _, err := New(Options{}).List(context.Background(), marketplace.Filter{}); err == nil {
		t.Fatal("expected missing local catalog error")
	}
	if _, err := New(Options{}).Get(context.Background(), "", marketplace.SourceLocal); err != marketplace.ErrArtifactNotFound {
		t.Fatalf("expected ErrArtifactNotFound, got %v", err)
	}
	cloud, err := New(Options{}).List(context.Background(), marketplace.Filter{Source: marketplace.SourceCloud, Limit: -1, Offset: -3})
	if err != nil {
		t.Fatal(err)
	}
	if cloud.CloudErr == "" || cloud.Result.Limit != 50 || cloud.Result.Offset != 0 {
		t.Fatalf("unexpected degraded cloud list: %#v err=%q", cloud.Result, cloud.CloudErr)
	}
	if _, err := New(Options{}).Resolve(context.Background(), "x", ""); err != marketregistry.ErrNotConfigured {
		t.Fatalf("expected registry config error, got %v", err)
	}
	if _, err := New(Options{Store: marketplace.NewStore(t.TempDir())}).StartInstall(context.Background(), marketplace.InstallRequest{ArtifactID: "x"}); err == nil {
		t.Fatal("expected start install config error")
	}
	if _, err := New(Options{Store: marketplace.NewStore(t.TempDir())}).ExecuteJob(context.Background(), "missing"); err == nil {
		t.Fatal("expected execute job config error")
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

func bridgeRegistryServer(t *testing.T, archive []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
					"risk_level":     "low",
					"trust_level":    "verified",
					"permissions":    []string{"fs.read"},
				}},
				"total": 1,
				"limit": 5,
			}})
		case r.URL.Path == "/v1/artifacts/cloud.skill.release-notes":
			writeBridgeJSON(t, w, map[string]any{"data": map[string]any{
				"id":             "cloud.skill.release-notes",
				"kind":           "skill",
				"name":           "Release Notes",
				"summary":        "Writes notes.",
				"latest_version": "1.0.0",
				"source":         "anyclaw-cloud",
				"risk_level":     "low",
				"trust_level":    "verified",
				"permissions":    []string{"fs.read"},
			}})
		case r.URL.Path == "/v1/artifacts/cloud.skill.release-notes/versions":
			writeBridgeJSON(t, w, map[string]any{"data": map[string]any{
				"items": []map[string]any{{"version": "1.0.0"}},
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
