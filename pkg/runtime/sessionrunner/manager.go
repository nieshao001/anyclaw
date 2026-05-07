package sessionrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agent "github.com/1024XEngineer/anyclaw/pkg/capability/agents"
	"github.com/1024XEngineer/anyclaw/pkg/capability/tools"
	"github.com/1024XEngineer/anyclaw/pkg/config"
	appruntime "github.com/1024XEngineer/anyclaw/pkg/runtime"
	"github.com/1024XEngineer/anyclaw/pkg/runtime/taskrunner"
	"github.com/1024XEngineer/anyclaw/pkg/state"
)

type Manager struct {
	store     *state.Store
	sessions  *state.SessionManager
	runtimes  RuntimeProvider
	approvals ApprovalRequester
	events    EventRecorder
	nowFunc   func() time.Time
	nextID    func(prefix string) string
	execute   func(context.Context, *appruntime.MainRuntime, appruntime.ExecutionRequest) (*appruntime.ExecutionResult, error)
	stream    func(context.Context, *appruntime.MainRuntime, appruntime.ExecutionRequest, func(string)) (*appruntime.ExecutionResult, error)
}

type RuntimeProvider interface {
	GetOrCreate(agentName string, org string, project string, workspaceID string) (*appruntime.MainRuntime, error)
}

type ApprovalRequester interface {
	RequestWithSignature(taskID string, sessionID string, stepIndex int, toolName string, action string, payload map[string]any, signaturePayload map[string]any) (*state.Approval, error)
}

type EventRecorder interface {
	AppendEvent(eventType string, sessionID string, payload map[string]any)
}

type RunOptions struct {
	Source              string
	Resume              bool
	Channel             string
	ApprovalResumeState *agent.ApprovalResumeState
}

type RunRequest struct {
	SessionID string
	Title     string
	Message   string
	Options   RunOptions
}

type RunResult struct {
	Response string
	Session  *state.Session
}

type ApprovalContext struct {
	Title   string
	Message string
	Source  string
}

type ChannelRunRequest struct {
	Source    string
	SessionID string
	Message   string
	QueueMode string
	Meta      map[string]string
	Streaming bool
	OnChunk   func(string)
}

var ErrTaskWaitingApproval = taskrunner.ErrTaskWaitingApproval

func NewManager(store *state.Store, sessions *state.SessionManager, runtimes RuntimeProvider, approvals ApprovalRequester, events EventRecorder) *Manager {
	return &Manager{
		store:     store,
		sessions:  sessions,
		runtimes:  runtimes,
		approvals: approvals,
		events:    events,
		nowFunc: func() time.Time {
			return time.Now().UTC()
		},
		nextID: func(prefix string) string {
			return state.UniqueID(prefix)
		},
		execute: func(ctx context.Context, runtime *appruntime.MainRuntime, req appruntime.ExecutionRequest) (*appruntime.ExecutionResult, error) {
			return runtime.Execute(ctx, req)
		},
		stream: func(ctx context.Context, runtime *appruntime.MainRuntime, req appruntime.ExecutionRequest, onChunk func(string)) (*appruntime.ExecutionResult, error) {
			return runtime.Stream(ctx, req, onChunk)
		},
	}
}

func (m *Manager) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session creation now requires registered org/project/workspace via request path")
	}
	source := firstNonEmpty(strings.TrimSpace(req.Options.Source), "api")

	if !req.Options.Resume {
		if _, err := m.sessions.EnqueueTurn(sessionID); err == nil {
			m.appendEvent("session.queue.updated", sessionID, map[string]any{"queue_mode": "fifo", "source": source})
		}
	}
	if _, err := m.sessions.SetPresence(sessionID, "typing", true); err == nil {
		m.appendEvent("session.typing", sessionID, map[string]any{"typing": true, "source": source})
	}

	eventName := "chat.started"
	if req.Options.Resume {
		eventName = "chat.resumed"
	}
	m.appendEvent(eventName, sessionID, map[string]any{"message": req.Message, "source": source})

	session, ok := m.sessions.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	agentName, orgID, projectID, workspaceID := state.SessionExecutionTarget(session)
	targetRuntime, err := m.runtimes.GetOrCreate(agentName, orgID, projectID, workspaceID)
	if err != nil {
		failedSession := m.failRun(sessionID, req.Message, source, req.Options.Resume, session, err)
		return &RunResult{Session: failedSession}, err
	}

	if result, handled, err := m.tryDirectDesktopOpen(ctx, session, targetRuntime, ApprovalContext{
		Title:   req.Title,
		Message: req.Message,
		Source:  source,
	}, req.Options.Resume); handled {
		if err != nil {
			if errors.Is(err, ErrTaskWaitingApproval) {
				m.updateSessionApprovalPresence(sessionID, "")
			} else {
				failedSession := m.failRun(sessionID, req.Message, source, req.Options.Resume, session, err)
				if result == nil {
					result = &RunResult{}
				}
				result.Session = failedSession
			}
			return result, err
		}
		return result, nil
	}

	execResult, err := targetRuntime.Execute(ctx, appruntime.ExecutionRequest{
		Input:                req.Message,
		History:              session.History,
		ReplaceHistory:       true,
		SessionID:            sessionID,
		Channel:              firstNonEmpty(strings.TrimSpace(req.Options.Channel), "api"),
		AgentApprovalHook:    m.ToolApprovalHook(session, targetRuntime.Config, ApprovalContext{Title: req.Title, Message: req.Message, Source: source}),
		ApprovalResumeState:  req.Options.ApprovalResumeState,
		ProtocolApprovalHook: m.ProtocolApprovalHook(session, targetRuntime.Config, ApprovalContext{Title: req.Title, Message: req.Message, Source: source}),
	})

	response := ""
	persistedSession := session
	if execResult != nil {
		response = execResult.Output
	}
	if err != nil {
		if errors.Is(err, ErrTaskWaitingApproval) {
			var pauseErr *agent.ApprovalPauseError
			if errors.As(err, &pauseErr) {
				m.persistApprovalResumeState(session, pauseErr)
			}
			if pendingSession, persistErr := m.sessions.EnsurePendingUserMessage(sessionID, req.Message); persistErr == nil {
				persistedSession = pendingSession
			}
			if execResult != nil && len(execResult.ToolActivities) > 0 && persistedSession != nil {
				m.RecordToolActivities(persistedSession, execResult.ToolActivities)
			}
			m.updateSessionApprovalPresence(sessionID, "")
		} else {
			if fallback, completed := agent.ToolActivitiesCompletionMessage(execResultToolActivities(execResult), err); completed {
				updatedSession, finishErr := m.completeRunWithResponse(sessionID, req.Message, source, req.Options.Resume, persistedSession, fallback, execResultToolActivities(execResult))
				if finishErr != nil {
					failedSession := m.failRun(sessionID, req.Message, source, req.Options.Resume, persistedSession, finishErr)
					return &RunResult{Response: fallback, Session: failedSession}, finishErr
				}
				return &RunResult{Response: fallback, Session: updatedSession}, nil
			}
			persistedSession = m.failRun(sessionID, req.Message, source, req.Options.Resume, persistedSession, err)
		}
		return &RunResult{Response: response, Session: persistedSession}, err
	}

	updatedSession, err := m.completeRunWithResponse(sessionID, req.Message, source, req.Options.Resume, session, response, execResultToolActivities(execResult))
	if err != nil {
		failedSession := m.failRun(sessionID, req.Message, source, req.Options.Resume, session, err)
		return &RunResult{Response: response, Session: failedSession}, err
	}
	return &RunResult{Response: response, Session: updatedSession}, nil
}

func (m *Manager) RunChannel(ctx context.Context, req ChannelRunRequest) (*RunResult, error) {
	session, targetRuntime, err := m.beginChannelExecution(req)
	if err != nil {
		return nil, err
	}

	response := ""
	activities := []agent.ToolActivity(nil)
	execReq := appruntime.ExecutionRequest{
		Input:                req.Message,
		History:              session.History,
		ReplaceHistory:       true,
		SessionID:            req.SessionID,
		Channel:              req.Source,
		AgentApprovalHook:    m.ToolApprovalHook(session, targetRuntime.Config, ApprovalContext{Message: req.Message, Source: req.Source}),
		ProtocolApprovalHook: m.ProtocolApprovalHook(session, targetRuntime.Config, ApprovalContext{Message: req.Message, Source: req.Source}),
	}
	if req.Streaming {
		execResult, streamErr := m.stream(ctx, targetRuntime, execReq, func(chunk string) {
			if req.OnChunk != nil {
				req.OnChunk(chunk)
			}
		})
		if execResult != nil {
			response = execResult.Output
			activities = execResult.ToolActivities
		}
		if streamErr != nil {
			return m.handleChannelExecutionError(req, session, response, activities, streamErr)
		}
	} else {
		execResult, execErr := m.execute(ctx, targetRuntime, execReq)
		if execResult != nil {
			response = execResult.Output
			activities = execResult.ToolActivities
		}
		if execErr != nil {
			return m.handleChannelExecutionError(req, session, response, activities, execErr)
		}
	}

	updatedSession, err := m.finishChannelExecution(req, response, activities)
	if err != nil {
		return &RunResult{Response: response, Session: session}, err
	}
	return &RunResult{Response: response, Session: updatedSession}, nil
}

func (m *Manager) handleChannelExecutionError(req ChannelRunRequest, session *state.Session, response string, activities []agent.ToolActivity, runErr error) (*RunResult, error) {
	persistedSession := session
	if errors.Is(runErr, ErrTaskWaitingApproval) {
		var pauseErr *agent.ApprovalPauseError
		if errors.As(runErr, &pauseErr) {
			m.persistApprovalResumeState(session, pauseErr)
		}
		if pendingSession, persistErr := m.sessions.EnsurePendingUserMessage(req.SessionID, req.Message); persistErr == nil {
			persistedSession = pendingSession
		}
		if len(activities) > 0 && persistedSession != nil {
			m.RecordToolActivities(persistedSession, activities)
		}
		m.updateSessionApprovalPresence(req.SessionID, "")
		return &RunResult{Response: response, Session: persistedSession}, runErr
	}

	if fallback, completed := agent.ToolActivitiesCompletionMessage(activities, runErr); completed {
		updatedSession, err := m.finishChannelExecution(req, fallback, activities)
		if err != nil {
			return &RunResult{Response: fallback, Session: persistedSession}, err
		}
		return &RunResult{Response: fallback, Session: updatedSession}, nil
	}

	persistedSession = m.failRun(req.SessionID, req.Message, req.Source, false, persistedSession, runErr)
	return &RunResult{Response: response, Session: persistedSession}, runErr
}

func (m *Manager) ToolApprovalHook(session *state.Session, cfg *config.Config, meta ApprovalContext) agent.ToolApprovalHook {
	if m == nil || m.approvals == nil || cfg == nil || session == nil {
		return nil
	}
	return func(ctx context.Context, tc agent.ToolCall) error {
		action, decision := sessionPermissionDecision(cfg, tc.Name, tc.Args)
		approved, err := m.requirePermissionApproval(session, cfg, meta, tc.Name, tc.Args, action, decision, tc.RequiresApproval)
		if err == nil && approved {
			tools.GrantPermissionAction(ctx, action)
			if action.Capability == tools.HostReviewedCapabilityDesktop {
				tools.GrantHostReviewedCapability(ctx, tools.HostReviewedCapabilityDesktop)
			}
		}
		return err
	}
}

func (m *Manager) ProtocolApprovalHook(session *state.Session, cfg *config.Config, meta ApprovalContext) tools.ToolApprovalHook {
	if m == nil || m.approvals == nil || cfg == nil || session == nil {
		return nil
	}
	return func(ctx context.Context, call tools.ToolApprovalCall) error {
		action, decision := sessionPermissionDecision(cfg, call.Name, call.Args)
		approved, err := m.requirePermissionApproval(session, cfg, meta, call.Name, call.Args, action, decision)
		if err == nil && approved {
			tools.GrantPermissionAction(ctx, action)
			if action.Capability == tools.HostReviewedCapabilityDesktop {
				tools.GrantHostReviewedCapability(ctx, tools.HostReviewedCapabilityDesktop)
			}
		}
		return err
	}
}

func (m *Manager) RequireToolApproval(session *state.Session, meta ApprovalContext, toolName string, args map[string]any, forceApproval ...bool) error {
	_, err := m.requireToolApproval(session, nil, meta, toolName, args, forceApproval...)
	return err
}

func (m *Manager) requirePermissionApproval(session *state.Session, cfg *config.Config, meta ApprovalContext, toolName string, args map[string]any, action tools.PermissionAction, decision tools.PermissionResult, forceApproval ...bool) (bool, error) {
	if len(forceApproval) > 0 {
		for _, force := range forceApproval {
			if force {
				decision.Decision = tools.DecisionAsk
				if strings.TrimSpace(decision.Reason) == "" {
					decision.Reason = "tool requested permission approval"
				}
				break
			}
		}
	}
	switch decision.Decision {
	case tools.DecisionAllow:
		if requiresExplicitToolApproval(forceApproval...) {
			return m.requireToolApproval(session, cfg, meta, toolName, args, true)
		}
		return false, nil
	case tools.DecisionDeny:
		if strings.TrimSpace(decision.Reason) == "" {
			decision.Reason = "operation denied by permissions policy"
		}
		return false, fmt.Errorf("%s", decision.Reason)
	case tools.DecisionAsk:
		return m.requireToolApproval(session, cfg, meta, toolName, args, true)
	default:
		return false, nil
	}
}

func (m *Manager) requireToolApproval(session *state.Session, cfg *config.Config, meta ApprovalContext, toolName string, args map[string]any, forceApproval ...bool) (bool, error) {
	if m == nil || session == nil {
		return false, nil
	}
	if !requiresToolApprovalNameOrFlag(toolName, forceApproval...) {
		return false, nil
	}

	workspaceID := state.SessionExecutionWorkspace(session)
	signaturePayload := map[string]any{
		"tool_name":  toolName,
		"args":       cloneAnyMap(args),
		"session_id": session.ID,
		"workspace":  workspaceID,
	}
	payload := cloneAnyMap(signaturePayload)
	payload["message"] = strings.TrimSpace(meta.Message)
	payload["title"] = strings.TrimSpace(meta.Title)
	payload["capability"] = sessionApprovalCapability(toolName)
	payload["grant_scope"] = "session"
	payload["grant_ttl_minutes"] = 15
	if cfg != nil {
		action, decision := sessionPermissionDecision(cfg, toolName, args)
		applySessionPermissionPayload(payload, cfg, action, decision)
	}
	if description := approvalDescription(toolName, args); description != "" {
		payload["description"] = description
	}
	if domain := approvalTargetDomain(toolName, args); domain != "" {
		payload["domain"] = domain
	}
	signature := approvalSignature(toolName, "tool_call", signaturePayload)
	desktopApprovalScope := desktopApprovalScopeForRuntime(cfg)
	for _, approval := range m.store.ListSessionApprovals(session.ID) {
		if !matchesSessionToolApproval(approval, signature, session.ID, workspaceID, toolName, args, m.nowFunc(), desktopApprovalScope) {
			continue
		}
		switch approval.Status {
		case "approved":
			return true, nil
		case "rejected":
			return false, fmt.Errorf("tool call rejected: %s", toolName)
		case "pending":
			m.updateSessionApprovalPresence(session.ID, toolName)
			return false, ErrTaskWaitingApproval
		}
	}

	approval, err := m.approvals.RequestWithSignature("", session.ID, 0, toolName, "tool_call", payload, signaturePayload)
	if err != nil {
		return false, err
	}
	m.updateSessionApprovalPresence(session.ID, toolName)
	m.appendEvent("approval.requested", session.ID, map[string]any{
		"approval_id": approval.ID,
		"tool_name":   toolName,
		"action":      "tool_call",
		"status":      approval.Status,
		"capability":  payload["capability"],
		"grant_scope": payload["grant_scope"],
		"source":      firstNonEmpty(strings.TrimSpace(meta.Source), "session"),
	})
	return false, ErrTaskWaitingApproval
}

func applySessionPermissionPayload(payload map[string]any, cfg *config.Config, action tools.PermissionAction, decision tools.PermissionResult) {
	if payload == nil || cfg == nil {
		return
	}
	payload["permission_kind"] = action.Kind
	payload["permission_reason"] = decision.Reason
	payload["sandbox_mode"] = cfg.Permissions.SandboxMode
	payload["approval_policy"] = cfg.Permissions.ApprovalPolicy
	payload["network_access"] = cfg.Permissions.NetworkAccess
	payload["desktop_access"] = cfg.Permissions.DesktopAccess
	if action.Capability != "" {
		payload["capability"] = action.Capability
	}
	switch action.Kind {
	case "read", "write", "delete":
		payload["target"] = action.Path
	case "execute":
		payload["target"] = action.CWD
		payload["command"] = action.Command
	case "network":
		payload["target"] = action.URL
	case "desktop":
		payload["target"] = "local desktop"
	}
}

func requiresToolApprovalNameOrFlag(name string, forceApproval ...bool) bool {
	for _, force := range forceApproval {
		if force {
			return true
		}
	}
	return taskrunner.RequiresToolApprovalName(name)
}

func requiresExplicitToolApproval(forceApproval ...bool) bool {
	for _, force := range forceApproval {
		if force {
			return true
		}
	}
	return false
}

func sessionPermissionDecision(cfg *config.Config, toolName string, args map[string]any) (tools.PermissionAction, tools.PermissionResult) {
	action := tools.PermissionActionForTool(toolName, args, sessionPermissionWorkingDir(cfg))
	if action.Capability == "" {
		action.Capability = sessionApprovalCapability(toolName)
	}
	engine := tools.NewPermissionEngine(sessionPermissionWorkingDir(cfg), tools.PermissionOptions{
		SandboxMode:    cfg.Permissions.SandboxMode,
		ApprovalPolicy: cfg.Permissions.ApprovalPolicy,
		NetworkAccess:  cfg.Permissions.NetworkAccess,
		DesktopAccess:  cfg.Permissions.DesktopAccess,
	})
	return action, engine.Decide(action)
}

func sessionPermissionWorkingDir(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	workingDir := strings.TrimSpace(cfg.Agent.WorkingDir)
	if workingDir == "" {
		workingDir = strings.TrimSpace(cfg.Agent.WorkDir)
	}
	return workingDir
}

func (m *Manager) ResumeApproved(ctx context.Context, approval *state.Approval) error {
	if m == nil || approval == nil {
		return nil
	}
	sessionID := strings.TrimSpace(approval.SessionID)
	if sessionID == "" {
		return nil
	}
	message, _ := approval.Payload["message"].(string)
	title, _ := approval.Payload["title"].(string)
	message = strings.TrimSpace(message)
	title = strings.TrimSpace(title)
	resumeState := decodeApprovalResumeState(approval.Payload["resume_state"])
	if message == "" {
		return nil
	}

	_, err := m.Run(ctx, RunRequest{
		SessionID: sessionID,
		Title:     title,
		Message:   message,
		Options: RunOptions{
			Source:              "approval_resume",
			Resume:              true,
			Channel:             "api",
			ApprovalResumeState: resumeState,
		},
	})
	return err
}

func (m *Manager) AppendToolActivity(sessionID string, activity state.ToolActivityRecord) {
	if m == nil || m.store == nil {
		return
	}
	activity.ID = m.nextID("tool")
	activity.SessionID = sessionID
	if activity.Timestamp.IsZero() {
		activity.Timestamp = m.nowFunc()
	}
	_ = m.store.AppendToolActivity(&activity)
	m.appendEvent("tool.activity", sessionID, map[string]any{
		"tool_name": activity.ToolName,
		"args":      activity.Args,
		"error":     activity.Error,
		"agent":     activity.Agent,
		"workspace": activity.Workspace,
	})
}

func (m *Manager) RecordToolActivities(session *state.Session, activities []agent.ToolActivity) {
	if m == nil || session == nil {
		return
	}
	agentName := state.SessionExecutionAgent(session)
	workspaceID := state.SessionExecutionWorkspace(session)
	for _, activity := range activities {
		m.AppendToolActivity(session.ID, state.ToolActivityRecord{
			ToolName:  activity.ToolName,
			Args:      activity.Args,
			Result:    activity.Result,
			Error:     activity.Error,
			Agent:     agentName,
			Workspace: workspaceID,
		})
	}
}

func (m *Manager) tryDirectDesktopOpen(ctx context.Context, session *state.Session, targetRuntime *appruntime.MainRuntime, meta ApprovalContext, resume bool) (*RunResult, bool, error) {
	request, ok := directDesktopOpenTarget(meta.Message, runtimeWorkingDir(targetRuntime))
	if !ok || session == nil || targetRuntime == nil {
		return nil, false, nil
	}
	ctx = tools.WithApprovalGrantScope(ctx)
	agentName := state.SessionExecutionAgent(session)
	workspaceID := state.SessionExecutionWorkspace(session)
	input := map[string]any{
		"target": request.Target,
		"kind":   request.Kind,
	}
	if request.Browser != "" {
		input["browser"] = request.Browser
	}
	approved, err := m.requireToolApproval(session, targetRuntime.Config, meta, "desktop_open", input)
	if err != nil {
		if errors.Is(err, ErrTaskWaitingApproval) {
			if pendingSession, persistErr := m.sessions.EnsurePendingUserMessage(session.ID, meta.Message); persistErr == nil {
				return &RunResult{Session: pendingSession}, true, err
			}
		}
		return &RunResult{Session: session}, true, err
	}
	if approved {
		tools.GrantHostReviewedCapability(ctx, tools.HostReviewedCapabilityDesktop)
	}

	beforeWindows, _ := m.listDirectDesktopOpenWindows(ctx, targetRuntime)
	result, err := targetRuntime.CallTool(ctx, "desktop_open", input)
	if err != nil {
		m.AppendToolActivity(session.ID, state.ToolActivityRecord{
			ToolName:  "desktop_open",
			Args:      cloneAnyMap(input),
			Error:     err.Error(),
			Agent:     agentName,
			Workspace: workspaceID,
		})
		return &RunResult{Session: session}, true, err
	}
	m.AppendToolActivity(session.ID, state.ToolActivityRecord{
		ToolName:  "desktop_open",
		Args:      cloneAnyMap(input),
		Result:    result,
		Agent:     agentName,
		Workspace: workspaceID,
	})

	verified, verificationResult, verificationErr := m.verifyDirectDesktopOpen(ctx, targetRuntime, beforeWindows, request.Kind)
	verificationArgs := map[string]any{"timeout_ms": 3000, "interval_ms": 250}
	if verificationErr == nil {
		verificationArgs["strategy"] = "desktop_wait_window_or_list_windows"
	} else {
		verificationArgs["strategy"] = "desktop_wait_window_or_list_windows"
	}
	if verificationErr != nil {
		m.AppendToolActivity(session.ID, state.ToolActivityRecord{
			ToolName:  "desktop_wait_window",
			Args:      cloneAnyMap(verificationArgs),
			Error:     verificationErr.Error(),
			Agent:     agentName,
			Workspace: workspaceID,
		})
	} else {
		m.AppendToolActivity(session.ID, state.ToolActivityRecord{
			ToolName:  "desktop_wait_window",
			Args:      cloneAnyMap(verificationArgs),
			Result:    verificationResult,
			Agent:     agentName,
			Workspace: workspaceID,
		})
	}

	response := directDesktopOpenResponse(request, verified, verificationErr)
	var updatedSession *state.Session
	if resume {
		updatedSession, err = m.sessions.AddAssistantMessage(session.ID, response)
	} else {
		updatedSession, err = m.sessions.AddExchange(session.ID, meta.Message, response)
	}
	if err != nil {
		return &RunResult{Response: response, Session: session}, true, err
	}
	if _, err := m.sessions.SetPresence(session.ID, "idle", false); err == nil {
		m.appendEvent("session.presence", session.ID, map[string]any{"presence": "idle", "source": firstNonEmpty(strings.TrimSpace(meta.Source), "api")})
	}
	m.appendEvent("chat.completed", session.ID, map[string]any{
		"message":         meta.Message,
		"response_length": len(response),
		"source":          firstNonEmpty(strings.TrimSpace(meta.Source), "api"),
	})
	return &RunResult{Response: response, Session: updatedSession}, true, nil
}

func (m *Manager) verifyDirectDesktopOpen(ctx context.Context, targetRuntime *appruntime.MainRuntime, before []desktopWindowSnapshot, kind string) (bool, string, error) {
	deadline := time.Now().Add(3 * time.Second)
	var lastResult string
	var lastErr error
	for {
		after, err := m.listDirectDesktopOpenWindows(ctx, targetRuntime)
		if err == nil {
			if directDesktopOpenVerified(before, after, kind) {
				return true, directDesktopOpenWindowsSummary(after, kind), nil
			}
			lastResult = directDesktopOpenWindowsSummary(after, kind)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return false, lastResult, lastErr
			}
			return false, lastResult, fmt.Errorf("desktop browser window did not appear within verification timeout")
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, lastResult, ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) listDirectDesktopOpenWindows(ctx context.Context, targetRuntime *appruntime.MainRuntime) ([]desktopWindowSnapshot, error) {
	result, err := targetRuntime.CallTool(ctx, "desktop_list_windows", map[string]any{})
	if err != nil {
		return nil, err
	}
	var windows []desktopWindowSnapshot
	if err := json.Unmarshal([]byte(result), &windows); err != nil {
		return nil, err
	}
	return windows, nil
}

func (m *Manager) beginChannelExecution(req ChannelRunRequest) (*state.Session, *appruntime.MainRuntime, error) {
	if _, err := m.sessions.EnqueueTurn(req.SessionID); err == nil {
		m.appendEvent("session.queue.updated", req.SessionID, map[string]any{
			"queue_mode":   req.QueueMode,
			"source":       req.Source,
			"reply_target": req.Meta["reply_target"],
		})
	}

	if _, err := m.sessions.SetUserMapping(
		req.SessionID,
		req.Meta["user_id"],
		firstNonEmpty(req.Meta["username"], req.Meta["user_name"]),
		req.Meta["reply_target"],
		req.Meta["thread_id"],
		channelTransportMeta(req.Meta),
	); err == nil {
		m.appendEvent("session.user_mapped", req.SessionID, map[string]any{
			"source":       req.Source,
			"user_id":      req.Meta["user_id"],
			"user_name":    firstNonEmpty(req.Meta["username"], req.Meta["user_name"]),
			"reply_target": req.Meta["reply_target"],
		})
	}

	if _, err := m.sessions.SetPresence(req.SessionID, "typing", true); err == nil {
		m.appendEvent("session.typing", req.SessionID, map[string]any{
			"typing":  true,
			"source":  req.Source,
			"user_id": req.Meta["user_id"],
		})
	}

	startedPayload := channelMetaPayload(map[string]any{
		"message": req.Message,
		"source":  req.Source,
	}, req.Meta)
	if req.Streaming {
		startedPayload["streaming"] = true
	}
	m.appendEvent("chat.started", req.SessionID, startedPayload)

	session, ok := m.sessions.Get(req.SessionID)
	if !ok {
		return nil, nil, fmt.Errorf("session not found: %s", req.SessionID)
	}
	if m.runtimes == nil {
		return nil, nil, fmt.Errorf("runtime pool not initialized")
	}

	agentName, orgID, projectID, workspaceID := state.SessionExecutionTarget(session)
	targetRuntime, err := m.runtimes.GetOrCreate(agentName, orgID, projectID, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	return session, targetRuntime, nil
}

func (m *Manager) finishChannelExecution(req ChannelRunRequest, response string, activities []agent.ToolActivity) (*state.Session, error) {
	updatedSession, err := m.sessions.AddExchange(req.SessionID, req.Message, response)
	if err != nil {
		return nil, err
	}
	if updatedSession.ReplyBack {
		m.appendEvent("session.reply_back", req.SessionID, map[string]any{
			"enabled":      true,
			"source":       req.Source,
			"reply_target": req.Meta["reply_target"],
		})
	}
	if _, err := m.sessions.SetPresence(req.SessionID, "idle", false); err == nil {
		m.appendEvent("session.presence", req.SessionID, map[string]any{
			"presence": "idle",
			"source":   req.Source,
			"user_id":  req.Meta["user_id"],
		})
	}
	if len(activities) > 0 {
		m.RecordToolActivities(updatedSession, activities)
	}

	completedPayload := channelMetaPayload(map[string]any{
		"message":         req.Message,
		"response_length": len(response),
		"source":          req.Source,
	}, req.Meta)
	if req.Streaming {
		completedPayload["streaming"] = true
	}
	m.appendEvent("chat.completed", req.SessionID, completedPayload)
	return updatedSession, nil
}

func (m *Manager) completeRunWithResponse(sessionID string, message string, source string, resume bool, session *state.Session, response string, activities []agent.ToolActivity) (*state.Session, error) {
	var updatedSession *state.Session
	var err error
	if resume {
		updatedSession, err = m.sessions.AddAssistantMessage(sessionID, response)
	} else {
		updatedSession, err = m.sessions.AddExchange(sessionID, message, response)
	}
	if err != nil {
		return nil, err
	}
	if _, err := m.sessions.SetPresence(sessionID, "idle", false); err == nil {
		m.appendEvent("session.presence", sessionID, map[string]any{"presence": "idle", "source": source})
	}
	if len(activities) > 0 {
		m.RecordToolActivities(updatedSession, activities)
	}
	m.appendEvent("chat.completed", sessionID, map[string]any{"message": message, "response_length": len(response), "source": source})
	return updatedSession, nil
}

func execResultToolActivities(execResult *appruntime.ExecutionResult) []agent.ToolActivity {
	if execResult == nil {
		return nil
	}
	return execResult.ToolActivities
}

func (m *Manager) updateSessionApprovalPresence(sessionID string, toolName string) {
	m.updateSessionPresence(sessionID, "waiting_approval", false)
	m.appendEvent("session.presence", sessionID, map[string]any{
		"presence":  "waiting_approval",
		"tool_name": strings.TrimSpace(toolName),
		"source":    "approval",
	})
}

func (m *Manager) updateSessionPresence(sessionID string, presence string, typing bool) {
	if m == nil || m.sessions == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	_, _ = m.sessions.SetPresence(sessionID, presence, typing)
}

func (m *Manager) appendEvent(eventType string, sessionID string, payload map[string]any) {
	if m == nil || m.events == nil {
		return
	}
	m.events.AppendEvent(eventType, sessionID, payload)
}

func (m *Manager) failRun(sessionID string, message string, source string, resume bool, session *state.Session, runErr error) *state.Session {
	persistedSession := session
	if m == nil || m.sessions == nil || strings.TrimSpace(sessionID) == "" {
		return persistedSession
	}
	if !resume {
		if pendingSession, err := m.sessions.EnsurePendingUserMessage(sessionID, message); err == nil {
			persistedSession = pendingSession
		}
	}
	if runErr != nil {
		if failedMessageSession, err := m.sessions.AddAssistantMessage(sessionID, agent.FailureAssistantMessage(runErr)); err == nil {
			persistedSession = failedMessageSession
		}
	}
	if failedSession, err := m.sessions.FailTurn(sessionID); err == nil {
		persistedSession = failedSession
	} else {
		m.updateSessionPresence(sessionID, "idle", false)
	}
	if runErr != nil {
		m.appendEvent("chat.failed", sessionID, map[string]any{
			"message": message,
			"error":   runErr.Error(),
			"source":  source,
		})
	}
	return persistedSession
}

func matchesSessionToolApproval(approval *state.Approval, signature string, sessionID string, workspace string, toolName string, args map[string]any, now time.Time, desktopApprovalScope string) bool {
	if approval == nil {
		return false
	}
	if approval.Action != "tool_call" || approval.SessionID != sessionID {
		return false
	}
	if approval.Signature == signature {
		return sessionToolApprovalGrantAllowsMatch(approval, now)
	}
	payloadWorkspace, _ := approval.Payload["workspace"].(string)
	if strings.TrimSpace(payloadWorkspace) != strings.TrimSpace(workspace) {
		return false
	}
	if desktopApprovalScopeForValue(desktopApprovalScope) == "capability" && matchesSessionCapabilityApproval(approval, toolName, now) {
		return true
	}
	if approval.ToolName != toolName {
		return false
	}
	payloadArgs, ok := approval.Payload["args"]
	if !ok {
		return false
	}
	expectedArgs, err := json.Marshal(cloneAnyMap(args))
	if err != nil {
		return false
	}
	actualArgs, err := json.Marshal(payloadArgs)
	if err != nil {
		return false
	}
	return string(actualArgs) == string(expectedArgs) && sessionToolApprovalGrantAllowsMatch(approval, now)
}

func desktopApprovalScopeForRuntime(cfg *config.Config) string {
	if cfg == nil {
		return "capability"
	}
	return desktopApprovalScopeForValue(cfg.Security.DesktopApprovalScope)
}

func desktopApprovalScopeForValue(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "tool_call":
		return "tool_call"
	default:
		return "capability"
	}
}

func matchesSessionCapabilityApproval(approval *state.Approval, toolName string, now time.Time) bool {
	if approval == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(approval.Status), "approved") {
		return false
	}
	capability := sessionApprovalCapability(toolName)
	if capability == "" {
		return false
	}
	payloadCapability, _ := approval.Payload["capability"].(string)
	if strings.TrimSpace(strings.ToLower(payloadCapability)) != capability {
		return false
	}
	payloadScope, _ := approval.Payload["grant_scope"].(string)
	if payloadScope != "" && !strings.EqualFold(strings.TrimSpace(payloadScope), "session") {
		return false
	}
	return sessionApprovalGrantStillValid(approval, now)
}

func sessionToolApprovalGrantAllowsMatch(approval *state.Approval, now time.Time) bool {
	if approval == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(approval.Status), "approved") {
		return true
	}
	if approvalTTLMinutes(approval.Payload["grant_ttl_minutes"]) <= 0 {
		return true
	}
	return sessionApprovalGrantStillValid(approval, now)
}

func sessionApprovalGrantStillValid(approval *state.Approval, now time.Time) bool {
	if approval == nil || approval.Payload == nil {
		return false
	}
	ttlMinutes := approvalTTLMinutes(approval.Payload["grant_ttl_minutes"])
	if ttlMinutes <= 0 {
		return true
	}
	base := approval.RequestedAt
	if strings.TrimSpace(approval.ResolvedAt) != "" {
		if resolvedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(approval.ResolvedAt)); err == nil {
			base = resolvedAt
		}
	}
	if base.IsZero() || now.IsZero() {
		return true
	}
	return now.Before(base.Add(time.Duration(ttlMinutes) * time.Minute))
}

func approvalTTLMinutes(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	case string:
		var out int
		_, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &out)
		if err == nil {
			return out
		}
	}
	return 0
}

func sessionApprovalCapability(toolName string) string {
	name := strings.TrimSpace(strings.ToLower(toolName))
	if name == "desktop_plan" || strings.HasPrefix(name, "desktop_") || strings.HasPrefix(name, "computer_") {
		return tools.HostReviewedCapabilityDesktop
	}
	return ""
}

func approvalDescription(toolName string, args map[string]any) string {
	name := strings.TrimSpace(strings.ToLower(toolName))
	if name == "desktop_open" {
		target, _ := args["target"].(string)
		kind, _ := args["kind"].(string)
		target = strings.TrimSpace(target)
		if target == "" {
			return "Open a desktop target"
		}
		if strings.EqualFold(strings.TrimSpace(kind), "url") {
			return fmt.Sprintf("Open a visible desktop browser and navigate to %s", target)
		}
		return fmt.Sprintf("Open desktop target %s", target)
	}
	if name == "computer_action" {
		if summary := computerActionApprovalSummary(args); summary != "" {
			return "Control the local desktop: " + summary
		}
	}
	if name == "computer_observe" {
		return "Observe the local desktop with screenshot and window metadata"
	}
	if strings.HasPrefix(name, "computer_") || strings.HasPrefix(name, "desktop_") || name == "desktop_plan" {
		return "Control the local desktop for this session"
	}
	return ""
}

func computerActionApprovalSummary(args map[string]any) string {
	names := computerActionApprovalNames(args)
	if len(names) == 0 {
		return ""
	}
	if len(names) == 1 {
		return names[0]
	}
	return fmt.Sprintf("%d actions (%s)", len(names), strings.Join(names, ", "))
}

func computerActionApprovalNames(args map[string]any) []string {
	if len(args) == 0 {
		return nil
	}
	if actions, ok := args["actions"].([]any); ok {
		names := make([]string, 0, len(actions))
		for _, item := range actions {
			actionMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name := firstNonEmptyApprovalString(actionMap["type"], actionMap["action"])
			if name != "" {
				names = append(names, name)
			}
		}
		return names
	}
	if action := firstNonEmptyApprovalString(args["action"]); action != "" {
		return []string{action}
	}
	if action := firstNonEmptyApprovalString(args["type"]); action != "" {
		return []string{action}
	}
	return nil
}

func firstNonEmptyApprovalString(values ...any) string {
	for _, value := range values {
		if value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" {
			return text
		}
	}
	return ""
}

func approvalTargetDomain(toolName string, args map[string]any) string {
	if strings.TrimSpace(strings.ToLower(toolName)) != "desktop_open" {
		return ""
	}
	target, _ := args["target"].(string)
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ToLower(parsed.Hostname()))
}

func (m *Manager) persistApprovalResumeState(session *state.Session, pauseErr *agent.ApprovalPauseError) {
	if m == nil || m.store == nil || session == nil || pauseErr == nil {
		return
	}
	pending := m.findPendingToolApproval(session, pauseErr.State.PendingTool.Name, pauseErr.State.PendingTool.Args)
	if pending == nil {
		return
	}
	if pending.Payload == nil {
		pending.Payload = map[string]any{}
	}
	pending.Payload["resume_state"] = encodeApprovalResumeState(pauseErr.State)
	_ = m.store.UpdateApproval(pending)
}

func (m *Manager) findPendingToolApproval(session *state.Session, toolName string, args map[string]any) *state.Approval {
	if m == nil || m.store == nil || session == nil {
		return nil
	}
	workspaceID := state.SessionExecutionWorkspace(session)
	signaturePayload := map[string]any{
		"tool_name":  toolName,
		"args":       cloneAnyMap(args),
		"session_id": session.ID,
		"workspace":  workspaceID,
	}
	signature := approvalSignature(toolName, "tool_call", signaturePayload)
	for _, approval := range m.store.ListSessionApprovals(session.ID) {
		if approval == nil || approval.Status != "pending" {
			continue
		}
		if matchesSessionToolApproval(approval, signature, session.ID, workspaceID, toolName, args, m.nowFunc(), "tool_call") {
			return approval
		}
	}
	return nil
}

func encodeApprovalResumeState(state agent.ApprovalResumeState) map[string]any {
	encoded := map[string]any{}
	data, err := json.Marshal(state)
	if err != nil {
		return encoded
	}
	_ = json.Unmarshal(data, &encoded)
	return encoded
}

func decodeApprovalResumeState(raw any) *agent.ApprovalResumeState {
	if raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var state agent.ApprovalResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	if strings.TrimSpace(state.PendingTool.Name) == "" {
		return nil
	}
	return &state
}

func approvalSignature(toolName string, action string, payload map[string]any) string {
	encoded, _ := json.Marshal(payload)
	return fmt.Sprintf("%s|%s|%s", strings.TrimSpace(toolName), strings.TrimSpace(action), string(encoded))
}

type desktopWindowSnapshot struct {
	Title       string `json:"title,omitempty"`
	ProcessName string `json:"process_name,omitempty"`
	Handle      int    `json:"handle,omitempty"`
	IsFocused   bool   `json:"is_focused,omitempty"`
}

func directDesktopOpenWindowAppeared(before []desktopWindowSnapshot, after []desktopWindowSnapshot, kind string) bool {
	beforeByHandle := make(map[int]desktopWindowSnapshot, len(before))
	for _, item := range before {
		if item.Handle > 0 {
			beforeByHandle[item.Handle] = item
		}
	}
	for _, item := range after {
		if !isRelevantDesktopOpenWindowSnapshot(item, kind) {
			continue
		}
		if item.Handle > 0 {
			if _, ok := beforeByHandle[item.Handle]; !ok {
				return true
			}
		}
		if item.IsFocused {
			beforeItem, ok := beforeByHandle[item.Handle]
			if !ok || !beforeItem.IsFocused {
				return true
			}
		}
	}
	return false
}

func directDesktopOpenVerified(before []desktopWindowSnapshot, after []desktopWindowSnapshot, kind string) bool {
	if directDesktopOpenWindowAppeared(before, after, kind) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(kind), "url") {
		return false
	}
	beforeHadRelevantWindow := false
	for _, item := range before {
		if isRelevantDesktopOpenWindowSnapshot(item, kind) {
			beforeHadRelevantWindow = true
			break
		}
	}
	if !beforeHadRelevantWindow {
		return false
	}
	for _, item := range after {
		if isRelevantDesktopOpenWindowSnapshot(item, kind) {
			return true
		}
	}
	return false
}

func directDesktopOpenWindowsSummary(windows []desktopWindowSnapshot, kind string) string {
	items := make([]string, 0, len(windows))
	for _, item := range windows {
		if !isRelevantDesktopOpenWindowSnapshot(item, kind) {
			continue
		}
		descriptor := firstNonEmpty(strings.TrimSpace(item.Title), strings.TrimSpace(item.ProcessName), fmt.Sprintf("window-%d", item.Handle))
		if item.IsFocused {
			descriptor += " (focused)"
		}
		items = append(items, descriptor)
		if len(items) >= 5 {
			break
		}
	}
	if len(items) == 0 {
		return ""
	}
	return strings.Join(items, ", ")
}

func isRelevantDesktopOpenWindowSnapshot(item desktopWindowSnapshot, kind string) bool {
	if strings.EqualFold(strings.TrimSpace(kind), "url") {
		return isBrowserWindowSnapshot(item)
	}
	return strings.TrimSpace(item.Title) != "" || item.Handle > 0 || strings.TrimSpace(item.ProcessName) != ""
}

func isBrowserWindowSnapshot(item desktopWindowSnapshot) bool {
	process := strings.ToLower(strings.TrimSpace(item.ProcessName))
	title := strings.ToLower(strings.TrimSpace(item.Title))
	for _, candidate := range []string{"chrome", "msedge", "firefox", "brave", "opera"} {
		if process == candidate || strings.Contains(title, candidate) {
			return true
		}
	}
	return false
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for k, v := range input {
		result[k] = v
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func channelMetaPayload(base map[string]any, meta map[string]string) map[string]any {
	payload := make(map[string]any, len(base)+len(meta))
	for k, v := range base {
		payload[k] = v
	}
	for k, v := range meta {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			payload[k] = trimmed
		}
	}
	return payload
}

func channelTransportMeta(meta map[string]string) map[string]string {
	transportMeta := map[string]string{}
	for _, key := range []string{"channel_id", "chat_id", "guild_id", "attachment_count"} {
		if v := strings.TrimSpace(meta[key]); v != "" {
			transportMeta[key] = v
		}
	}
	return transportMeta
}

type directDesktopOpenRequest struct {
	Target  string
	Kind    string
	Browser string
}

func directDesktopOpenTarget(message string, workingDir string) (directDesktopOpenRequest, bool) {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return directDesktopOpenRequest{}, false
	}
	lower := strings.ToLower(msg)
	hasOpenIntent := strings.Contains(lower, "open") || strings.Contains(lower, "visit") || strings.Contains(msg, "打开") || strings.Contains(msg, "访问")
	if !hasOpenIntent {
		return directDesktopOpenRequest{}, false
	}
	browser := ""
	if strings.Contains(lower, "edge") || strings.Contains(lower, "msedge") {
		browser = "edge"
	}
	if target := directDesktopOpenExplicitTarget(msg); target != "" {
		if isDirectDesktopOpenURL(target) {
			return directDesktopOpenRequest{Target: target, Kind: "url", Browser: browser}, true
		}
		if targetURL := directDesktopOpenEmbeddedURL(target); targetURL != "" {
			return directDesktopOpenRequest{Target: targetURL, Kind: "url", Browser: browser}, true
		}
		if isDirectDesktopOpenFileCandidate(target, workingDir) {
			return directDesktopOpenRequest{Target: resolveDirectDesktopOpenFileTarget(target, workingDir), Kind: "file", Browser: browser}, true
		}
		if isDirectDesktopOpenAppCandidate(target) {
			return directDesktopOpenRequest{Target: target, Kind: "app", Browser: browser}, true
		}
	}
	for _, candidate := range directDesktopOpenCandidates(msg) {
		if isDirectDesktopOpenURL(candidate) {
			return directDesktopOpenRequest{Target: candidate, Kind: "url", Browser: browser}, true
		}
		if isDirectDesktopOpenFileCandidate(candidate, workingDir) {
			return directDesktopOpenRequest{Target: resolveDirectDesktopOpenFileTarget(candidate, workingDir), Kind: "file", Browser: browser}, true
		}
	}
	if target := resolveDirectDesktopOpenGeneratedPageTarget(msg, workingDir); target != "" {
		return directDesktopOpenRequest{Target: target, Kind: "file", Browser: browser}, true
	}
	if target := directDesktopOpenAppTarget(msg); target != "" {
		return directDesktopOpenRequest{Target: target, Kind: "app", Browser: browser}, true
	}
	return directDesktopOpenRequest{}, false
}

func directDesktopOpenEmbeddedURL(value string) string {
	for _, candidate := range directDesktopOpenCandidates(value) {
		if isDirectDesktopOpenURL(candidate) {
			return candidate
		}
	}
	return ""
}

func resolveDirectDesktopOpenGeneratedPageTarget(message string, workingDir string) string {
	if strings.TrimSpace(workingDir) == "" {
		return ""
	}
	lower := strings.ToLower(strings.TrimSpace(message))
	hasGeneratedPageReference := strings.Contains(lower, "generated page") ||
		strings.Contains(lower, "generated webpage") ||
		strings.Contains(lower, "page you generated") ||
		strings.Contains(lower, "created page") ||
		strings.Contains(lower, "created webpage") ||
		strings.Contains(lower, "page you created") ||
		strings.Contains(message, "生成的页面") ||
		strings.Contains(message, "生成的网页") ||
		strings.Contains(message, "你生成的页面") ||
		strings.Contains(message, "刚生成的页面") ||
		strings.Contains(message, "创建的页面") ||
		strings.Contains(message, "创建的网页") ||
		strings.Contains(message, "你创建的页面") ||
		strings.Contains(message, "刚创建的页面") ||
		strings.Contains(message, "本地页面") ||
		strings.Contains(message, "本地网页") ||
		strings.Contains(message, "网页文件") ||
		strings.Contains(message, "预览页面")
	if !hasGeneratedPageReference {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(workingDir, "*.htm*"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	type candidate struct {
		name    string
		modTime time.Time
	}
	candidates := make([]candidate, 0, len(matches))
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		candidates = append(candidates, candidate{name: filepath.Base(path), modTime: info.ModTime()})
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.SliceStable(candidates, func(i int, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	return candidates[0].name
}

func directDesktopOpenExplicitTarget(message string) string {
	trimmedMessage := strings.TrimSpace(message)
	lower := strings.ToLower(trimmedMessage)
	for _, phrase := range []string{"open ", "launch ", "start ", "visit ", "打开", "启动", "访问"} {
		idx := strings.Index(lower, phrase)
		if idx < 0 {
			continue
		}
		start := idx + len(phrase)
		if phrase == "打开" || phrase == "启动" || phrase == "访问" {
			start = strings.Index(trimmedMessage, phrase) + len(phrase)
		}
		target := strings.TrimSpace(trimmedMessage[start:])
		for _, prefix := range []string{"帮我", "请", "帮忙", "给我"} {
			target = strings.TrimPrefix(target, prefix)
			target = strings.TrimSpace(target)
		}
		target = strings.TrimPrefix(target, "一下")
		target = strings.TrimPrefix(target, "这个")
		target = strings.TrimSpace(target)
		target = strings.Trim(target, " \t\r\n,，。！？；:?)（）[]【】>\"'")
		return target
	}
	return ""
}

func directDesktopOpenCandidates(message string) []string {
	fields := strings.Fields(message)
	candidates := make([]string, 0, len(fields))
	for _, field := range fields {
		trimmed := strings.Trim(field, " \t\r\n,，。！？；:?)（）[]【】>\"'")
		if trimmed != "" {
			candidates = append(candidates, trimmed)
		}
	}
	return candidates
}

func isDirectDesktopOpenURL(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func isDirectDesktopOpenFileCandidate(value string, workingDir string) bool {
	value = strings.TrimSpace(value)
	if value == "" || isDirectDesktopOpenURL(value) {
		return false
	}
	if isInvalidDirectDesktopOpenTarget(value) {
		return false
	}
	if strings.ContainsAny(value, `/\`) {
		return true
	}
	ext := strings.ToLower(filepath.Ext(value))
	if ext == "" {
		return resolveDirectDesktopOpenFileTarget(value, workingDir) != value
	}
	for _, known := range []string{
		".html", ".htm", ".pdf", ".txt", ".md", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg",
		".css", ".js", ".json", ".csv", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
	} {
		if ext == known {
			return true
		}
	}
	resolved := value
	if strings.TrimSpace(workingDir) != "" && !filepath.IsAbs(value) {
		resolved = filepath.Join(workingDir, value)
	}
	return filepath.Ext(resolved) != ""
}

func isInvalidDirectDesktopOpenTarget(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	onlySeparators := true
	for _, r := range value {
		if r != '\\' && r != '/' {
			onlySeparators = false
			break
		}
	}
	if onlySeparators {
		return true
	}
	cleaned := filepath.Clean(value)
	return cleaned == "." || cleaned == string(filepath.Separator)
}

func resolveDirectDesktopOpenFileTarget(value string, workingDir string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.Ext(value) != "" || strings.TrimSpace(workingDir) == "" {
		return value
	}
	for _, ext := range []string{".html", ".htm", ".pdf", ".txt", ".md", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg"} {
		candidate := filepath.Join(workingDir, value+ext)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return value + ext
		}
	}
	return value
}

func directDesktopOpenAppTarget(message string) string {
	target := directDesktopOpenExplicitTarget(message)
	if isDirectDesktopOpenAppCandidate(target) {
		return target
	}
	return ""
}

func isDirectDesktopOpenAppCandidate(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, " ") || isDirectDesktopOpenURL(value) || isDirectDesktopOpenFileCandidate(value, "") {
		return false
	}
	lower := strings.ToLower(value)
	for _, candidate := range []string{
		"notepad", "calc", "calculator", "mspaint", "paint", "cmd", "powershell", "pwsh",
		"edge", "msedge", "chrome", "firefox", "code", "vscode", "steam",
	} {
		if lower == candidate || lower == candidate+".exe" {
			return true
		}
	}
	return strings.HasSuffix(lower, ".exe")
}

func directDesktopOpenResponse(request directDesktopOpenRequest, verified bool, verificationErr error) string {
	target := strings.TrimSpace(request.Target)
	kindLabel := directDesktopOpenKindLabel(request.Kind)
	if verified {
		return fmt.Sprintf("已处理：\n- 已打开%s %s。\n\n已验证：\n- 已确认桌面窗口出现。\n\n未确认/阻塞：\n- 无", kindLabel, target)
	}
	reason := "桌面窗口验证超时或未返回匹配窗口。"
	if verificationErr != nil && strings.TrimSpace(verificationErr.Error()) != "" {
		reason = verificationErr.Error()
	}
	return fmt.Sprintf("已处理：\n- 已请求打开%s %s。\n\n已验证：\n- 未能确认桌面窗口已出现。\n\n未确认/阻塞：\n- %s", kindLabel, target, reason)
}

func directDesktopOpenKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "url":
		return "网页"
	case "file":
		return "文件"
	case "app":
		return "应用"
	default:
		return "目标"
	}
}

func runtimeWorkingDir(runtime *appruntime.MainRuntime) string {
	if runtime == nil {
		return ""
	}
	if strings.TrimSpace(runtime.WorkingDir) != "" {
		return strings.TrimSpace(runtime.WorkingDir)
	}
	if runtime.Config != nil {
		return sessionPermissionWorkingDir(runtime.Config)
	}
	return ""
}
