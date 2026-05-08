package gateway

import (
	"errors"
	"net/http"
	"strings"

	"github.com/1024XEngineer/anyclaw/pkg/clihub"
	"github.com/1024XEngineer/anyclaw/pkg/marketplace"
	marketregistry "github.com/1024XEngineer/anyclaw/pkg/marketplace/registry"
)

func (s *Server) handleMarketArtifacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.mainRuntime == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime not available"})
		return
	}

	filter := marketplace.Filter{
		Kind:       marketplace.NormalizeKind(r.URL.Query().Get("kind")),
		Source:     marketplace.NormalizeSource(r.URL.Query().Get("source")),
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Status:     marketplace.NormalizeStatus(r.URL.Query().Get("status")),
		Risk:       strings.TrimSpace(r.URL.Query().Get("risk")),
		Trust:      strings.TrimSpace(r.URL.Query().Get("trust")),
		Tag:        strings.TrimSpace(r.URL.Query().Get("tag")),
		Permission: strings.TrimSpace(r.URL.Query().Get("permission")),
		Publisher:  strings.TrimSpace(r.URL.Query().Get("publisher")),
		OS:         strings.TrimSpace(r.URL.Query().Get("os")),
		Arch:       strings.TrimSpace(r.URL.Query().Get("arch")),
		Sort:       strings.TrimSpace(r.URL.Query().Get("sort")),
		Limit:      parseIntParam(r.URL.Query().Get("limit"), 50),
		Offset:     parseIntParam(r.URL.Query().Get("offset"), 0),
	}
	if filter.Source == marketplace.SourceCloud {
		list, err := s.marketplaceBridge().List(r.Context(), filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if list.CloudErr != "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"data": list.Result,
				"meta": map[string]any{
					"cloud_error": list.CloudErr,
				},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": list.Result})
		return
	}
	list, err := s.marketplaceBridge().List(r.Context(), filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": list.Result,
	})
}

func (s *Server) handleMarketArtifactDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.mainRuntime == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime not available"})
		return
	}

	id, versions := parseMarketArtifactPath(r.URL.Path)
	if id == "" {
		http.Error(w, "artifact id required", http.StatusBadRequest)
		return
	}

	if s.shouldUseCloudMarketArtifact(r, id) {
		if versions {
			items, err := s.marketplaceBridge().Versions(r.Context(), id, marketplace.SourceCloud)
			if err != nil {
				if errors.Is(err, marketregistry.ErrNotConfigured) || errors.Is(err, marketregistry.ErrRemoteDisabled) {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud registry unavailable"})
					return
				}
				if status, ok := marketregistry.HTTPStatusCode(err); ok && status == http.StatusNotFound {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "artifact not found"})
					return
				}
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": items, "total": len(items)}})
			return
		}

		artifact, err := s.marketplaceBridge().Get(r.Context(), id, marketplace.SourceCloud)
		if err != nil {
			if errors.Is(err, marketregistry.ErrNotConfigured) || errors.Is(err, marketregistry.ErrRemoteDisabled) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud registry unavailable"})
				return
			}
			if status, ok := marketregistry.HTTPStatusCode(err); ok && status == http.StatusNotFound {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "artifact not found"})
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": artifact})
		return
	}

	if versions {
		items, err := s.marketplaceBridge().Versions(r.Context(), id, marketplace.SourceLocal)
		if err != nil {
			if errors.Is(err, marketplace.ErrArtifactNotFound) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "artifact not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
		return
	}

	artifact, err := s.marketplaceBridge().Get(r.Context(), id, marketplace.SourceLocal)
	if err != nil {
		if errors.Is(err, marketplace.ErrArtifactNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "artifact not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": artifact})
}

func (s *Server) localMarketCatalog() *marketplace.LocalCatalog {
	return marketplace.NewLocalCatalog(marketplace.LocalCatalogDeps{
		Config:     s.mainRuntime.Config,
		Skills:     s.mainRuntime.Skills,
		Plugins:    s.plugins,
		AgentStore: s.storeModule,
		CLIHub:     s.loadCLIHubCatalog(),
	})
}

func (s *Server) cloudRegistryClient() *marketregistry.Client {
	if s == nil || s.mainRuntime == nil || s.mainRuntime.Config == nil {
		return nil
	}
	if !marketregistry.IsEnabled(s.mainRuntime.Config.Marketplace) {
		return nil
	}
	if s.marketRegistry == nil {
		s.marketRegistry = marketregistry.NewClientFromConfig(s.mainRuntime.Config.Marketplace)
	}
	return s.marketRegistry
}

func (s *Server) listCloudMarketArtifacts(r *http.Request, filter marketplace.Filter) (marketplace.ListResult, string) {
	result, err := s.marketplaceBridge().List(r.Context(), filter)
	if err != nil {
		return emptyMarketList(filter), err.Error()
	}
	return result.Result, result.CloudErr
}

func (s *Server) cloudMarketArtifact(r *http.Request, id string) (*marketplace.Artifact, error) {
	return s.marketplaceBridge().Get(r.Context(), id, marketplace.SourceCloud)
}

func (s *Server) cloudMarketVersions(r *http.Request, id string) ([]marketplace.ArtifactVersion, error) {
	return s.marketplaceBridge().Versions(r.Context(), id, marketplace.SourceCloud)
}

func (s *Server) shouldUseCloudMarketArtifact(r *http.Request, id string) bool {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return false
	}
	if strings.EqualFold(r.URL.Query().Get("source"), string(marketplace.SourceCloud)) {
		return true
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "cloud.") {
		return true
	}
	if s.cloudRegistryClient() == nil {
		return false
	}
	_, err := s.localMarketCatalog().Get(r.Context(), trimmed)
	return errors.Is(err, marketplace.ErrArtifactNotFound)
}

func emptyMarketList(filter marketplace.Filter) marketplace.ListResult {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	return marketplace.ListResult{Items: []marketplace.Artifact{}, Total: 0, Limit: limit, Offset: offset}
}

func parseMarketArtifactPath(path string) (string, bool) {
	trimmed := strings.Trim(strings.TrimPrefix(path, "/market/artifacts/"), "/")
	if trimmed == "" || trimmed == path {
		return "", false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 2 && parts[1] == "versions" {
		return parts[0], true
	}
	return parts[0], false
}

func (s *Server) loadCLIHubCatalog() *clihub.Catalog {
	if s == nil || s.mainRuntime == nil {
		return nil
	}
	catalog, err := clihub.LoadAuto(s.mainRuntime.WorkingDir)
	if err != nil {
		return nil
	}
	return catalog
}
