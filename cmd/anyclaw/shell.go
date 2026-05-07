package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/1024XEngineer/anyclaw/pkg/capability/tools"
	"github.com/1024XEngineer/anyclaw/pkg/config"
)

func runShellCommand(args []string) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	configPath := fs.String("config", "anyclaw.json", "path to config file")
	command := fs.String("execute", "", "shell command to execute")
	cwd := fs.String("cwd", "", "working directory override")
	shellName := fs.String("shell", "auto", "shell to use: auto, cmd, powershell, pwsh, sh, or bash")
	sandboxMode := fs.String("sandbox-mode", "", "sandbox mode override: read-only, workspace-write, or danger-full-access")
	timeoutSeconds := fs.Int("timeout", 0, "command timeout in seconds")
	dryRun := fs.Bool("dry-run", false, "print the planned execution without running it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cmdStr := strings.TrimSpace(*command)
	if cmdStr == "" {
		return fmt.Errorf("--execute is required")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if override := strings.TrimSpace(*sandboxMode); override != "" {
		cfg.Permissions.SandboxMode = override
	}
	if *timeoutSeconds > 0 {
		cfg.Security.CommandTimeoutSeconds = *timeoutSeconds
	}

	workingDir := config.ResolvePath(*configPath, cfg.Agent.WorkingDir)
	if workingDir == "" {
		workingDir = config.ResolvePath(*configPath, ".anyclaw")
	}
	executionCwd := strings.TrimSpace(*cwd)
	if executionCwd != "" {
		executionCwd = config.ResolvePath(*configPath, executionCwd)
	}

	if *dryRun {
		fmt.Printf("Dry-run: would execute in %q with shell %q under sandbox %q: %s\n", firstNonEmptyShellDir(executionCwd, workingDir), *shellName, cfg.Permissions.SandboxMode, cmdStr)
		return nil
	}

	sandboxManager := tools.NewSandboxManager(cfg.Sandbox, workingDir)
	permissionOptions := tools.PermissionOptions{
		SandboxMode:    cfg.Permissions.SandboxMode,
		ApprovalPolicy: cfg.Permissions.ApprovalPolicy,
		NetworkAccess:  cfg.Permissions.NetworkAccess,
		DesktopAccess:  cfg.Permissions.DesktopAccess,
	}
	output, err := tools.RunCommandToolWithPolicy(context.Background(), map[string]any{
		"command": cmdStr,
		"cwd":     executionCwd,
		"shell":   *shellName,
	}, tools.BuiltinOptions{
		WorkingDir:            workingDir,
		Permissions:           permissionOptions,
		PermissionEngine:      tools.NewPermissionEngine(workingDir, permissionOptions),
		CommandTimeoutSeconds: cfg.Security.CommandTimeoutSeconds,
		Sandbox:               sandboxManager,
	})
	if output != "" {
		fmt.Print(output)
	}
	return err
}

func firstNonEmptyShellDir(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
