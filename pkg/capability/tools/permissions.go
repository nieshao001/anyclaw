package tools

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

const (
	SandboxModeReadOnly         = "read-only"
	SandboxModeWorkspaceWrite   = "workspace-write"
	SandboxModeDangerFullAccess = "danger-full-access"

	ApprovalPolicyUntrusted = "untrusted"
	ApprovalPolicyOnRequest = "on-request"
	ApprovalPolicyOnFailure = "on-failure"
	ApprovalPolicyNever     = "never"

	NetworkAccessEnabled  = "enabled"
	NetworkAccessDisabled = "disabled"

	DesktopAccessDisabled          = "disabled"
	DesktopAccessAskOncePerSession = "ask-once-per-session"
	DesktopAccessAlways            = "always"
)

type PermissionDecision string

const (
	DecisionAllow PermissionDecision = "allow"
	DecisionDeny  PermissionDecision = "deny"
	DecisionAsk   PermissionDecision = "ask"
)

type PermissionOptions struct {
	SandboxMode    string
	ApprovalPolicy string
	NetworkAccess  string
	DesktopAccess  string
}

type PermissionAction struct {
	Kind       string
	ToolName   string
	Path       string
	Command    string
	CWD        string
	URL        string
	Reason     string
	Capability string
}

type PermissionResult struct {
	Decision   PermissionDecision
	Reason     string
	Capability string
}

type PermissionEngine struct {
	workingDir string
	options    PermissionOptions
}

func NewPermissionEngine(workingDir string, opts PermissionOptions) *PermissionEngine {
	return &PermissionEngine{
		workingDir: normalizePolicyPath(resolvePath(firstNonEmptyPermissionString(workingDir, "."), "")),
		options:    NormalizePermissionOptions(opts),
	}
}

func NormalizePermissionOptions(opts PermissionOptions) PermissionOptions {
	opts.SandboxMode = strings.TrimSpace(strings.ToLower(opts.SandboxMode))
	if opts.SandboxMode == "" {
		opts.SandboxMode = SandboxModeWorkspaceWrite
	}
	opts.ApprovalPolicy = strings.TrimSpace(strings.ToLower(opts.ApprovalPolicy))
	if opts.ApprovalPolicy == "" {
		opts.ApprovalPolicy = ApprovalPolicyOnRequest
	}
	opts.NetworkAccess = strings.TrimSpace(strings.ToLower(opts.NetworkAccess))
	if opts.NetworkAccess == "" {
		opts.NetworkAccess = NetworkAccessEnabled
	}
	opts.DesktopAccess = strings.TrimSpace(strings.ToLower(opts.DesktopAccess))
	if opts.DesktopAccess == "" {
		opts.DesktopAccess = DesktopAccessAskOncePerSession
	}
	return opts
}

func (pe *PermissionEngine) Options() PermissionOptions {
	if pe == nil {
		return NormalizePermissionOptions(PermissionOptions{})
	}
	return pe.options
}

func (pe *PermissionEngine) Decide(action PermissionAction) PermissionResult {
	if pe == nil {
		return PermissionResult{Decision: DecisionAllow}
	}
	action.Kind = strings.TrimSpace(strings.ToLower(action.Kind))
	switch action.Kind {
	case "read":
		return pe.decideRead(action)
	case "write", "delete":
		return pe.decideWrite(action)
	case "execute":
		return pe.decideExecute(action)
	case "network":
		return pe.decideNetwork(action)
	case "desktop":
		return pe.decideDesktop(action)
	default:
		return PermissionResult{Decision: DecisionAllow}
	}
}

func (pe *PermissionEngine) Check(action PermissionAction) error {
	result := pe.Decide(action)
	if result.Decision == DecisionAllow {
		return nil
	}
	reason := strings.TrimSpace(result.Reason)
	if reason == "" {
		reason = fmt.Sprintf("%s denied by permissions policy", action.Kind)
	}
	return fmt.Errorf("%s", reason)
}

func CheckPermission(ctx context.Context, opts BuiltinOptions, action PermissionAction) error {
	if action.Kind == "desktop" && HasHostReviewedCapability(ctx, HostReviewedCapabilityDesktop) {
		return nil
	}
	if HasPermissionActionGrant(ctx, action) {
		return nil
	}
	if opts.PermissionEngine != nil {
		result := opts.PermissionEngine.Decide(action)
		if result.Decision == DecisionAllow {
			return nil
		}
		reason := strings.TrimSpace(result.Reason)
		if reason == "" {
			reason = fmt.Sprintf("%s denied by permissions policy", action.Kind)
		}
		return fmt.Errorf("%s", reason)
	}
	return nil
}

func PermissionActionForTool(toolName string, args map[string]any, workingDir string) PermissionAction {
	name := strings.TrimSpace(strings.ToLower(toolName))
	switch {
	case name == "read_file" || name == "read":
		return PermissionAction{Kind: "read", ToolName: toolName, Path: permissionArtifactPath(firstPermissionArg(args, "path"), workingDir)}
	case name == "list_directory" || name == "search_files":
		return PermissionAction{Kind: "read", ToolName: toolName, Path: permissionArtifactPath(firstPermissionArg(args, "path"), workingDir)}
	case name == "write_file" || name == "write" || name == "edit":
		return PermissionAction{Kind: "write", ToolName: toolName, Path: permissionArtifactPath(firstPermissionArg(args, "path"), workingDir)}
	case name == "apply_patch":
		return PermissionAction{Kind: "write", ToolName: toolName, Path: workingDir, Reason: "apply patch in workspace"}
	case name == "run_command" || name == "exec" || name == "process" || name == "clihub_exec":
		return PermissionAction{
			Kind:     "execute",
			ToolName: toolName,
			Command:  firstPermissionArg(args, "command", "cmd"),
			CWD:      firstNonEmptyPermissionString(firstPermissionArg(args, "cwd"), workingDir),
		}
	case name == "fetch_url" || name == "web_fetch" || name == "browser_navigate" || name == "browser_upload":
		return PermissionAction{Kind: "network", ToolName: toolName, URL: firstPermissionArg(args, "url", "target_url", "target")}
	case name == "image_analyze":
		if urlValue := firstPermissionArg(args, "url"); urlValue != "" {
			return PermissionAction{Kind: "network", ToolName: toolName, URL: urlValue}
		}
		return PermissionAction{Kind: "read", ToolName: toolName, Path: firstPermissionArg(args, "path")}
	case strings.HasPrefix(name, "desktop_") || strings.HasPrefix(name, "computer_"):
		return PermissionAction{Kind: "desktop", ToolName: toolName, Capability: HostReviewedCapabilityDesktop}
	default:
		return PermissionAction{Kind: "custom", ToolName: toolName}
	}
}

func firstPermissionArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if args == nil {
			return ""
		}
		if value, ok := args[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func (pe *PermissionEngine) decideRead(action PermissionAction) PermissionResult {
	if pe.options.SandboxMode == SandboxModeDangerFullAccess {
		return allowDecision()
	}
	if strings.TrimSpace(action.Path) == "" || pe.pathInsideWorkspace(action.Path) {
		return allowDecision()
	}
	return pe.askOrDeny(fmt.Sprintf("read outside workspace denied: %s", action.Path), "")
}

func (pe *PermissionEngine) decideWrite(action PermissionAction) PermissionResult {
	switch pe.options.SandboxMode {
	case SandboxModeDangerFullAccess:
		return allowDecision()
	case SandboxModeReadOnly:
		return pe.askOrDeny(fmt.Sprintf("%s denied: sandbox is read-only", action.Kind), "")
	default:
		if strings.TrimSpace(action.Path) == "" || pe.pathInsideWorkspace(action.Path) {
			return allowDecision()
		}
		return pe.askOrDeny(fmt.Sprintf("%s outside workspace denied: %s", action.Kind, action.Path), "")
	}
}

func (pe *PermissionEngine) decideExecute(action PermissionAction) PermissionResult {
	if isDangerousCommand(action.Command, dangerousCommandPatterns) {
		return pe.askOrDeny("command execution requires approval: dangerous command pattern matched", "")
	}
	switch pe.options.SandboxMode {
	case SandboxModeDangerFullAccess:
		return allowDecision()
	case SandboxModeReadOnly:
		return pe.askOrDeny("command execution denied: sandbox is read-only", "")
	default:
		if strings.TrimSpace(action.CWD) == "" || pe.pathInsideWorkspace(action.CWD) {
			return allowDecision()
		}
		return pe.askOrDeny(fmt.Sprintf("command cwd outside workspace denied: %s", action.CWD), "")
	}
}

func (pe *PermissionEngine) decideNetwork(action PermissionAction) PermissionResult {
	if pe.options.NetworkAccess == NetworkAccessEnabled {
		return allowDecision()
	}
	target := strings.TrimSpace(action.URL)
	if target == "" {
		target = strings.TrimSpace(action.Reason)
	}
	if isLocalPermissionURL(target) {
		return allowDecision()
	}
	return pe.askOrDeny(fmt.Sprintf("network access denied: %s", firstNonEmptyPermissionString(target, "network is disabled")), "")
}

func (pe *PermissionEngine) decideDesktop(action PermissionAction) PermissionResult {
	switch pe.options.DesktopAccess {
	case DesktopAccessAlways:
		return allowDecision()
	case DesktopAccessDisabled:
		return PermissionResult{Decision: DecisionDeny, Reason: "desktop access is disabled", Capability: HostReviewedCapabilityDesktop}
	default:
		return pe.askOrDeny("desktop access requires session approval", HostReviewedCapabilityDesktop)
	}
}

func (pe *PermissionEngine) askOrDeny(reason string, capability string) PermissionResult {
	switch pe.options.ApprovalPolicy {
	case ApprovalPolicyNever, ApprovalPolicyOnFailure:
		return PermissionResult{Decision: DecisionDeny, Reason: reason, Capability: capability}
	default:
		return PermissionResult{Decision: DecisionAsk, Reason: reason, Capability: capability}
	}
}

func (pe *PermissionEngine) pathInsideWorkspace(path string) bool {
	if pe == nil {
		return true
	}
	target := normalizePolicyPath(resolvePath(path, pe.workingDir))
	if target == "" || pe.workingDir == "" {
		return true
	}
	return pathWithin(target, pe.workingDir)
}

func allowDecision() PermissionResult {
	return PermissionResult{Decision: DecisionAllow}
}

func firstNonEmptyPermissionString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func isLocalPermissionURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.TrimSpace(strings.ToLower(parsed.Hostname()))
	return host == "" || isLocalEgressHost(host)
}

func permissionArtifactPath(path string, workingDir string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return resolvePath(path, workingDir)
}
