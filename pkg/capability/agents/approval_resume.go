package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	llm "github.com/1024XEngineer/anyclaw/pkg/capability/models"
)

type ApprovalResumeState struct {
	Messages         []llm.Message     `json:"messages,omitempty"`
	AssistantMessage llm.Message       `json:"assistant_message"`
	Observations     []ToolObservation `json:"observations,omitempty"`
	PendingTool      ToolCall          `json:"pending_tool"`
}

type ApprovalPauseError struct {
	State ApprovalResumeState
	Cause error
}

func (e *ApprovalPauseError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "approval required"
}

func (e *ApprovalPauseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (a *Agent) ResumeAfterApproval(ctx context.Context, resume ApprovalResumeState) (string, error) {
	if a == nil {
		return "", errors.New("agent is not initialized")
	}
	if strings.TrimSpace(resume.PendingTool.Name) == "" {
		return "", errors.New("approval resume missing pending tool")
	}

	a.resetToolActivities()

	messages := compactCodexLoopMessages(resume.Messages)
	assistantMessage := compactCodexLoopMessage(resume.AssistantMessage)
	if strings.TrimSpace(assistantMessage.Role) == "" {
		assistantMessage.Role = "assistant"
	}
	messages = append(messages, assistantMessage)

	observations := cloneToolObservations(resume.Observations)
	for _, obs := range observations {
		tc := ToolCall{ID: obs.ToolCallID, Name: obs.Tool}
		messages = append(messages, toolObservationMessage(tc, obs))
	}

	pendingTool := cloneToolCall(resume.PendingTool)
	observation, activity := a.executeToolObservation(ctx, pendingTool)
	a.recordToolActivity(activity)
	observations = append(observations, observation)
	messages = append(messages, toolObservationMessage(pendingTool, observation))
	messages = append(messages, llm.Message{Role: "user", Content: a.toolContinuationPrompt(observations)})
	toolDefs := a.buildSelectedToolDefinitions(a.selectToolInfos(a.latestUserInput()))

	response, err := NewCodexChain(a).Run(ctx, CodexChainRequest{
		Messages: messages,
		Tools:    toolDefs,
	})
	if err != nil {
		return "", err
	}

	a.appendHistoryMessage(ctx, "assistant", response)

	return response, nil
}

func cloneToolObservations(items []ToolObservation) []ToolObservation {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]ToolObservation, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, compactToolObservation(item, true))
	}
	return cloned
}

func limitToolObservations(items []ToolObservation, limit int) []ToolObservation {
	if limit <= 0 || len(items) <= limit {
		return append([]ToolObservation(nil), items...)
	}
	result := append([]ToolObservation(nil), items[:limit]...)
	result = append(result, ToolObservation{
		Type:    codexObservationType,
		Tool:    "loop",
		OK:      true,
		Summary: fmt.Sprintf("...and %d more observation(s)", len(items)-limit),
	})
	return result
}

func cloneLLMMessage(message llm.Message) llm.Message {
	cloned := message
	if len(message.ToolCalls) > 0 {
		cloned.ToolCalls = make([]llm.ToolCall, len(message.ToolCalls))
		copy(cloned.ToolCalls, message.ToolCalls)
	}
	return cloned
}

func cloneToolCall(tc ToolCall) ToolCall {
	cloned := tc
	if tc.Args != nil {
		cloned.Args = make(map[string]any, len(tc.Args))
		for key, value := range tc.Args {
			cloned.Args[key] = value
		}
	}
	return cloned
}
