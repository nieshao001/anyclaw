package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	llm "github.com/1024XEngineer/anyclaw/pkg/capability/models"
)

const (
	codexObservationType           = "codex_tool_observation"
	codexLoopMaxToolOutputChars    = 4000
	codexLoopMaxSummaryChars       = 700
	codexLoopMaxArgStringChars     = 500
	codexLoopMaxMessageChars       = 9000
	codexLoopMaxSystemChars        = 24000
	codexLoopMaxProtocolChars      = 6000
	codexLoopMaxFailureDetailChars = 1200
	codexLoopRecentToolOutputs     = 6
)

var commandTargetRegex = regexp.MustCompile(`(?i)["']([^"']+\.(?:html?|url))["']`)

type ToolObservation struct {
	Type            string         `json:"type"`
	ToolCallID      string         `json:"tool_call_id,omitempty"`
	Tool            string         `json:"tool"`
	OK              bool           `json:"ok"`
	Summary         string         `json:"summary"`
	Output          string         `json:"output,omitempty"`
	OutputChars     int            `json:"output_chars,omitempty"`
	OutputTruncated bool           `json:"output_truncated,omitempty"`
	Error           string         `json:"error,omitempty"`
	Args            map[string]any `json:"args,omitempty"`
}

func (a *Agent) executeToolObservation(ctx context.Context, tc ToolCall) (ToolObservation, ToolActivity) {
	result, err := a.executeTool(ctx, tc)
	if err != nil {
		obs := newToolObservation(tc, "", err)
		return obs, ToolActivity{ToolName: tc.Name, Args: tc.Args, Error: err.Error()}
	}
	obs := newToolObservation(tc, result, nil)
	return obs, ToolActivity{ToolName: tc.Name, Args: tc.Args, Result: result}
}

func newToolObservation(tc ToolCall, result string, execErr error) ToolObservation {
	output := result
	errText := ""
	ok := execErr == nil
	if execErr != nil {
		errText = execErr.Error()
		output = errText
	}
	truncatedOutput, outputTruncated := truncateMiddle(output, codexLoopMaxToolOutputChars)
	obs := ToolObservation{
		Type:            codexObservationType,
		ToolCallID:      strings.TrimSpace(tc.ID),
		Tool:            strings.TrimSpace(tc.Name),
		OK:              ok,
		Summary:         summarizeToolObservation(tc.Name, output, execErr),
		Output:          truncatedOutput,
		OutputChars:     len(output),
		OutputTruncated: outputTruncated,
		Args:            sanitizeToolArgs(tc.Name, tc.Args),
	}
	if !ok {
		obs.Error = truncateString(errText, codexLoopMaxSummaryChars)
	}
	return obs
}

func summarizeToolObservation(toolName string, output string, execErr error) string {
	if execErr != nil {
		return truncateString(fmt.Sprintf("%s failed: %s", toolName, execErr.Error()), codexLoopMaxSummaryChars)
	}
	text := strings.TrimSpace(firstNonEmptyLine(output))
	if text == "" {
		text = "completed"
	}
	return truncateString(fmt.Sprintf("%s completed: %s", toolName, text), codexLoopMaxSummaryChars)
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func toolObservationMessage(tc ToolCall, obs ToolObservation) llm.Message {
	content := marshalToolObservation(obs)
	return llm.Message{
		Role:       "tool",
		ToolCallID: strings.TrimSpace(tc.ID),
		Name:       strings.TrimSpace(tc.Name),
		Content:    content,
	}
}

func marshalToolObservation(obs ToolObservation) string {
	if strings.TrimSpace(obs.Type) == "" {
		obs.Type = codexObservationType
	}
	data, err := json.Marshal(obs)
	if err != nil {
		return `{"type":"codex_tool_observation","ok":false,"summary":"failed to encode tool observation"}`
	}
	return string(data)
}

func sanitizeToolCallForHistory(tc ToolCall) llm.ToolCall {
	return llm.ToolCall{
		ID:   ensureToolCallID(tc.ID),
		Type: "function",
		Function: llm.FunctionCall{
			Name:      strings.TrimSpace(tc.Name),
			Arguments: mustJSON(sanitizeToolArgs(tc.Name, tc.Args)),
		},
	}
}

func sanitizeLLMToolCallForHistory(tc llm.ToolCall) llm.ToolCall {
	cloned := tc
	if strings.TrimSpace(cloned.ID) == "" {
		cloned.ID = ensureToolCallID("")
	}
	if strings.TrimSpace(cloned.Type) == "" {
		cloned.Type = "function"
	}
	args := map[string]any{}
	if strings.TrimSpace(cloned.Function.Arguments) != "" {
		_ = json.Unmarshal([]byte(cloned.Function.Arguments), &args)
	}
	cloned.Function.Arguments = mustJSON(sanitizeToolArgs(cloned.Function.Name, args))
	return cloned
}

func sanitizeToolArgs(toolName string, args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = sanitizeToolArgValue(strings.TrimSpace(toolName), strings.TrimSpace(key), value, 0)
	}
	return out
}

func sanitizeToolArgValue(toolName string, key string, value any, depth int) any {
	if depth > 3 {
		return summarizeAny(value)
	}
	switch v := value.(type) {
	case string:
		if shouldOmitLargeArgument(toolName, key, v) {
			return summarizeLargeString(v)
		}
		if len(v) > codexLoopMaxArgStringChars {
			truncated, _ := truncateMiddle(v, codexLoopMaxArgStringChars)
			return map[string]any{
				"value":     truncated,
				"chars":     len(v),
				"sha256":    sha256Hex(v),
				"truncated": true,
			}
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for nestedKey, nestedValue := range v {
			out[nestedKey] = sanitizeToolArgValue(toolName, nestedKey, nestedValue, depth+1)
		}
		return out
	case []any:
		limit := len(v)
		truncated := false
		if limit > 20 {
			limit = 20
			truncated = true
		}
		out := make([]any, 0, limit)
		for i := 0; i < limit; i++ {
			out = append(out, sanitizeToolArgValue(toolName, key, v[i], depth+1))
		}
		if truncated {
			return map[string]any{
				"items":     out,
				"count":     len(v),
				"truncated": true,
			}
		}
		return out
	default:
		return v
	}
}

func shouldOmitLargeArgument(toolName string, key string, value string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	if key == "content" || key == "body" || key == "text" || key == "data" || key == "input" {
		return len(value) > codexLoopMaxArgStringChars || toolName == "write_file" || toolName == "write" || toolName == "edit" || toolName == "apply_patch"
	}
	return false
}

func summarizeLargeString(value string) map[string]any {
	preview, truncated := truncateMiddle(value, codexLoopMaxArgStringChars)
	return map[string]any{
		"omitted":   true,
		"preview":   preview,
		"chars":     len(value),
		"sha256":    sha256Hex(value),
		"truncated": truncated,
	}
}

func summarizeAny(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%T", value)
	}
	text, truncated := truncateMiddle(string(data), codexLoopMaxArgStringChars)
	return map[string]any{
		"value":     text,
		"chars":     len(data),
		"truncated": truncated,
	}
}

func compactCodexLoopMessages(messages []llm.Message) []llm.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		out = append(out, compactCodexLoopMessage(msg))
	}
	compactOlderToolObservations(out)
	return out
}

func compactOlderToolObservations(messages []llm.Message) {
	recentToolOutputs := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.TrimSpace(messages[i].Role) != "tool" {
			continue
		}
		recentToolOutputs++
		if recentToolOutputs <= codexLoopRecentToolOutputs {
			continue
		}
		messages[i].Content = summarizeToolMessageContent(messages[i])
	}
}

func compactCodexLoopMessage(msg llm.Message) llm.Message {
	cloned := cloneLLMMessage(msg)
	role := strings.TrimSpace(cloned.Role)
	if role == "" {
		cloned.Role = "user"
		role = "user"
	}
	for i := range cloned.ToolCalls {
		cloned.ToolCalls[i] = sanitizeLLMToolCallForHistory(cloned.ToolCalls[i])
	}
	switch role {
	case "system":
		cloned.Content = truncateString(cloned.Content, codexLoopMaxSystemChars)
	case "tool":
		cloned.Content = compactToolMessageContent(cloned)
	default:
		if strings.TrimSpace(cloned.Content) == "" && len(cloned.ToolCalls) > 0 {
			cloned.Content = "Tool call requested."
		}
		cloned.Content = truncateString(cloned.Content, codexLoopMaxMessageChars)
	}
	if strings.TrimSpace(cloned.Content) == "" && len(cloned.ToolCalls) == 0 {
		cloned.Content = "(empty message omitted by loop compaction)"
	}
	return cloned
}

func compactToolMessageContent(msg llm.Message) string {
	var obs ToolObservation
	if err := json.Unmarshal([]byte(strings.TrimSpace(msg.Content)), &obs); err == nil && obs.Type == codexObservationType {
		obs = compactToolObservation(obs, true)
		return marshalToolObservation(obs)
	}
	toolName := firstNonEmpty(strings.TrimSpace(msg.Name), "tool")
	tc := ToolCall{ID: strings.TrimSpace(msg.ToolCallID), Name: toolName}
	obs = newToolObservation(tc, msg.Content, nil)
	return marshalToolObservation(obs)
}

func summarizeToolMessageContent(msg llm.Message) string {
	var obs ToolObservation
	if err := json.Unmarshal([]byte(strings.TrimSpace(msg.Content)), &obs); err != nil || obs.Type != codexObservationType {
		toolName := firstNonEmpty(strings.TrimSpace(msg.Name), "tool")
		tc := ToolCall{ID: strings.TrimSpace(msg.ToolCallID), Name: toolName}
		obs = newToolObservation(tc, msg.Content, nil)
	}
	obs = compactToolObservation(obs, false)
	return marshalToolObservation(obs)
}

func compactToolObservation(obs ToolObservation, keepOutput bool) ToolObservation {
	if strings.TrimSpace(obs.Type) == "" {
		obs.Type = codexObservationType
	}
	obs.Summary = truncateString(obs.Summary, codexLoopMaxSummaryChars)
	obs.Args = sanitizeToolArgs(obs.Tool, obs.Args)
	if keepOutput {
		if len(obs.Output) > codexLoopMaxToolOutputChars {
			obs.Output, obs.OutputTruncated = truncateMiddle(obs.Output, codexLoopMaxToolOutputChars)
		}
		return obs
	}
	if strings.TrimSpace(obs.Output) != "" {
		if obs.OutputChars == 0 {
			obs.OutputChars = len(obs.Output)
		}
		obs.Output = ""
		obs.OutputTruncated = true
	}
	return obs
}

func ensureToolCallID(id string) string {
	id = strings.TrimSpace(id)
	if id != "" {
		return id
	}
	return fmt.Sprintf("toolcall_%d", time.Now().UnixNano())
}

func normalizeToolCallIDs(calls []ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, len(calls))
	for i, call := range calls {
		out[i] = cloneToolCall(call)
		if strings.TrimSpace(out[i].ID) == "" {
			out[i].ID = fmt.Sprintf("toolcall_%d_%d", time.Now().UnixNano(), i)
		}
	}
	return out
}

func truncateString(text string, limit int) string {
	truncated, _ := truncateMiddle(text, limit)
	return truncated
}

func truncateMiddle(text string, limit int) (string, bool) {
	if limit <= 0 || len(text) <= limit {
		return text, false
	}
	if limit < 32 {
		return text[:limit], true
	}
	notice := fmt.Sprintf("\n...[truncated %d chars]...\n", len(text)-limit)
	head := (limit - len(notice)) / 2
	if head < 0 {
		head = limit / 2
	}
	tail := limit - len(notice) - head
	if tail < 0 {
		tail = 0
	}
	return text[:head] + notice + text[len(text)-tail:], true
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func failureAssistantMessage(runErr error) string {
	if runErr == nil {
		return "这次任务没有完成。"
	}
	var reqErr *llm.RequestError
	if errors.As(runErr, &reqErr) {
		detail := strings.Join(strings.Fields(reqErr.Error()), " ")
		return "这次任务没有完成：模型请求超时。已记录诊断信息：" + truncateString(detail, codexLoopMaxFailureDetailChars)
	}
	detail := strings.TrimSpace(runErr.Error())
	detail = strings.Join(strings.Fields(detail), " ")
	if detail == "" {
		return "这次任务没有完成。"
	}
	return "这次任务没有完成：" + truncateString(detail, codexLoopMaxFailureDetailChars)
}

func FailureAssistantMessage(runErr error) string {
	return failureAssistantMessage(runErr)
}

func ToolActivitiesCompletionMessage(activities []ToolActivity, runErr error) (string, bool) {
	observations := make([]ToolObservation, 0, len(activities))
	for _, activity := range activities {
		if strings.TrimSpace(activity.Error) != "" {
			observations = append(observations, newToolObservation(ToolCall{Name: activity.ToolName, Args: activity.Args}, "", errors.New(activity.Error)))
			continue
		}
		observations = append(observations, newToolObservation(ToolCall{Name: activity.ToolName, Args: activity.Args}, activity.Result, nil))
	}
	return codexToolSuccessFallback(observations, runErr)
}

func codexToolLimitMessage(partial string, observations []ToolObservation) string {
	trimmed := strings.TrimSpace(partial)
	if len(observations) > 0 {
		return codexToolCompletionMessage(observations, fmt.Errorf("tool call limit reached"))
	}
	lines := []string{
		"已处理：",
		"- 已停止继续调用工具，避免重复执行。",
		"",
		"已验证：",
		"- 本轮已达到工具调用上限。",
		"",
		"未确认/阻塞：",
		"- 工具调用次数达到上限，任务结果未能继续确认。",
	}
	if trimmed != "" {
		lines = append(lines, "", "补充信息：", "- "+truncateString(strings.Join(strings.Fields(trimmed), " "), codexLoopMaxSummaryChars))
	}
	return strings.Join(lines, "\n")
}

func codexToolSuccessFallback(observations []ToolObservation, llmErr error) (string, bool) {
	if len(observations) == 0 {
		return "", false
	}
	successes := successfulToolObservations(observations)
	if len(successes) == 0 {
		return "", false
	}
	if !hasCompletionEligibleToolObservation(successes) {
		return "", false
	}
	return codexToolCompletionMessage(observations, llmErr), true
}

func successfulToolObservations(observations []ToolObservation) []ToolObservation {
	if len(observations) == 0 {
		return nil
	}
	successes := make([]ToolObservation, 0, len(observations))
	for _, observation := range observations {
		if observation.OK {
			successes = append(successes, observation)
		}
	}
	return successes
}

func codexToolCompletionMessage(observations []ToolObservation, llmErr error) string {
	report := buildCodexToolCompletionReport(observations)
	lines := []string{"已处理："}
	for _, line := range report.Handled {
		lines = append(lines, "- "+line)
	}
	lines = append(lines, "", "已验证：")
	for _, line := range report.Verified {
		lines = append(lines, "- "+line)
	}
	lines = append(lines, "", "未确认/阻塞：")
	if len(report.Blocked) == 0 {
		lines = append(lines, "- 无")
	} else {
		for _, line := range report.Blocked {
			lines = append(lines, "- "+line)
		}
	}
	if llmErr != nil && !report.HasStrongVerification {
		lines = append(lines, "- 模型生成补充说明失败，以上为工具结果归纳；最终状态仍建议人工确认。")
	}
	return strings.Join(lines, "\n")
}

type codexCompletionReport struct {
	Handled               []string
	Verified              []string
	Blocked               []string
	HasStrongVerification bool
}

func buildCodexToolCompletionReport(observations []ToolObservation) codexCompletionReport {
	report := codexCompletionReport{}
	for _, observation := range observations {
		if !observation.OK {
			addUniqueLine(&report.Blocked, codexToolBlockingLine(observation))
			continue
		}
		switch strings.ToLower(strings.TrimSpace(observation.Tool)) {
		case "run_command", "exec", "process":
			addRunCommandEvidence(&report, observation)
		case "computer_observe":
			addComputerObserveEvidence(&report, observation)
		case "desktop_open":
			target := firstNonEmptyToolArg(observation.Args, "target")
			kind := firstNonEmptyToolArg(observation.Args, "kind")
			addUniqueLine(&report.Handled, fmt.Sprintf("已打开%s %s。", desktopOpenKindLabel(kind), target))
			addUniqueLine(&report.Verified, "桌面打开工具已成功返回。")
		case "write_file", "write", "edit":
			addUniqueLine(&report.Handled, fmt.Sprintf("已更新文件 %s。", firstNonEmptyToolArg(observation.Args, "path")))
			addUniqueLine(&report.Verified, "文件写入工具已成功返回。")
		default:
			addUniqueLine(&report.Handled, codexToolCompletionLine(observation))
			addUniqueLine(&report.Verified, codexToolVerificationLine(observation))
		}
	}
	if len(report.Handled) == 0 {
		for _, observation := range observations {
			if observation.OK {
				addUniqueLine(&report.Handled, codexToolCompletionLine(observation))
			}
		}
	}
	if len(report.Verified) == 0 {
		for _, observation := range observations {
			if observation.OK {
				addUniqueLine(&report.Verified, codexToolVerificationLine(observation))
			}
		}
	}
	return report
}

func addRunCommandEvidence(report *codexCompletionReport, observation ToolObservation) {
	if report == nil {
		return
	}
	command := firstNonEmptyToolArg(observation.Args, "command", "cmd")
	commandLower := strings.ToLower(command)
	output := strings.TrimSpace(firstNonEmpty(observation.Output, observation.Summary))
	outputLower := strings.ToLower(output)
	target := commandTargetLabel(command)
	if commandOutputIndicatesMissing(outputLower) {
		addUniqueLine(&report.Blocked, fmt.Sprintf("命令返回目标不存在：%s。", shortCommandLabel(command)))
		return
	}
	switch {
	case strings.Contains(commandLower, "invoke-item"):
		addUniqueLine(&report.Handled, fmt.Sprintf("已请求系统打开 %s。", target))
	case strings.Contains(commandLower, "start-process"):
		if commandLooksBrowserRelated(commandLower) {
			addUniqueLine(&report.Handled, fmt.Sprintf("已启动浏览器打开 %s。", target))
		} else {
			addUniqueLine(&report.Handled, fmt.Sprintf("已启动目标进程：%s。", shortCommandLabel(command)))
		}
	case commandLooksStartCommand(commandLower):
		addUniqueLine(&report.Handled, fmt.Sprintf("已请求打开 %s。", target))
	case commandLooksFileMutation(commandLower):
		addUniqueLine(&report.Handled, fmt.Sprintf("已执行本地文件操作：%s。", shortCommandLabel(command)))
	}
	if strings.Contains(commandLower, "test-path") {
		if outputIndicatesExists(outputLower) || !commandOutputIndicatesMissing(outputLower) {
			addUniqueLine(&report.Verified, fmt.Sprintf("已确认目标文件存在：%s。", target))
			report.HasStrongVerification = true
		}
	}
	if strings.Contains(commandLower, "get-process") {
		if outputLooksProcessPresent(outputLower) {
			addUniqueLine(&report.Verified, "已检测到浏览器进程正在运行。")
			report.HasStrongVerification = true
		}
	}
	if strings.Contains(commandLower, "start-process") || strings.Contains(commandLower, "invoke-item") || commandLooksStartCommand(commandLower) {
		addUniqueLine(&report.Verified, "打开命令已成功返回。")
	}
}

func addComputerObserveEvidence(report *codexCompletionReport, observation ToolObservation) {
	if report == nil {
		return
	}
	output := strings.ToLower(firstNonEmpty(observation.Output, observation.Summary))
	if strings.Contains(output, "screenshot_path") || strings.Contains(output, `"environment":"desktop"`) {
		addUniqueLine(&report.Verified, "已获取桌面截图和窗口列表。")
		report.HasStrongVerification = true
	}
	if strings.Contains(output, "msedge") || strings.Contains(output, "chrome") || strings.Contains(output, "firefox") {
		addUniqueLine(&report.Verified, "桌面窗口列表中已出现浏览器窗口。")
		report.HasStrongVerification = true
	}
}

func addUniqueLine(lines *[]string, line string) {
	line = strings.TrimSpace(line)
	if line == "" || lines == nil {
		return
	}
	for _, existing := range *lines {
		if existing == line {
			return
		}
	}
	*lines = append(*lines, line)
}

func codexToolCompletionLine(observation ToolObservation) string {
	toolName := strings.TrimSpace(observation.Tool)
	if !observation.OK {
		if toolName == "" {
			toolName = "tool"
		}
		return fmt.Sprintf("`%s` 未完成：%s", toolName, firstNonEmpty(observation.Error, observation.Summary, "工具调用失败。"))
	}
	switch strings.ToLower(toolName) {
	case "desktop_open":
		return fmt.Sprintf("已完成 `%s`，目标：%s。", toolName, firstNonEmptyToolArg(observation.Args, "target"))
	case "write_file", "write", "edit":
		return fmt.Sprintf("已完成 `%s`，文件：%s。", toolName, firstNonEmptyToolArg(observation.Args, "path"))
	case "run_command", "exec", "process":
		return fmt.Sprintf("已完成 `%s`。", toolName)
	default:
		if toolName == "" {
			toolName = "tool"
		}
		return fmt.Sprintf("已完成 `%s`。", toolName)
	}
}

func codexToolVerificationLine(observation ToolObservation) string {
	toolName := strings.TrimSpace(observation.Tool)
	if toolName == "" {
		toolName = "tool"
	}
	if !observation.OK {
		return fmt.Sprintf("`%s` 返回失败：%s", toolName, firstNonEmpty(observation.Error, observation.Summary, "工具调用失败。"))
	}
	summary := strings.TrimSpace(observation.Summary)
	if summary == "" {
		summary = "工具调用已成功返回。"
	}
	return fmt.Sprintf("`%s` 已成功返回：%s", toolName, summary)
}

func codexToolBlockingLine(observation ToolObservation) string {
	toolName := strings.TrimSpace(observation.Tool)
	if toolName == "" {
		toolName = "tool"
	}
	return fmt.Sprintf("`%s` 失败：%s", toolName, firstNonEmpty(observation.Error, observation.Summary, "工具调用失败。"))
}

func desktopOpenKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "url", "web", "browser":
		return "网页"
	case "file":
		return "文件"
	default:
		return "目标"
	}
}

func commandTargetLabel(command string) string {
	if match := commandTargetRegex.FindStringSubmatch(command); len(match) > 1 {
		return match[1]
	}
	return shortCommandLabel(command)
}

func shortCommandLabel(command string) string {
	command = strings.Join(strings.Fields(strings.TrimSpace(command)), " ")
	if command == "" || command == "未指定" {
		return "未指定命令"
	}
	return truncateString(command, 120)
}

func commandOutputIndicatesMissing(outputLower string) bool {
	return strings.Contains(outputLower, "file does not exist") ||
		strings.Contains(outputLower, "cannot find path") ||
		strings.Contains(outputLower, "not found") ||
		strings.Contains(outputLower, "找不到") ||
		strings.Contains(outputLower, "不存在")
}

func outputIndicatesExists(outputLower string) bool {
	return strings.Contains(outputLower, "file exists") ||
		strings.Contains(outputLower, "true") ||
		strings.Contains(outputLower, "存在")
}

func outputLooksProcessPresent(outputLower string) bool {
	if strings.TrimSpace(outputLower) == "" {
		return false
	}
	if strings.Contains(outputLower, "processname") || strings.Contains(outputLower, "handles") || strings.Contains(outputLower, "id") {
		return true
	}
	return strings.Contains(outputLower, "msedge") ||
		strings.Contains(outputLower, "chrome") ||
		strings.Contains(outputLower, "firefox") ||
		strings.Contains(outputLower, "iexplore") ||
		strings.Contains(outputLower, "opera")
}

func commandLooksBrowserRelated(commandLower string) bool {
	return strings.Contains(commandLower, "msedge") ||
		strings.Contains(commandLower, "chrome") ||
		strings.Contains(commandLower, "firefox") ||
		strings.Contains(commandLower, "iexplore") ||
		strings.Contains(commandLower, "opera") ||
		strings.Contains(commandLower, ".html")
}

func commandLooksStartCommand(commandLower string) bool {
	trimmed := strings.TrimSpace(commandLower)
	return strings.HasPrefix(trimmed, "start ") ||
		strings.HasPrefix(trimmed, "cmd /c start ") ||
		strings.HasPrefix(trimmed, "powershell start ")
}

func commandLooksFileMutation(commandLower string) bool {
	for _, token := range []string{"new-item", "set-content", "add-content", "copy-item", "move-item", "remove-item", "out-file"} {
		if strings.Contains(commandLower, token) {
			return true
		}
	}
	return false
}

func hasCompletionEligibleToolObservation(observations []ToolObservation) bool {
	for _, observation := range observations {
		if observation.OK && isCompletionEligibleToolObservation(observation) {
			return true
		}
	}
	return false
}

func isCompletionEligibleToolObservation(observation ToolObservation) bool {
	name := strings.ToLower(strings.TrimSpace(observation.Tool))
	switch name {
	case "run_command", "exec", "process":
		command := strings.ToLower(firstNonEmptyToolArg(observation.Args, "command", "cmd"))
		output := strings.ToLower(firstNonEmpty(observation.Output, observation.Summary))
		if commandOutputIndicatesMissing(output) {
			return false
		}
		return strings.Contains(command, "invoke-item") ||
			strings.Contains(command, "start-process") ||
			commandLooksStartCommand(command) ||
			commandLooksFileMutation(command)
	case "computer_observe":
		return false
	default:
		return isCompletionEligibleTool(observation.Tool)
	}
}

func isCompletionEligibleTool(toolName string) bool {
	name := strings.ToLower(strings.TrimSpace(toolName))
	switch name {
	case "desktop_open", "desktop_type", "desktop_type_human", "desktop_hotkey", "desktop_clipboard_set",
		"desktop_paste", "desktop_click", "desktop_double_click", "desktop_scroll", "desktop_drag",
		"desktop_focus_window", "desktop_invoke_ui", "desktop_set_value_ui", "desktop_set_target_value",
		"desktop_click_image", "desktop_click_text", "computer_action", "browser_navigate", "browser_click",
		"browser_type", "browser_press", "browser_select", "browser_upload", "write_file", "write", "edit",
		"apply_patch", "canvas_push", "canvas_reset", "memory_set", "memory_add", "memory_save", "memory_update",
		"clihub_exec", "intent_route", "skill_runner", "delegate_task":
		return true
	default:
		if strings.HasPrefix(name, "write_") {
			return true
		}
		return false
	}
}

func firstNonEmptyToolArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if args == nil {
			return "未指定"
		}
		if value, ok := args[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return "未指定"
}
