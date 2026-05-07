package approvals

import (
	"strings"
	"testing"
	"time"

	"github.com/1024XEngineer/anyclaw/pkg/state"
)

func TestHandleResolvedRejectedSessionApprovalAppendsCancellationReply(t *testing.T) {
	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sessions := state.NewSessionManager(store, nil)
	session, err := sessions.Create("approval session", "binbin", "org-1", "project-1", "workspace-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := sessions.EnqueueTurn(session.ID); err != nil {
		t.Fatalf("EnqueueTurn: %v", err)
	}
	if _, err := sessions.EnsurePendingUserMessage(session.ID, "run echo anyclaw-approval-smoke"); err != nil {
		t.Fatalf("EnsurePendingUserMessage: %v", err)
	}
	if _, err := sessions.SetPresence(session.ID, "waiting_approval", false); err != nil {
		t.Fatalf("SetPresence: %v", err)
	}

	var events []string
	svc := Service{
		Sessions: sessions,
		AppendEvent: func(eventType string, sessionID string, payload map[string]any) {
			if sessionID == session.ID {
				events = append(events, eventType)
			}
		},
	}
	approval := &state.Approval{
		ID:          "approval-1",
		SessionID:   session.ID,
		ToolName:    "run_command",
		Action:      "tool_call",
		Status:      "rejected",
		RequestedAt: time.Now().UTC(),
	}

	svc.HandleResolved(approval, false, "用户取消")

	updated, ok := sessions.Get(session.ID)
	if !ok || updated == nil {
		t.Fatal("expected session after rejection")
	}
	if updated.QueueDepth != 0 {
		t.Fatalf("expected queue depth 0 after rejection, got %d", updated.QueueDepth)
	}
	if updated.Presence != "idle" || updated.Typing {
		t.Fatalf("expected idle non-typing session, got presence=%q typing=%v", updated.Presence, updated.Typing)
	}
	if len(updated.Messages) != 2 {
		t.Fatalf("expected user plus cancellation reply, got %#v", updated.Messages)
	}
	last := updated.Messages[len(updated.Messages)-1]
	if last.Role != "assistant" {
		t.Fatalf("expected assistant cancellation reply, got %#v", last)
	}
	for _, want := range []string{"已处理", "已验证", "未确认/阻塞", "`run_command`", "该操作未继续执行", "用户取消"} {
		if !strings.Contains(last.Content, want) {
			t.Fatalf("expected cancellation reply to contain %q, got %q", want, last.Content)
		}
	}
	if len(events) != 1 || events[0] != "chat.cancelled" {
		t.Fatalf("expected chat.cancelled event, got %#v", events)
	}
}
