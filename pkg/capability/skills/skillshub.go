package skills

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	SKILLSH_API_BASE    = "https://skills.sh"
	rawGitHubContentURL = "https://raw.githubusercontent.com"
)

type SkillSearchResult struct {
	Name        string   `json:"name"`
	FullName    string   `json:"full_name"`
	Description string   `json:"description"`
	Installs    int64    `json:"installs"`
	Stars       int      `json:"stars"`
	URL         string   `json:"url"`
	Version     string   `json:"version,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Entrypoint  string   `json:"entrypoint,omitempty"`
	Source      string   `json:"source,omitempty"`
}

type SkillDetail struct {
	Name        string   `json:"name"`
	FullName    string   `json:"full_name"`
	Description string   `json:"description"`
	Summary     string   `json:"summary"`
	Installs    int64    `json:"installs"`
	Stars       int      `json:"stars"`
	Repo        string   `json:"repo"`
	Markdown    string   `json:"markdown"`
	Version     string   `json:"version,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Entrypoint  string   `json:"entrypoint,omitempty"`
	Registry    string   `json:"registry,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
}

func SearchCatalog(ctx context.Context, query string, limit int) ([]SkillCatalogEntry, error) {
	results, err := SearchSkills(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	specs := make([]catalogEntrySpec, 0, len(results))
	for _, r := range results {
		specs = append(specs, catalogEntrySpec{
			Name:        r.Name,
			FullName:    r.FullName,
			Description: r.Description,
			Version:     r.Version,
			Registry:    "skills.sh",
			Homepage:    r.URL,
			Source:      r.URL,
			Permissions: r.Permissions,
			Entrypoint:  r.Entrypoint,
			InstallHint: "anyclaw skill install " + r.Name,
		})
	}
	return buildCatalogEntries(specs), nil
}

func SearchSkills(ctx context.Context, query string, limit int) ([]SkillSearchResult, error) {
	limit = normalizeSearchLimit(limit)

	searchURL := fmt.Sprintf("%s/api/search?q=%s&limit=%d", SKILLSH_API_BASE, url.QueryEscape(query), limit)

	client := newRemoteClient(30 * time.Second)
	var results struct {
		Skills []SkillSearchResult `json:"skills"`
	}
	if err := fetchRemoteJSON(ctx, client, searchURL, &results); err != nil {
		return nil, err
	}

	return results.Skills, nil
}

func GetSkillDetail(ctx context.Context, owner, repo, skillName string) (*SkillDetail, error) {
	detailURL := fmt.Sprintf("%s/api/skills/%s/%s/%s", SKILLSH_API_BASE, owner, repo, skillName)

	client := newRemoteClient(30 * time.Second)
	var detail SkillDetail
	if err := fetchRemoteJSON(ctx, client, detailURL, &detail); err != nil {
		return nil, err
	}

	return &detail, nil
}

func GetSkillMarkdown(ctx context.Context, owner, repo, skillName string) (string, error) {
	client := newRemoteClient(30 * time.Second)
	candidates := []string{
		fmt.Sprintf("%s/%s/%s/main/skills/%s/SKILL.md", rawGitHubContentURL, owner, repo, skillName),
		fmt.Sprintf("%s/%s/%s/master/skills/%s/SKILL.md", rawGitHubContentURL, owner, repo, skillName),
		fmt.Sprintf("%s/%s/%s/main/SKILL.md", rawGitHubContentURL, owner, repo),
		fmt.Sprintf("%s/%s/%s/master/SKILL.md", rawGitHubContentURL, owner, repo),
	}

	for _, rawURL := range candidates {
		content, status, err := fetchRemoteText(ctx, client, rawURL)
		if err != nil {
			return "", err
		}
		if status == http.StatusOK {
			return content, nil
		}
		if status != http.StatusNotFound {
			return "", fmt.Errorf("SKILL.md request failed: status %d", status)
		}
	}

	return "", fmt.Errorf("SKILL.md not found")
}

func InstallSkillFromGitHub(ctx context.Context, owner, repo, skillName string, destDir string) error {
	detail, _ := GetSkillDetail(ctx, owner, repo, skillName)
	md, err := GetSkillMarkdown(ctx, owner, repo, skillName)
	if err != nil {
		return fmt.Errorf("failed to fetch SKILL.md: %w", err)
	}

	definition := buildMarkdownSkillFileDefinition(md, skillName, detail)
	if err := installSkillDefinition(destDir, skillName, definition); err != nil {
		return fmt.Errorf("failed to write skill: %w", err)
	}
	return nil
}

func ConvertMarkdownToSkillJSON(md, name string, detail *SkillDetail) (string, error) {
	definition := buildMarkdownSkillFileDefinition(md, name, detail)
	data, err := marshalSkillJSON(definition)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func buildMarkdownSkillFileDefinition(md, name string, detail *SkillDetail) skillFileDefinition {
	lines := strings.Split(md, "\n")

	var description string
	var systemPrompt strings.Builder
	lastPromptAppend := ""
	frontmatterName, frontmatterDescription, bodyLines := splitMarkdownFrontmatter(lines)
	if strings.TrimSpace(frontmatterName) != "" {
		name = strings.TrimSpace(frontmatterName)
	}
	if strings.TrimSpace(frontmatterDescription) != "" {
		description = strings.TrimSpace(frontmatterDescription)
	}

	for _, line := range bodyLines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "##") || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "- [") {
			continue
		}

		if strings.HasPrefix(line, "-") {
			appendSkillPromptText(&systemPrompt, &lastPromptAppend, strings.TrimPrefix(line, "- "), "space")
			continue
		}

		if strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**") {
			if description == "" {
				desc := strings.Trim(line, "*")
				description = desc
			}
			continue
		}

		if line == "" {
			continue
		}

		appendSkillPromptText(&systemPrompt, &lastPromptAppend, line, "line")
	}

	if systemPrompt.Len() == 0 {
		systemPrompt.WriteString("You are a helpful skill assistant.")
	}

	version := "1.0.0"
	permissions := []string{}
	entrypoint := ""
	homepage := ""
	registry := "skills.sh"
	if detail != nil {
		if strings.TrimSpace(detail.Version) != "" {
			version = strings.TrimSpace(detail.Version)
		}
		permissions = append(permissions, detail.Permissions...)
		entrypoint = strings.TrimSpace(detail.Entrypoint)
		homepage = strings.TrimSpace(detail.Homepage)
		if strings.TrimSpace(detail.Registry) != "" {
			registry = strings.TrimSpace(detail.Registry)
		}
		if description == "" {
			description = firstNonEmpty(detail.Description, detail.Summary)
		}
	}
	if description == "" {
		description = "Skill from Skillhub"
	}

	return skillFileDefinition{
		Name:           name,
		Description:    description,
		Version:        version,
		Source:         "skills.sh",
		Registry:       registry,
		Homepage:       homepage,
		Entrypoint:     entrypoint,
		Permissions:    permissions,
		InstallCommand: "anyclaw skill install " + name,
		Prompts: map[string]string{
			"system": strings.TrimSpace(systemPrompt.String()),
		},
	}
}

func appendSkillPromptText(builder *strings.Builder, lastKind *string, text string, kind string) {
	if builder == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if builder.Len() > 0 {
		if kind == "line" && lastKind != nil && *lastKind != "space" {
			builder.WriteString("\n")
		} else {
			builder.WriteString(" ")
		}
	}
	builder.WriteString(text)
	if lastKind != nil {
		*lastKind = kind
	}
}

func splitMarkdownFrontmatter(lines []string) (string, string, []string) {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", lines
	}
	var name string
	var description string
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			return name, description, lines[i+1:]
		}
		switch {
		case strings.HasPrefix(line, "name:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		case strings.HasPrefix(line, "description:"):
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return "", "", lines
}
