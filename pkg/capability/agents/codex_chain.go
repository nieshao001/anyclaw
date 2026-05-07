package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	llm "github.com/1024XEngineer/anyclaw/pkg/capability/models"
	"github.com/1024XEngineer/anyclaw/pkg/extensions/plugin"
)

type CodexChain struct {
	agent *Agent
}

type CodexChainRequest struct {
	Messages []llm.Message
	Tools    []llm.ToolDefinition
	OnChunk  func(string)
}

func NewCodexChain(agent *Agent) *CodexChain {
	return &CodexChain{agent: agent}
}

func (c *CodexChain) Run(ctx context.Context, req CodexChainRequest) (string, error) {
	if c == nil || c.agent == nil {
		return "", errors.New("codex chain is not initialized")
	}
	if req.OnChunk == nil {
		return c.run(ctx, req.Messages, req.Tools, nil)
	}

	var emitted bool
	forwardChunk := func(chunk string) {
		if chunk == "" {
			return
		}
		emitted = true
		req.OnChunk(chunk)
	}
	response, err := c.run(ctx, req.Messages, req.Tools, forwardChunk)
	if err != nil {
		return "", err
	}
	if !emitted && response != "" {
		forwardChunk(response)
	}
	return response, nil
}

func (c *CodexChain) run(ctx context.Context, messages []llm.Message, toolDefs []llm.ToolDefinition, onChunk func(string)) (string, error) {
	loopDetector := NewToolLoopDetector(3)
	var completedObservations []ToolObservation
	for toolCalls := 0; ; toolCalls++ {
		messages = compactCodexLoopMessages(messages)
		resp, err := c.chatModelTurn(ctx, messages, toolDefs, onChunk)
		if err != nil {
			if fallback, ok := codexToolSuccessFallback(completedObservations, err); ok {
				return fallback, nil
			}
			return "", fmt.Errorf("LLM error: %w", normalizeLLMError(ctx, err))
		}

		if len(resp.ToolCalls) == 0 {
			if result, handled, err := c.executeProtocolResponse(ctx, resp); handled {
				if err != nil {
					return "", err
				}
				if toolCalls >= c.agent.maxToolCalls {
					return codexToolLimitMessage(result, nil), nil
				}
				messages = append(messages, llm.Message{Role: "assistant", Content: resp.Content})
				messages = append(messages, llm.Message{Role: "user", Content: c.agent.protocolContinuationPrompt(result)})
				continue
			}
		}

		calls := normalizeToolCallIDs(c.agent.extractToolCalls(resp))
		if len(calls) == 0 {
			return resp.Content, nil
		}

		if toolCalls >= c.agent.maxToolCalls {
			if fallback, ok := codexToolSuccessFallback(completedObservations, nil); ok {
				return fallback, nil
			}
			return codexToolLimitMessage(resp.Content, nil), nil
		}

		toolMessages := make([]llm.Message, 0, len(calls)+1)
		observations := make([]ToolObservation, 0, len(calls))
		assistantCallMsg := llm.Message{Role: "assistant", Content: resp.Content, ToolCalls: make([]llm.ToolCall, 0, len(calls))}
		approvalHook := toolApprovalHookFromContext(ctx)
		for _, tc := range calls {
			tc.RequiresApproval = c.agent.toolRequiresApproval(tc.Name)
			if loopDetector.Check("agent-turn", tc.Name, toolCallArgsHash(tc.Args)) {
				return "", fmt.Errorf("tool loop detected: %s repeated with identical arguments", tc.Name)
			}
			currentToolCall := sanitizeToolCallForHistory(tc)
			if approvalHook != nil {
				if err := approvalHook(ctx, tc); err != nil {
					return "", &ApprovalPauseError{
						State: ApprovalResumeState{
							Messages:         compactCodexLoopMessages(messages),
							AssistantMessage: compactCodexLoopMessage(llm.Message{Role: "assistant", Content: resp.Content, ToolCalls: append(append([]llm.ToolCall(nil), assistantCallMsg.ToolCalls...), currentToolCall)}),
							Observations:     cloneToolObservations(observations),
							PendingTool:      cloneToolCall(tc),
						},
						Cause: err,
					}
				}
			}
			assistantCallMsg.ToolCalls = append(assistantCallMsg.ToolCalls, currentToolCall)
			observation, activity := c.agent.executeToolObservation(ctx, tc)
			observations = append(observations, observation)
			if observation.OK {
				completedObservations = append(completedObservations, observation)
			}
			c.agent.recordToolActivity(activity)
			toolMessages = append(toolMessages, toolObservationMessage(tc, observation))
		}

		messages = append(messages, compactCodexLoopMessage(assistantCallMsg))
		messages = append(messages, toolMessages...)
		messages = append(messages, llm.Message{Role: "user", Content: c.agent.toolContinuationPrompt(observations)})
	}
}

func (c *CodexChain) chatModelTurn(ctx context.Context, messages []llm.Message, toolDefs []llm.ToolDefinition, onChunk func(string)) (*llm.Response, error) {
	if onChunk == nil {
		return c.agent.llm.Chat(ctx, messages, toolDefs)
	}

	var emitted bool
	forwardChunk := func(chunk string) {
		if chunk == "" {
			return
		}
		emitted = true
		onChunk(chunk)
	}

	if streamer, ok := c.agent.llm.(LLMStreamResponder); ok {
		resp, err := streamer.StreamChatResponse(ctx, messages, toolDefs, forwardChunk)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			resp = &llm.Response{}
		}
		if !emitted && len(resp.ToolCalls) == 0 && resp.Content != "" {
			forwardChunk(resp.Content)
		}
		return resp, nil
	}

	if len(toolDefs) == 0 {
		var content strings.Builder
		err := c.agent.llm.StreamChat(ctx, messages, toolDefs, func(chunk string) {
			content.WriteString(chunk)
			forwardChunk(chunk)
		})
		if err != nil {
			return nil, err
		}
		return &llm.Response{Content: content.String()}, nil
	}

	return c.agent.llm.Chat(ctx, messages, toolDefs)
}

func (c *CodexChain) executeProtocolResponse(ctx context.Context, resp *llm.Response) (string, bool, error) {
	if resp == nil || c.agent == nil || c.agent.tools == nil {
		return "", false, nil
	}
	for _, payload := range extractProtocolPayloads(resp.Content) {
		result, handled, err := plugin.ExecuteProtocolOutput(ctx, c.agent.tools, plugin.ProtocolExecutionMeta{
			ToolName: "agent_desktop_plan",
			App:      strings.TrimSpace(c.agent.config.Name),
			Action:   "user_request",
			Input: map[string]any{
				"request": c.agent.latestUserInput(),
			},
		}, payload)
		if handled {
			return result, true, err
		}
	}
	return "", false, nil
}

func normalizeLLMError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var reqErr *llm.RequestError
	if errors.As(err, &reqErr) {
		return reqErr
	}
	if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) || strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded") {
		return fmt.Errorf("模型请求超时：当前请求在限定时间内没有返回。AnyClaw 已采用 Codex 风格的最小工具和上下文预算；如果仍反复出现，请换更稳定的模型供应商或提高请求超时时间。detail=%w", err)
	}
	return err
}

func extractProtocolPayloads(content string) [][]byte {
	items := make([][]byte, 0, 2)
	seen := map[string]bool{}
	appendCandidate := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			return
		}
		seen[candidate] = true
		items = append(items, []byte(candidate))
	}
	appendCandidate(content)
	for _, match := range codeBlockRegex.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 {
			appendCandidate(match[1])
		}
	}
	return items
}

func toolCallArgsHash(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	data, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("%v", args)
	}
	return string(data)
}
