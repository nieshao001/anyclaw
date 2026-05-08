package gateway

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
	appRuntime "github.com/1024XEngineer/anyclaw/pkg/runtime"
	"github.com/1024XEngineer/anyclaw/pkg/state"
)

func TestMarketInstallCreatesJobAndReceiptThroughBridge(t *testing.T) {
	archive := testGatewayMarketArchive(t, "cloud.skill.release-notes", marketplace.ArtifactKindSkill, "1.0.0")
	registry := testGatewayMarketRegistry(t, "cloud.skill.release-notes", marketplace.ArtifactKindSkill, "1.0.0", "low", "verified", []string{"fs.read"}, archive)
	defer registry.Close()

	server := newGatewayMarketTestServer(t, registry.URL)
	runQueuedMarketJobs(t, server)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/market/install", strings.NewReader(`{"artifact_id":"cloud.skill.release-notes","user_confirmed":true}`))
	req.Header.Set("Idempotency-Key", "install-1")
	server.handleMarketInstall(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("install status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	job := waitGatewayMarketJob(t, server, payload.JobID)
	if job.State != marketplace.JobSucceeded || job.ReceiptID == "" {
		t.Fatalf("job = %#v, want succeeded with receipt", job)
	}

	rec = httptest.NewRecorder()
	server.handleMarketJobDetail(rec, httptest.NewRequest(http.MethodGet, "/market/jobs/"+payload.JobID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("job detail status = %d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	server.handleMarketJobs(rec, httptest.NewRequest(http.MethodGet, "/market/jobs", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), payload.JobID) {
		t.Fatalf("jobs status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMarketInstallPolicyRequiresAcknowledgement(t *testing.T) {
	archive := testGatewayMarketArchive(t, "cloud.skill.shell-helper", marketplace.ArtifactKindSkill, "1.0.0")
	registry := testGatewayMarketRegistry(t, "cloud.skill.shell-helper", marketplace.ArtifactKindSkill, "1.0.0", "low", "verified", []string{"process.exec"}, archive)
	defer registry.Close()

	server := newGatewayMarketTestServer(t, registry.URL)
	runQueuedMarketJobs(t, server)

	rec := httptest.NewRecorder()
	server.handleMarketInstall(rec, httptest.NewRequest(http.MethodPost, "/market/install", strings.NewReader(`{"artifact_id":"cloud.skill.shell-helper","user_confirmed":true}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("install status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	job := waitGatewayMarketJob(t, server, payload.JobID)
	if job.State != marketplace.JobFailed || job.Decision == nil || !job.Decision.RequiresRiskAcknowledgement {
		t.Fatalf("job = %#v, want failed high-risk acknowledgement", job)
	}

	rec = httptest.NewRecorder()
	server.handleMarketEvents(rec, httptest.NewRequest(http.MethodGet, "/market/events", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "market.policy.decision") {
		t.Fatalf("events status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMarketUpgradeAndUninstallUseBridge(t *testing.T) {
	archive := testGatewayMarketArchive(t, "cloud.skill.release-notes", marketplace.ArtifactKindSkill, "2.0.0")
	registry := testGatewayMarketRegistry(t, "cloud.skill.release-notes", marketplace.ArtifactKindSkill, "2.0.0", "low", "verified", []string{"fs.read"}, archive)
	defer registry.Close()

	server := newGatewayMarketTestServer(t, registry.URL)
	runner := runQueuedMarketJobs(t, server)
	receipt := &marketplace.InstallReceipt{
		ID:            "cloud.skill.release-notes@1.0.0",
		ArtifactID:    "cloud.skill.release-notes",
		Kind:          marketplace.ArtifactKindSkill,
		Name:          "Release Notes",
		Version:       "1.0.0",
		Source:        marketplace.SourceCloud,
		InstalledPath: filepath.Join(server.marketplaceStore().InstalledDir(), "skill", "cloud-skill-release-notes", "1-0-0"),
		InstalledBy:   "user",
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := os.MkdirAll(receipt.InstalledPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(receipt.InstalledPath, "skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receipt.InstalledPath, "anyclaw.artifact.json"), []byte(`{"id":"cloud.skill.release-notes","kind":"skill","name":"Release Notes","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receipt.InstalledPath, "skill", "SKILL.md"), []byte("# Release Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := server.marketplaceStore().SaveReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	if err := server.mainRuntime.IntegrateMarketReceiptAndRefresh(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(server.mainRuntime.Config.Skills.Dir, "cloud-skill-release-notes")); err != nil {
		t.Fatalf("expected integrated skill dir: %v", err)
	}
	binding, err := server.marketplaceStore().CreateBinding(marketplace.BindingRequest{
		ArtifactID: receipt.ArtifactID,
		ReceiptID:  receipt.ID,
		TargetType: marketplace.TargetRuntimeGlobal,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	server.handleMarketUpgrade(rec, httptest.NewRequest(http.MethodPost, "/market/upgrade", strings.NewReader(`{"artifact_id":"cloud.skill.release-notes","user_confirmed":true}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("upgrade status = %d body=%s", rec.Code, rec.Body.String())
	}
	var upgradePayload struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &upgradePayload); err != nil {
		t.Fatal(err)
	}
	job := waitGatewayMarketJob(t, server, upgradePayload.JobID)
	runner.Wait(t)
	if job.State != marketplace.JobSucceeded || job.Version != "2.0.0" {
		t.Fatalf("job = %#v, want upgraded to 2.0.0", job)
	}
	bindings, err := server.marketplaceStore().ListBindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings.Items) != 1 || bindings.Items[0].ID != binding.ID || bindings.Items[0].Version != "2.0.0" {
		t.Fatalf("bindings = %#v, want upgraded binding", bindings.Items)
	}

	rec = httptest.NewRecorder()
	server.handleMarketUninstall(rec, httptest.NewRequest(http.MethodPost, "/market/uninstall", strings.NewReader(`{"artifact_id":"cloud.skill.release-notes"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall status = %d body=%s", rec.Code, rec.Body.String())
	}
	var uninstallPayload struct {
		Data marketplace.UninstallResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &uninstallPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := server.marketplaceStore().GetReceipt(uninstallPayload.Data.ReceiptID); err != marketplace.ErrArtifactNotFound {
		t.Fatalf("uninstalled receipt err = %v, want not found", err)
	}
	if bindings, err := server.marketplaceStore().ListBindings(); err != nil || len(bindings.Items) != 0 {
		t.Fatalf("bindings after uninstall = %#v err=%v, want removed", bindings.Items, err)
	}
	if _, err := os.Stat(filepath.Join(server.mainRuntime.Config.Skills.Dir, "cloud-skill-release-notes")); !os.IsNotExist(err) {
		t.Fatalf("integrated skill dir err = %v, want not exist", err)
	}
}

func testGatewayMarketRegistry(t *testing.T, id string, kind marketplace.ArtifactKind, version, risk, trust string, permissions []string, archive []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/resolve"):
			writeRegistryJSON(t, w, map[string]any{"data": map[string]any{
				"artifact_id":     id,
				"version":         version,
				"download_url":    "http://" + r.Host + "/v1/download/" + id + "/" + version,
				"checksum_sha256": gatewayMarketSHA256(archive),
				"size_bytes":      len(archive),
				"risk_level":      risk,
				"trust_level":     trust,
				"permissions":     permissions,
				"kind":            kind,
				"name":            id,
			}})
		case strings.Contains(r.URL.Path, "/v1/download/"):
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
}

func testGatewayMarketArchive(t *testing.T, id string, kind marketplace.ArtifactKind, version string) []byte {
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

func gatewayMarketSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newGatewayMarketTestServer(t *testing.T, endpoint string) *Server {
	t.Helper()
	workDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agent.Name = "Main Agent"
	cfg.Agent.ActiveProfile = "Main Agent"
	cfg.Agent.Profiles = []config.AgentProfile{{Name: "Main Agent", PermissionLevel: "limited"}}
	cfg.Agent.WorkDir = workDir
	cfg.Agent.WorkingDir = workDir
	cfg.Skills.Dir = filepath.Join(workDir, "skills")
	cfg.Marketplace.RegistryEndpoint = endpoint
	cfg.Marketplace.RequestTimeoutSeconds = 2
	cfg.Marketplace.CacheTTLSeconds = 0
	if err := cfg.Save(filepath.Join(workDir, "anyclaw.json")); err != nil {
		t.Fatal(err)
	}
	store, err := state.NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	rt := &appRuntime.MainRuntime{
		Config:     cfg,
		ConfigPath: filepath.Join(workDir, "anyclaw.json"),
		WorkDir:    workDir,
		WorkingDir: workDir,
	}
	server := &Server{
		mainRuntime: rt,
		store:       store,
		runtimePool: appRuntime.NewRuntimePool(rt.ConfigPath, store, 4, time.Minute),
		jobQueue:    make(chan func(), 16),
		marketJobs:  marketplace.NewStore(workDir),
	}
	server.sessions = state.NewSessionManager(store, nil)
	server.hotReload = appRuntime.NewHotReloadCoordinator(server.runtimePool, store)
	rt.HotReload = server.hotReload
	return server
}

type gatewayMarketJobRunner struct {
	done      chan struct{}
	completed chan struct{}
	stopped   chan struct{}
}

func runQueuedMarketJobs(t *testing.T, server *Server) *gatewayMarketJobRunner {
	t.Helper()
	runner := &gatewayMarketJobRunner{
		done:      make(chan struct{}),
		completed: make(chan struct{}, 16),
		stopped:   make(chan struct{}),
	}
	go func() {
		defer close(runner.stopped)
		for {
			select {
			case job := <-server.jobQueue:
				job()
				select {
				case runner.completed <- struct{}{}:
				case <-runner.done:
					return
				}
			case <-runner.done:
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(runner.done)
		<-runner.stopped
	})
	return runner
}

func (r *gatewayMarketJobRunner) Wait(t *testing.T) {
	t.Helper()
	if r == nil {
		return
	}
	select {
	case <-r.completed:
	case <-time.After(5 * time.Second):
		t.Fatal("queued market job did not finish")
	}
}

func waitGatewayMarketJob(t *testing.T, server *Server, jobID string) *marketplace.InstallJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := server.marketplaceStore().GetJob(jobID)
		if err == nil && isTerminalMarketJob(job.State) {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, err := server.marketplaceStore().GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("job did not finish: %#v", job)
	return nil
}
