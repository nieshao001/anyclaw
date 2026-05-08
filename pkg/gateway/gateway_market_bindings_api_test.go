package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/1024XEngineer/anyclaw/pkg/marketplace"
)

func TestMarketBindingCreateDeleteAndRefresh(t *testing.T) {
	server := newGatewayMarketTestServer(t, "")
	receipt := &marketplace.InstallReceipt{
		ID:            "cloud.agent.code-reviewer@1.0.0",
		ArtifactID:    "cloud.agent.code-reviewer",
		Kind:          marketplace.ArtifactKindAgent,
		Name:          "Code Reviewer",
		Version:       "1.0.0",
		Source:        marketplace.SourceCloud,
		InstalledPath: t.TempDir(),
		InstalledBy:   "user",
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := server.marketplaceStore().SaveReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	server.handleMarketBindings(rec, httptest.NewRequest(http.MethodPost, "/market/bindings", strings.NewReader(`{"artifact_id":"cloud.agent.code-reviewer","target_type":"main_agent"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("binding status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data marketplace.Binding `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.TargetID != "Main Agent" {
		t.Fatalf("target id = %q, want Main Agent", payload.Data.TargetID)
	}
	if metrics := server.runtimePool.Metrics(); metrics.Refreshes != 1 {
		t.Fatalf("expected one refresh, got %+v", metrics)
	}

	rec = httptest.NewRecorder()
	server.handleMarketBindings(rec, httptest.NewRequest(http.MethodGet, "/market/bindings", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), payload.Data.ID) {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.handleMarketBindingByID(rec, httptest.NewRequest(http.MethodDelete, "/market/bindings/"+payload.Data.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", rec.Code, rec.Body.String())
	}
	if metrics := server.runtimePool.Metrics(); metrics.Refreshes != 2 {
		t.Fatalf("expected delete refresh, got %+v", metrics)
	}
}

func TestMarketRefreshAndBindingValidation(t *testing.T) {
	server := newGatewayMarketTestServer(t, "")

	rec := httptest.NewRecorder()
	server.handleMarketRefresh(rec, httptest.NewRequest(http.MethodPost, "/market/refresh", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.handleMarketBindings(rec, httptest.NewRequest(http.MethodPost, "/market/bindings", strings.NewReader(`{"artifact_id":"x","target_type":"unknown"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid binding status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.handleMarketBindingByID(rec, httptest.NewRequest(http.MethodDelete, "/market/bindings/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing delete status = %d body=%s", rec.Code, rec.Body.String())
	}

	agent, orgID, projectID, workspaceID := (*Server)(nil).marketRefreshTarget("a", "o", "p", "w")
	if agent != "a" || orgID != "o" || projectID != "p" || workspaceID != "w" {
		t.Fatalf("nil server changed target values: %q %q %q %q", agent, orgID, projectID, workspaceID)
	}
}

func TestMarketBindingCreateReturnsUnavailableWithoutRuntime(t *testing.T) {
	server := &Server{marketJobs: marketplace.NewStore(t.TempDir())}

	rec := httptest.NewRecorder()
	server.handleMarketBindings(rec, httptest.NewRequest(http.MethodPost, "/market/bindings", strings.NewReader(`{"artifact_id":"x","target_type":"main_agent"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("binding status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "runtime config is unavailable") {
		t.Fatalf("binding body = %s", rec.Body.String())
	}
}
