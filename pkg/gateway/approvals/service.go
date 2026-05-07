package approvals

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sessionrunner "github.com/1024XEngineer/anyclaw/pkg/runtime/sessionrunner"
	taskrunner "github.com/1024XEngineer/anyclaw/pkg/runtime/taskrunner"
	"github.com/1024XEngineer/anyclaw/pkg/state"
)

type Service struct {
	Store                *state.Store
	Sessions             *state.SessionManager
	Tasks                *taskrunner.Manager
	Runner               *sessionrunner.Manager
	AppendEvent          func(eventType string, sessionID string, payload map[string]any)
	RecordTaskCompletion func(result *taskrunner.ExecutionResult, source string)
	SessionResumeTimeout time.Duration
}

const defaultSessionResumeTimeout = 90 * time.Second

func (svc Service) HandleResolved(updated *state.Approval, approved bool, comment string) {
	if updated == nil {
		return
	}
	if updated.TaskID != "" {
		if approved {
			if svc.Tasks == nil {
				return
			}
			go func(taskID string) {
				result, runErr := svc.Tasks.Execute(context.Background(), taskID)
				if runErr != nil {
					if errors.Is(runErr, taskrunner.ErrTaskWaitingApproval) {
						return
					}
					return
				}
				svc.recordTaskCompletion(result, "approval_resume")
			}(updated.TaskID)
			return
		}
		if svc.Tasks != nil {
			_ = svc.Tasks.MarkRejected(updated.TaskID, updated.StepIndex, firstNonEmpty(strings.TrimSpace(comment), "task execution rejected by approver"))
		}
		return
	}
	if updated.SessionID == "" {
		return
	}
	if approved {
		approval := state.CloneApproval(updated)
		go func(item *state.Approval) {
			if item == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), svc.sessionResumeTimeout())
			defer cancel()
			_ = svc.ResumeApprovedSessionApproval(ctx, item)
		}(approval)
		return
	}
	if svc.Sessions != nil {
		if _, err := svc.Sessions.AddAssistantMessage(updated.SessionID, sessionApprovalRejectedAssistantMessage(updated, comment)); err != nil {
			_, _ = svc.Sessions.FailTurn(updated.SessionID)
		}
	} else {
		svc.UpdateSessionPresence(updated.SessionID, "idle", false)
	}
	svc.appendEvent("chat.cancelled", updated.SessionID, map[string]any{
		"approval_id": updated.ID,
		"reason":      firstNonEmpty(strings.TrimSpace(comment), "approval rejected"),
		"source":      "approval",
		"status":      "rejected",
		"tool_name":   strings.TrimSpace(updated.ToolName),
	})
}

func (svc Service) RequireSessionToolApproval(session *state.Session, title string, message string, source string, toolName string, args map[string]any) error {
	if svc.Runner == nil {
		return fmt.Errorf("session runner not initialized")
	}
	return svc.Runner.RequireToolApproval(session, sessionrunner.ApprovalContext{
		Title:   title,
		Message: message,
		Source:  source,
	}, toolName, args)
}

func (svc Service) UpdateSessionApprovalPresence(sessionID string, toolName string) {
	svc.UpdateSessionPresence(sessionID, "waiting_approval", false)
	svc.appendEvent("session.presence", sessionID, map[string]any{
		"presence":  "waiting_approval",
		"tool_name": strings.TrimSpace(toolName),
		"source":    "approval",
	})
}

func (svc Service) UpdateSessionPresence(sessionID string, presence string, typing bool) {
	if svc.Sessions == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	_, _ = svc.Sessions.SetPresence(sessionID, presence, typing)
}

func (svc Service) SessionApprovalResponse(sessionID string) map[string]any {
	approvals := []*state.Approval{}
	if svc.Store != nil {
		approvals = svc.Store.ListSessionApprovals(sessionID)
	}
	response := map[string]any{
		"status":    "waiting_approval",
		"approvals": approvals,
	}
	if svc.Sessions != nil {
		if session, ok := svc.Sessions.Get(sessionID); ok {
			response["session"] = session
		}
	}
	return response
}

func (svc Service) ResumeApprovedSessionApproval(ctx context.Context, approval *state.Approval) error {
	if svc.Runner == nil {
		return nil
	}
	return svc.Runner.ResumeApproved(ctx, approval)
}

func (svc Service) appendEvent(eventType string, sessionID string, payload map[string]any) {
	if svc.AppendEvent == nil {
		return
	}
	svc.AppendEvent(eventType, sessionID, payload)
}

func (svc Service) recordTaskCompletion(result *taskrunner.ExecutionResult, source string) {
	if svc.RecordTaskCompletion == nil {
		return
	}
	svc.RecordTaskCompletion(result, source)
}

func (svc Service) sessionResumeTimeout() time.Duration {
	if svc.SessionResumeTimeout > 0 {
		return svc.SessionResumeTimeout
	}
	return defaultSessionResumeTimeout
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func sessionApprovalRejectedAssistantMessage(approval *state.Approval, comment string) string {
	toolName := "工具调用"
	if approval != nil {
		toolName = firstNonEmpty(approval.ToolName, toolName)
		comment = firstNonEmpty(comment, approval.Comment)
	}
	reason := sentence(firstNonEmpty(comment, "用户拒绝/取消了该操作"))
	return fmt.Sprintf("已处理：\n- 已取消等待确认的工具调用 `%s`。\n\n已验证：\n- 该操作未继续执行。\n\n未确认/阻塞：\n- %s", toolName, reason)
}

func sentence(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, ".") || strings.HasSuffix(value, "。") || strings.HasSuffix(value, "!") || strings.HasSuffix(value, "！") || strings.HasSuffix(value, "?") || strings.HasSuffix(value, "？") {
		return value
	}
	return value + "。"
}
