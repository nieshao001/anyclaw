package gateway

import (
	"context"
	"strings"

	"github.com/1024XEngineer/anyclaw/pkg/marketplace"
	marketbridge "github.com/1024XEngineer/anyclaw/pkg/marketplace/bridge"
	"github.com/1024XEngineer/anyclaw/pkg/runtime"
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

func (s *Server) marketplaceBridge() marketbridge.Bridge {
	if s == nil {
		return nil
	}
	return marketbridge.New(marketbridge.Options{
		Store:        s.marketplaceStore(),
		Registry:     s.cloudRegistryClient(),
		LocalCatalog: s.localMarketCatalog(),
		AfterInstall: func(ctx context.Context, receipt *marketplace.InstallReceipt) error {
			if s.mainRuntime == nil {
				return nil
			}
			return s.mainRuntime.IntegrateMarketReceiptAndRefresh(ctx, receipt)
		},
		AfterBind: func(ctx context.Context, binding *marketplace.Binding) error {
			if s.mainRuntime == nil {
				return nil
			}
			return s.mainRuntime.RefreshAfterMarketBinding(ctx, binding)
		},
		AfterUninstall: func(ctx context.Context, result *marketplace.UninstallResult) error {
			return s.refreshAfterMarketUninstall(ctx)
		},
	})
}

func (s *Server) refreshAfterMarketUninstall(ctx context.Context) error {
	if s == nil || s.mainRuntime == nil {
		return nil
	}
	if s.mainRuntime.HotReload != nil {
		result := s.mainRuntime.HotReload.Refresh(ctx, runtime.RefreshScope{Kind: runtime.RefreshScopeGlobal, Reason: "market.uninstall"})
		if result.Status == "failed" {
			return errString(result.Error)
		}
		return nil
	}
	if err := s.mainRuntime.RefreshToolRegistry(); err != nil {
		return err
	}
	return nil
}
