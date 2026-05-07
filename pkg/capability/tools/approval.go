package tools

import (
	"context"
	"strings"
	"sync"
)

type ToolApprovalCall struct {
	Name string
	Args map[string]any
}

type ToolApprovalHook func(ctx context.Context, call ToolApprovalCall) error

type ToolCallerRole string

const (
	ToolCallerRoleUnknown    ToolCallerRole = ""
	ToolCallerRoleMainAgent  ToolCallerRole = "main_agent"
	ToolCallerRoleSubAgent   ToolCallerRole = "sub_agent"
	ToolCallerRoleSystem     ToolCallerRole = "system"
	ToolCallerRoleControlAPI ToolCallerRole = "control_api"
)

type ToolCaller struct {
	Role        ToolCallerRole
	AgentName   string
	ExecutionID string
}

type toolApprovalHookKey struct{}
type toolCallerKey struct{}
type approvalGrantScopeKey struct{}

const HostReviewedCapabilityDesktop = "desktop"

type approvalGrantScope struct {
	mu               sync.RWMutex
	hostReviewedCaps map[string]struct{}
	permissionGrants map[string]struct{}
}

func WithToolApprovalHook(ctx context.Context, hook ToolApprovalHook) context.Context {
	if hook == nil {
		return ctx
	}
	return context.WithValue(ctx, toolApprovalHookKey{}, hook)
}

func RequestToolApproval(ctx context.Context, name string, args map[string]any) error {
	if ctx == nil {
		return nil
	}
	hook, _ := ctx.Value(toolApprovalHookKey{}).(ToolApprovalHook)
	if hook == nil {
		return nil
	}
	if err := hook(ctx, ToolApprovalCall{Name: name, Args: args}); err != nil {
		return err
	}
	if toolApprovalCapability(name) == HostReviewedCapabilityDesktop {
		GrantHostReviewedCapability(ctx, HostReviewedCapabilityDesktop)
	}
	return nil
}

func WithApprovalGrantScope(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if approvalGrantScopeFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, approvalGrantScopeKey{}, &approvalGrantScope{
		hostReviewedCaps: map[string]struct{}{},
		permissionGrants: map[string]struct{}{},
	})
}

func GrantHostReviewedCapability(ctx context.Context, capability string) {
	scope := approvalGrantScopeFromContext(ctx)
	if scope == nil {
		return
	}
	capability = strings.TrimSpace(strings.ToLower(capability))
	if capability == "" {
		return
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.hostReviewedCaps[capability] = struct{}{}
}

func HasHostReviewedCapability(ctx context.Context, capability string) bool {
	scope := approvalGrantScopeFromContext(ctx)
	if scope == nil {
		return false
	}
	capability = strings.TrimSpace(strings.ToLower(capability))
	if capability == "" {
		return false
	}
	scope.mu.RLock()
	defer scope.mu.RUnlock()
	_, ok := scope.hostReviewedCaps[capability]
	return ok
}

func GrantPermissionAction(ctx context.Context, action PermissionAction) {
	scope := approvalGrantScopeFromContext(ctx)
	if scope == nil {
		return
	}
	key := permissionGrantKey(action)
	if key == "" {
		return
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.permissionGrants[key] = struct{}{}
}

func HasPermissionActionGrant(ctx context.Context, action PermissionAction) bool {
	scope := approvalGrantScopeFromContext(ctx)
	if scope == nil {
		return false
	}
	key := permissionGrantKey(action)
	if key == "" {
		return false
	}
	scope.mu.RLock()
	defer scope.mu.RUnlock()
	_, ok := scope.permissionGrants[key]
	return ok
}

func approvalGrantScopeFromContext(ctx context.Context) *approvalGrantScope {
	if ctx == nil {
		return nil
	}
	scope, _ := ctx.Value(approvalGrantScopeKey{}).(*approvalGrantScope)
	return scope
}

func toolApprovalCapability(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "desktop_plan" || strings.HasPrefix(name, "desktop_") || strings.HasPrefix(name, "computer_") {
		return HostReviewedCapabilityDesktop
	}
	return ""
}

func permissionGrantKey(action PermissionAction) string {
	kind := strings.TrimSpace(strings.ToLower(action.Kind))
	if kind == "" {
		return ""
	}
	parts := []string{kind}
	switch kind {
	case "desktop":
		parts = append(parts, HostReviewedCapabilityDesktop)
	case "read", "write", "delete":
		parts = append(parts, normalizePolicyPath(permissionArtifactPath(action.Path, "")))
	case "execute":
		parts = append(parts, normalizePolicyPath(permissionArtifactPath(action.CWD, "")), strings.TrimSpace(action.Command))
	case "network":
		parts = append(parts, strings.TrimSpace(strings.ToLower(action.URL)))
	default:
		parts = append(parts, strings.TrimSpace(action.ToolName), strings.TrimSpace(action.Reason))
	}
	return strings.Join(parts, "|")
}

func WithToolCaller(ctx context.Context, caller ToolCaller) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, toolCallerKey{}, caller)
}

func ToolCallerFromContext(ctx context.Context) ToolCaller {
	if ctx == nil {
		return ToolCaller{}
	}
	caller, _ := ctx.Value(toolCallerKey{}).(ToolCaller)
	return caller
}
