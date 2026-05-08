package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/1024XEngineer/anyclaw/pkg/capability/skills"
	"github.com/1024XEngineer/anyclaw/pkg/config"
	"github.com/1024XEngineer/anyclaw/pkg/marketplace"
)

type runtimeMarketArtifactManifest struct {
	ID              string            `json:"id"`
	Kind            string            `json:"kind"`
	Name            string            `json:"name"`
	Summary         string            `json:"summary,omitempty"`
	Description     string            `json:"description,omitempty"`
	DescriptionMD   string            `json:"description_md,omitempty"`
	Version         string            `json:"version"`
	Publisher       string            `json:"publisher,omitempty"`
	Permissions     []string          `json:"permissions,omitempty"`
	Tags            []string          `json:"tags,omitempty"`
	ManifestSummary map[string]string `json:"manifest_summary,omitempty"`
}

func (a *MainRuntime) IntegrateMarketReceiptAndRefresh(ctx context.Context, receipt *marketplace.InstallReceipt) error {
	if a == nil || a.Config == nil {
		return fmt.Errorf("runtime config is unavailable")
	}
	if receipt == nil {
		return fmt.Errorf("install receipt is nil")
	}
	manifest := readRuntimeMarketArtifactManifest(receipt.InstalledPath)
	switch receipt.Kind {
	case marketplace.ArtifactKindSkill:
		if err := a.integrateRuntimeMarketSkill(receipt, manifest); err != nil {
			return err
		}
	case marketplace.ArtifactKindAgent:
		if err := a.integrateRuntimeMarketAgent(receipt, manifest); err != nil {
			return err
		}
	case marketplace.ArtifactKindCLI:
		if err := a.integrateRuntimeMarketCLI(receipt, manifest); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported marketplace artifact kind: %s", receipt.Kind)
	}
	return a.RefreshToolRegistry()
}

func (a *MainRuntime) RefreshAfterMarketBinding(ctx context.Context, binding *marketplace.Binding) error {
	if a == nil {
		return fmt.Errorf("runtime is unavailable")
	}
	if err := a.RefreshToolRegistry(); err != nil {
		return err
	}
	if binding == nil {
		return nil
	}
	scope := refreshScopeForMarketBinding(binding)
	if scope.Kind == "" {
		return nil
	}
	if a.HotReload == nil {
		return nil
	}
	result := a.HotReload.Refresh(ctx, scope)
	if result.Status == "failed" {
		return fmt.Errorf("refresh after marketplace binding: %s", result.Error)
	}
	return nil
}

func (a *MainRuntime) integrateRuntimeMarketSkill(receipt *marketplace.InstallReceipt, manifest runtimeMarketArtifactManifest) error {
	skillName := firstNonEmptyRuntimeMarket(manifest.Name, receipt.Name, receipt.ArtifactID)
	skillDirName := safeRuntimeMarketName(firstNonEmptyRuntimeMarket(receipt.ArtifactID, skillName))
	skillsDir := config.ResolvePath(a.ConfigPath, a.Config.Skills.Dir)
	if skillsDir == "" {
		skillsDir = config.ResolvePath(a.ConfigPath, "skills")
	}
	targetDir := filepath.Join(skillsDir, skillDirName)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	sourceSkillDir := filepath.Join(receipt.InstalledPath, "skill")
	if hasRuntimeMarketFile(sourceSkillDir, "skill.json") || hasRuntimeMarketFile(sourceSkillDir, "SKILL.md") {
		if err := copyRuntimeMarketDirContents(sourceSkillDir, targetDir); err != nil {
			return err
		}
	}
	if !hasRuntimeMarketFile(targetDir, "skill.json") {
		if err := writeRuntimeMarketSkillJSON(targetDir, receipt, manifest, skillName); err != nil {
			return err
		}
	}
	return a.attachRuntimeMarketSkill(skillName, receipt.Version, receipt.Permissions)
}

func (a *MainRuntime) integrateRuntimeMarketAgent(receipt *marketplace.InstallReceipt, manifest runtimeMarketArtifactManifest) error {
	agentName := firstNonEmptyRuntimeMarket(manifest.Name, receipt.Name, receipt.ArtifactID)
	profile := config.AgentProfile{
		Name:            agentName,
		Description:     firstNonEmptyRuntimeMarket(manifest.Summary, manifest.Description, receipt.Description),
		Role:            "marketplace",
		Persona:         firstNonEmptyRuntimeMarket(manifest.DescriptionMD, manifest.Description, manifest.Summary, receipt.Description),
		Domain:          "marketplace",
		Expertise:       append([]string(nil), manifest.Tags...),
		WorkingDir:      a.Config.Agent.WorkingDir,
		PermissionLevel: firstNonEmptyRuntimeMarket(runtimeMarketPermissionLevelFrom(receipt.Permissions), a.Config.Agent.PermissionLevel, "limited"),
		ProviderRef:     a.Config.LLM.DefaultProviderRef,
		Enabled:         config.BoolPtr(true),
	}
	if strings.TrimSpace(profile.Persona) == "" {
		profile.Persona = "Marketplace installed agent: " + receipt.ArtifactID
	}
	profile.SystemPrompt = profile.Persona
	if err := a.Config.UpsertAgentProfile(profile); err != nil {
		return err
	}
	return a.Config.Save(a.ConfigPath)
}

func (a *MainRuntime) integrateRuntimeMarketCLI(receipt *marketplace.InstallReceipt, manifest runtimeMarketArtifactManifest) error {
	root := filepath.Join(a.WorkingDir, "CLI-Anything")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	spec, err := runtimeMarketCLISpec(receipt.InstalledPath, receipt, manifest)
	if err != nil {
		return err
	}
	entryName := safeRuntimeMarketName(firstNonEmptyRuntimeMarket(spec.Name, manifest.ManifestSummary["command"], manifest.Name, receipt.Name, receipt.ArtifactID))
	entryPoint := filepath.Join(receipt.InstalledPath, filepath.FromSlash(spec.EntryPoint))
	if !pathWithinRuntimeMarketBase(receipt.InstalledPath, entryPoint) {
		return fmt.Errorf("marketplace CLI entry point escapes installed path: %s", spec.EntryPoint)
	}
	info, err := os.Stat(entryPoint)
	if err != nil {
		return fmt.Errorf("marketplace CLI entry point missing: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("marketplace CLI entry point is a directory: %s", spec.EntryPoint)
	}
	entry := map[string]any{
		"name":         entryName,
		"display_name": firstNonEmptyRuntimeMarket(manifest.Name, receipt.Name, entryName),
		"version":      firstNonEmptyRuntimeMarket(receipt.Version, manifest.Version),
		"description":  firstNonEmptyRuntimeMarket(manifest.Summary, manifest.Description, receipt.Description),
		"entry_point":  entryPoint,
		"category":     "marketplace",
		"contributor":  firstNonEmptyRuntimeMarket(manifest.Publisher, "AnyClaw Cloud"),
	}
	if err := upsertRuntimeMarketCLIRegistryEntry(filepath.Join(root, "registry.json"), entryName, entry); err != nil {
		return err
	}
	return nil
}

type runtimeMarketCLISpecData struct {
	Name       string `json:"name"`
	EntryPoint string `json:"entry_point"`
	Command    string `json:"command"`
}

func runtimeMarketCLISpec(installedPath string, receipt *marketplace.InstallReceipt, manifest runtimeMarketArtifactManifest) (runtimeMarketCLISpecData, error) {
	var spec runtimeMarketCLISpecData
	if data, err := os.ReadFile(filepath.Join(installedPath, "cli", "command.json")); err == nil {
		_ = json.Unmarshal(data, &spec)
	}
	spec.Name = firstNonEmptyRuntimeMarket(spec.Name, manifest.ManifestSummary["command"], manifest.Name, receipt.Name, receipt.ArtifactID)
	spec.EntryPoint = firstNonEmptyRuntimeMarket(spec.EntryPoint, spec.Command, manifest.ManifestSummary["entry_point"], manifest.ManifestSummary["command"])
	if strings.TrimSpace(spec.EntryPoint) == "" {
		return spec, fmt.Errorf("marketplace CLI artifact requires cli/command.json entry_point")
	}
	return spec, nil
}

func refreshScopeForMarketBinding(binding *marketplace.Binding) RefreshScope {
	targetID := strings.TrimSpace(binding.TargetID)
	scope := RefreshScope{Reason: "marketplace binding " + binding.ID}
	switch binding.TargetType {
	case marketplace.TargetMainAgent:
		scope.Kind = RefreshScopeAgent
		scope.Agent = firstNonEmptyRuntimeMarket(targetID, binding.TargetName)
	case marketplace.TargetPersistentSubagent:
		scope.Kind = RefreshScopeAgent
		scope.Agent = targetID
	case marketplace.TargetWorkspace:
		scope.Kind = RefreshScopeWorkspace
		scope.Workspace = targetID
	case marketplace.TargetRuntimeGlobal:
		scope.Kind = RefreshScopeGlobal
	}
	return scope
}

func (a *MainRuntime) attachRuntimeMarketSkill(name, version string, permissions []string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	if profile, ok := a.Config.ResolveMainAgentProfile(); ok {
		found := false
		for i := range profile.Skills {
			if strings.EqualFold(strings.TrimSpace(profile.Skills[i].Name), strings.TrimSpace(name)) {
				profile.Skills[i].Enabled = true
				if strings.TrimSpace(profile.Skills[i].Version) == "" {
					profile.Skills[i].Version = version
				}
				found = true
				break
			}
		}
		if !found {
			profile.Skills = append(profile.Skills, config.AgentSkillRef{Name: name, Enabled: true, Version: version, Permissions: append([]string(nil), permissions...)})
		}
		if err := a.Config.UpsertAgentProfile(profile); err != nil {
			return err
		}
		return a.Config.Save(a.ConfigPath)
	}
	for i := range a.Config.Agent.Skills {
		if strings.EqualFold(strings.TrimSpace(a.Config.Agent.Skills[i].Name), strings.TrimSpace(name)) {
			a.Config.Agent.Skills[i].Enabled = true
			return a.Config.Save(a.ConfigPath)
		}
	}
	a.Config.Agent.Skills = append(a.Config.Agent.Skills, config.AgentSkillRef{Name: name, Enabled: true, Version: version, Permissions: append([]string(nil), permissions...)})
	return a.Config.Save(a.ConfigPath)
}

func readRuntimeMarketArtifactManifest(root string) runtimeMarketArtifactManifest {
	var manifest runtimeMarketArtifactManifest
	data, err := os.ReadFile(filepath.Join(root, "anyclaw.artifact.json"))
	if err != nil {
		return manifest
	}
	_ = json.Unmarshal(data, &manifest)
	return manifest
}

func writeRuntimeMarketSkillJSON(targetDir string, receipt *marketplace.InstallReceipt, manifest runtimeMarketArtifactManifest, skillName string) error {
	prompt := firstNonEmptyRuntimeMarket(manifest.DescriptionMD, manifest.Description, manifest.Summary, receipt.Description, "Marketplace installed skill: "+receipt.ArtifactID)
	payload := map[string]any{
		"name":        skillName,
		"description": firstNonEmptyRuntimeMarket(manifest.Summary, manifest.Description, receipt.Description),
		"version":     firstNonEmptyRuntimeMarket(receipt.Version, manifest.Version, "1.0.0"),
		"permissions": receipt.Permissions,
		"source":      "marketplace",
		"registry":    receipt.SourceID,
		"prompts":     map[string]string{"system": prompt},
		"metadata": map[string]string{
			"artifact_id": receipt.ArtifactID,
			"receipt_id":  receipt.ID,
			"source":      "marketplace",
		},
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(targetDir, "skill.json"), data, 0o644)
}

func reloadRuntimeSkills(cfg *config.Config) (*skills.SkillsManager, []string, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("runtime config is unavailable")
	}
	manager := skills.NewSkillsManager(cfg.Skills.Dir)
	if err := manager.Load(); err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	configured := configuredAgentSkillNames(cfg)
	if len(configured) == 0 {
		return manager, nil, nil
	}
	filtered, missing := filterConfiguredSkills(manager, configured)
	return filtered, missing, nil
}

func upsertRuntimeMarketCLIRegistryEntry(path, name string, entry map[string]any) error {
	var registry struct {
		Meta map[string]string `json:"meta"`
		CLIs []map[string]any  `json:"clis"`
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &registry)
	}
	if registry.Meta == nil {
		registry.Meta = map[string]string{"repo": "AnyClaw Marketplace", "description": "AnyClaw marketplace CLI entries"}
	}
	replaced := false
	for i := range registry.CLIs {
		if strings.EqualFold(fmt.Sprint(registry.CLIs[i]["name"]), name) {
			registry.CLIs[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		registry.CLIs = append(registry.CLIs, entry)
	}
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func copyRuntimeMarketDirContents(srcDir, destDir string) error {
	srcDir = filepath.Clean(srcDir)
	destDir = filepath.Clean(destDir)
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not supported: %s", path)
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(destDir, rel)
		if !pathWithinRuntimeMarketBase(destDir, target) {
			return fmt.Errorf("copied path escapes destination: %s", rel)
		}
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func hasRuntimeMarketFile(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !info.IsDir()
}

func runtimeMarketPermissionLevelFrom(perms []string) string {
	for _, perm := range perms {
		if strings.Contains(strings.ToLower(strings.TrimSpace(perm)), "exec") {
			return "limited"
		}
	}
	return "read-only"
}

func safeRuntimeMarketName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "market-artifact"
	}
	return out
}

func pathWithinRuntimeMarketBase(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func firstNonEmptyRuntimeMarket(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
