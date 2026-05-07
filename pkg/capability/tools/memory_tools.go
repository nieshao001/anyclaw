package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	appmemory "github.com/1024XEngineer/anyclaw/pkg/state/memory"
)

func MemorySearchToolWithCwd(ctx context.Context, input map[string]any, cwd string) (string, error) {
	return MemorySearchToolWithBackend(ctx, input, cwd, nil)
}

func MemorySearchToolWithBackend(ctx context.Context, input map[string]any, cwd string, mem MemoryBackend) (string, error) {
	return MemorySearchToolWithBackendAndDailyDir(ctx, input, cwd, mem, "")
}

func MemorySearchToolWithBackendAndDailyDir(ctx context.Context, input map[string]any, cwd string, mem MemoryBackend, dailyDir string) (string, error) {
	query, ok := input["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is required")
	}

	limit := 5
	if value, ok := input["limit"].(float64); ok && value > 0 {
		limit = int(value)
	}

	if mem != nil {
		entries, err := mem.Search(query, limit)
		if err != nil {
			return "", err
		}
		if len(entries) == 0 {
			return "No memory entries found in backend", nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d memory entries\n\n", len(entries)))
		for _, entry := range entries {
			sb.WriteString(fmt.Sprintf("## [%s] %s %s\n\n%s\n\n", entry.Type, entry.Timestamp.Format("2006-01-02 15:04"), entry.ID, entry.Content))
		}
		return strings.TrimSpace(sb.String()), nil
	}

	day, _ := input["date"].(string)
	matches, err := appmemory.SearchDailyMarkdown(resolveDailyMemoryDir(cwd, mem, dailyDir), query, limit, day)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "No daily memory matches found", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d daily memory match(es)\n\n", len(matches)))
	for _, match := range matches {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", match.Date, match.Path))
		sb.WriteString(match.Snippet)
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String()), nil
}

func MemoryVectorSearchTool(ctx context.Context, input map[string]any, mem MemoryBackend, vec appmemory.VectorBackend) (string, error) {
	if mem == nil || vec == nil {
		return "", fmt.Errorf("vector memory backend not available")
	}

	query, ok := input["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is required")
	}

	limit := 5
	if value, ok := input["limit"].(float64); ok && value > 0 {
		limit = int(value)
	}

	threshold := 0.5
	if value, ok := input["threshold"].(float64); ok && value > 0 {
		threshold = value
	}

	var embedding []float64
	if rawEmbedding, ok := input["embedding"].([]any); ok {
		embedding = make([]float64, len(rawEmbedding))
		for i, v := range rawEmbedding {
			if f, ok := v.(float64); ok {
				embedding[i] = f
			}
		}
	}

	if len(embedding) == 0 {
		entries, err := mem.Search(query, limit)
		if err != nil {
			return "", err
		}
		if len(entries) == 0 {
			return "No memory entries found", nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d memory entries (text search)\n\n", len(entries)))
		for _, entry := range entries {
			sb.WriteString(fmt.Sprintf("## [%s] %s %s\n\n%s\n\n", entry.Type, entry.Timestamp.Format("2006-01-02 15:04"), entry.ID, entry.Content))
		}
		return strings.TrimSpace(sb.String()), nil
	}

	results, err := vec.VectorSearch(embedding, limit, threshold)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "No vector memory matches found", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d vector memory matches\n\n", len(results)))
	for _, entry := range results {
		sb.WriteString(fmt.Sprintf("## [%s] %s %s (score: %.4f)\n\n%s\n\n", entry.Type, entry.Timestamp.Format("2006-01-02 15:04"), entry.ID, entry.Score, entry.Content))
	}
	return strings.TrimSpace(sb.String()), nil
}

func MemoryHybridSearchTool(ctx context.Context, input map[string]any, mem MemoryBackend, vec appmemory.VectorBackend) (string, error) {
	if mem == nil || vec == nil {
		return "", fmt.Errorf("vector memory backend not available")
	}

	query, ok := input["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is required")
	}

	limit := 5
	if value, ok := input["limit"].(float64); ok && value > 0 {
		limit = int(value)
	}

	vectorWeight := 0.5
	if value, ok := input["vector_weight"].(float64); ok && value >= 0 && value <= 1 {
		vectorWeight = value
	}

	var embedding []float64
	if rawEmbedding, ok := input["embedding"].([]any); ok {
		embedding = make([]float64, len(rawEmbedding))
		for i, v := range rawEmbedding {
			if f, ok := v.(float64); ok {
				embedding[i] = f
			}
		}
	}

	if len(embedding) == 0 {
		entries, err := mem.Search(query, limit)
		if err != nil {
			return "", err
		}
		if len(entries) == 0 {
			return "No memory entries found", nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d memory entries (text search)\n\n", len(entries)))
		for _, entry := range entries {
			sb.WriteString(fmt.Sprintf("## [%s] %s %s\n\n%s\n\n", entry.Type, entry.Timestamp.Format("2006-01-02 15:04"), entry.ID, entry.Content))
		}
		return strings.TrimSpace(sb.String()), nil
	}

	results, err := vec.HybridSearch(query, embedding, limit, vectorWeight)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "No hybrid memory matches found", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d hybrid memory matches\n\n", len(results)))
	for _, result := range results {
		sb.WriteString(fmt.Sprintf("## [%s] %s %s (fts: %.4f, vec: %.4f, final: %.4f)\n\n%s\n\n",
			result.Entry.Type, result.Entry.Timestamp.Format("2006-01-02 15:04"), result.Entry.ID,
			result.FTSScore, result.VectorScore, result.FinalScore,
			result.Entry.Content))
	}
	return strings.TrimSpace(sb.String()), nil
}

func MemoryGetToolWithCwd(ctx context.Context, input map[string]any, cwd string) (string, error) {
	return MemoryGetToolWithDailyDir(ctx, input, cwd, nil, "")
}

func MemoryGetToolWithDailyDir(ctx context.Context, input map[string]any, cwd string, mem MemoryBackend, dailyDir string) (string, error) {
	if id, _ := input["id"].(string); strings.TrimSpace(id) != "" {
		if mem == nil {
			return "", fmt.Errorf("memory backend not available")
		}
		entry, err := mem.Get(strings.TrimSpace(id))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("# Memory Entry %s\n\nType: %s\nTimestamp: %s\n\n%s", entry.ID, entry.Type, formatMemoryTimestamp(entry.Timestamp), strings.TrimSpace(entry.Content)), nil
	}

	day, ok := input["date"].(string)
	if !ok || strings.TrimSpace(day) == "" {
		return "", fmt.Errorf("id or date is required")
	}

	file, err := appmemory.GetDailyMarkdown(resolveDailyMemoryDir(cwd, mem, dailyDir), day)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("# Daily Memory %s\nPath: %s\n\n%s", file.Date, file.Path, file.Content), nil
}

func resolveDailyMemoryDir(cwd string, mem MemoryBackend, explicit string) string {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit
	}
	if daily, ok := mem.(appmemory.DailyBackend); ok {
		if dir := strings.TrimSpace(daily.DailyDir()); dir != "" {
			return dir
		}
	}
	return appmemory.CodexDailyMemoryDir(cwd)
}

func formatMemoryTimestamp(t time.Time) string {
	return t.Format("2006-01-02 15:04")
}
