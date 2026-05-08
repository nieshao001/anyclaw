package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/1024XEngineer/anyclaw/pkg/marketplace"
	marketbridge "github.com/1024XEngineer/anyclaw/pkg/marketplace/bridge"
)

func (s *Server) handleMarketInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.mainRuntime == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime not available"})
		return
	}
	var req marketplace.InstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.IdempotencyKey = r.Header.Get("Idempotency-Key")
	if strings.TrimSpace(req.InstalledBy) == "" {
		req.InstalledBy = marketActor(r)
	}
	result, err := s.marketplaceBridge().StartInstall(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !result.Reused && result.Job != nil && !isTerminalMarketJob(result.Job.State) {
		s.enqueueMarketJob(result.Job.ID)
	}
	writeJSON(w, marketJobStatus(result), map[string]any{"job_id": result.Job.ID, "job": result.Job, "reused": result.Reused})
}

func (s *Server) handleMarketUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.mainRuntime == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime not available"})
		return
	}
	var req marketplace.UpgradeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.IdempotencyKey = r.Header.Get("Idempotency-Key")
	if strings.TrimSpace(req.InstalledBy) == "" {
		req.InstalledBy = marketActor(r)
	}
	result, err := s.marketplaceBridge().StartUpgrade(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !result.Reused && result.Job != nil && !isTerminalMarketJob(result.Job.State) {
		s.enqueueMarketJob(result.Job.ID)
	}
	writeJSON(w, marketJobStatus(result), map[string]any{"job_id": result.Job.ID, "job": result.Job, "reused": result.Reused})
}

func (s *Server) handleMarketUninstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.mainRuntime == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime not available"})
		return
	}
	var req marketplace.UninstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if strings.TrimSpace(req.Actor) == "" {
		req.Actor = marketActor(r)
	}
	result, err := s.marketplaceBridge().Uninstall(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) handleMarketJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := s.marketplaceBridge().ListJobs(parseIntParam(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) handleMarketJobDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/market/jobs/"), "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "job id required"})
		return
	}
	job, err := s.marketplaceBridge().GetJob(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": job})
}

func (s *Server) enqueueMarketJob(jobID string) {
	run := func() {
		_, _ = s.marketplaceBridge().ExecuteJob(context.Background(), jobID)
	}
	if s != nil && s.jobQueue != nil {
		s.jobQueue <- run
		return
	}
	go run()
}

func marketJobStatus(result marketbridge.InstallResult) int {
	if result.Job != nil && isTerminalMarketJob(result.Job.State) {
		return http.StatusOK
	}
	return http.StatusAccepted
}

func isTerminalMarketJob(state marketplace.JobState) bool {
	return state == marketplace.JobSucceeded || state == marketplace.JobFailed || state == marketplace.JobRolledBack || state == marketplace.JobCanceled
}
