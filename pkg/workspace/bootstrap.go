package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const DefaultBootstrapMaxChars = 20000

type BootstrapOptions struct {
	AgentName         string
	AgentDescription  string
	BootstrapMaxChars int
	UserProfile       string
	WorkspaceFocus    string
	AssistantStyle    string
	Constraints       string
}

type BootstrapFile struct {
	Name      string
	Content   string
	Missing   bool
	Truncated bool
}

var bootstrapFileOrder = []string{
	"AGENTS.md",
}

var legacyBootstrapFiles = []string{
	"SOUL.md",
	"TOOLS.md",
	"IDENTITY.md",
	"USER.md",
	"HEARTBEAT.md",
	"BOOTSTRAP.md",
	"MEMORY.md",
}

func EnsureBootstrap(dir string, opts BootstrapOptions) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	templates := defaultBootstrapTemplates(opts)
	for _, name := range bootstrapFileOrder {
		path := filepath.Join(dir, name)
		if bootstrapFileExists(dir, name) {
			continue
		}
		content, ok := templates[name]
		if !ok {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}

	return removeLegacyBootstrapFiles(dir)
}

func LoadBootstrapFiles(dir string, opts BootstrapOptions) ([]BootstrapFile, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}

	maxChars := opts.BootstrapMaxChars
	if maxChars <= 0 {
		maxChars = DefaultBootstrapMaxChars
	}

	files := make([]BootstrapFile, 0, len(bootstrapFileOrder))
	for _, name := range bootstrapFileOrder {
		path, actualName := resolveBootstrapFilePath(dir, name)

		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				files = append(files, BootstrapFile{
					Name:    name,
					Missing: true,
					Content: fmt.Sprintf("(missing workspace file: %s)", name),
				})
				continue
			}
			return nil, err
		}

		content := strings.TrimSpace(normalizeNewlines(string(data)))
		truncated := false
		if utf8.RuneCountInString(content) > maxChars {
			content = truncateRunes(content, maxChars)
			content = strings.TrimSpace(content) + "\n\n[truncated]"
			truncated = true
		}
		if content == "" {
			content = "(empty)"
		}
		files = append(files, BootstrapFile{
			Name:      actualName,
			Content:   content,
			Truncated: truncated,
		})
	}

	return files, nil
}

func bootstrapFileExists(dir string, name string) bool {
	path, _ := resolveBootstrapFilePath(dir, name)
	return fileExists(path)
}

func resolveBootstrapFilePath(dir string, name string) (string, string) {
	path := filepath.Join(dir, name)
	return path, name
}

func defaultBootstrapTemplates(opts BootstrapOptions) map[string]string {
	name := strings.TrimSpace(opts.AgentName)
	if name == "" {
		name = "AnyClaw"
	}
	return map[string]string{
		"AGENTS.md": strings.TrimSpace(fmt.Sprintf(`# AGENTS

## Primary Agent
- Name: %s
- Goal: Complete the user's task safely, end to end, and verify the real outcome.
%s

## Operating Notes
- If a careful person could safely do the task on this machine, the agent should try to do it instead of only describing it.
- Base each next action on current evidence: file state, command output, browser state, window/app state, UI inspection, OCR, or screenshots.
- Work in loops: inspect -> act -> inspect -> adapt -> verify.
- Prefer higher-level tools and workflows before low-level actions.
- When execution is possible, do the work instead of only explaining it.
- Before finishing, confirm that the requested artifact or state change actually exists.
- Leave concise updates during longer tasks and report what changed, what was verified, and what remains blocked.`, name, bootstrapAgentContext(opts))),
	}
}

func removeLegacyBootstrapFiles(dir string) error {
	for _, name := range legacyBootstrapFiles {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	memoryDir := filepath.Join(dir, "memory")
	if info, err := os.Stat(memoryDir); err == nil && info.IsDir() {
		if err := os.RemoveAll(memoryDir); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func bootstrapAgentContext(opts BootstrapOptions) string {
	lines := []string{}
	if profile := strings.TrimSpace(opts.UserProfile); profile != "" {
		lines = append(lines, "- User profile: "+profile)
	}
	if focus := strings.TrimSpace(opts.WorkspaceFocus); focus != "" {
		lines = append(lines, "- Work focus: "+focus)
	}
	if style := strings.TrimSpace(opts.AssistantStyle); style != "" {
		lines = append(lines, "- Assistant style: "+style)
	}
	if constraints := strings.TrimSpace(opts.Constraints); constraints != "" {
		lines = append(lines, "- Constraints: "+constraints)
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n## Context\n" + strings.Join(lines, "\n")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func normalizeNewlines(input string) string {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	return strings.ReplaceAll(input, "\r", "\n")
}

func truncateRunes(input string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(input)
	if len(runes) <= limit {
		return input
	}
	return string(runes[:limit])
}
