package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/1024XEngineer/anyclaw/pkg/marketplace"
	"github.com/1024XEngineer/anyclaw/pkg/runtime"
)

func (s *Server) handleMarketBindings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		result, err := s.marketplaceBridge().ListBindings()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	case http.MethodPost:
		var req marketplace.BindingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		normalized, err := s.normalizeMarketBindingRequest(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		binding, err := s.marketplaceBridge().Bind(r.Context(), normalized)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": binding})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMarketBindingByID(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/market/bindings/"), "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "binding id required"})
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if _, err := s.marketplaceBridge().DeleteBinding(r.Context(), id); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "binding not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMarketEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := s.marketplaceBridge().ListEvents(parseIntParam(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) handleMarketRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Scope     string `json:"scope,omitempty"`
		Agent     string `json:"agent,omitempty"`
		Org       string `json:"org,omitempty"`
		Project   string `json:"project,omitempty"`
		Workspace string `json:"workspace,omitempty"`
		SessionID string `json:"session_id,omitempty"`
		Warm      bool   `json:"warm,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	scopeKind := runtime.RefreshScopeKind(strings.TrimSpace(req.Scope))
	if scopeKind == "" && strings.TrimSpace(req.SessionID) != "" {
		scopeKind = runtime.RefreshScopeSession
	}
	agent, orgID, projectID, workspaceID := req.Agent, req.Org, req.Project, req.Workspace
	if scopeKind != runtime.RefreshScopeSession {
		agent, orgID, projectID, workspaceID = s.marketRefreshTarget(req.Agent, req.Org, req.Project, req.Workspace)
	}
	result := s.marketHotReload().Refresh(r.Context(), runtime.RefreshScope{
		Kind:      scopeKind,
		Agent:     agent,
		Org:       orgID,
		Project:   projectID,
		Workspace: workspaceID,
		SessionID: req.SessionID,
		Reason:    "market.manual_refresh",
		Warm:      req.Warm,
	})
	if result.Status == "failed" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "failed", "error": result.Error, "result": result})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "refreshed",
		"agent":     agent,
		"org":       orgID,
		"project":   projectID,
		"workspace": workspaceID,
		"result":    result,
	})
}

func (s *Server) normalizeMarketBindingRequest(req marketplace.BindingRequest) (marketplace.BindingRequest, error) {
	req.ArtifactID = strings.TrimSpace(req.ArtifactID)
	req.ReceiptID = strings.TrimSpace(req.ReceiptID)
	req.TargetType = marketplace.NormalizeBindingTargetType(string(req.TargetType))
	req.TargetID = strings.TrimSpace(req.TargetID)
	if req.TargetType == "" {
		return req, errString("invalid target_type")
	}
	if req.TargetType == marketplace.TargetMainAgent {
		req.TargetID = strings.TrimSpace(s.mainRuntime.Config.ResolveMainAgentName())
		if req.TargetID == "" {
			req.TargetID = strings.TrimSpace(s.mainRuntime.Config.Agent.Name)
		}
	}
	if req.TargetType == marketplace.TargetWorkspace && req.TargetID == "" {
		_, _, workspaceID := defaultResourceIDs(s.mainRuntime.WorkingDir)
		req.TargetID = workspaceID
	}
	return req, nil
}

func marketActor(r *http.Request) string {
	if user := UserFromContext(r.Context()); user != nil && strings.TrimSpace(user.Name) != "" {
		return user.Name
	}
	return "user"
}

func (s *Server) marketRefreshTarget(agent, orgID, projectID, workspaceID string) (string, string, string, string) {
	if s == nil || s.mainRuntime == nil {
		return agent, orgID, projectID, workspaceID
	}
	if strings.TrimSpace(agent) == "" && s.mainRuntime.Config != nil {
		agent = strings.TrimSpace(s.mainRuntime.Config.ResolveMainAgentName())
		if agent == "" {
			agent = strings.TrimSpace(s.mainRuntime.Config.Agent.Name)
		}
	}
	defaultOrg, defaultProject, defaultWorkspace := defaultResourceIDs(s.mainRuntime.WorkingDir)
	if strings.TrimSpace(orgID) == "" {
		orgID = defaultOrg
	}
	if strings.TrimSpace(projectID) == "" {
		projectID = defaultProject
	}
	if strings.TrimSpace(workspaceID) == "" {
		workspaceID = defaultWorkspace
	}
	return agent, orgID, projectID, workspaceID
}

type errString string

func (e errString) Error() string {
	return string(e)
}

func (s *Server) marketHotReload() *runtime.HotReloadCoordinator {
	if s == nil {
		return nil
	}
	if s.hotReload == nil {
		s.hotReload = runtime.NewHotReloadCoordinator(s.runtimePool, s.store)
	}
	return s.hotReload
}
