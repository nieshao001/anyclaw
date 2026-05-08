package gateway

import (
	"strings"

	"github.com/1024XEngineer/anyclaw/pkg/marketplace"
)

func (s *Server) marketplaceStore() *marketplace.Store {
	if s == nil {
		return marketplace.NewStore(".")
	}
	if s.marketJobs == nil {
		root := "."
		if s.mainRuntime != nil && strings.TrimSpace(s.mainRuntime.WorkDir) != "" {
			root = s.mainRuntime.WorkDir
		}
		s.marketJobs = marketplace.NewStore(root)
	}
	return s.marketJobs
}
