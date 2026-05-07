//go:build windows

package tools

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func openDesktopTarget(ctx context.Context, target string, kind string, browser string, workingDir string) error {
	_ = ctx
	target = strings.TrimSpace(target)
	kind = strings.ToLower(strings.TrimSpace(kind))
	browser = strings.ToLower(strings.TrimSpace(browser))
	cwd := strings.TrimSpace(workingDir)

	if kind == "file" {
		target = resolveDesktopOpenFileTarget(target, cwd)
	}
	if kind == "url" && (browser == "edge" || browser == "msedge") {
		return windows.ShellExecute(
			0,
			nil,
			windows.StringToUTF16Ptr("msedge.exe"),
			windows.StringToUTF16Ptr(target),
			utf16PtrOrNil(cwd),
			windows.SW_SHOWNORMAL,
		)
	}
	return windows.ShellExecute(
		0,
		nil,
		windows.StringToUTF16Ptr(target),
		nil,
		utf16PtrOrNil(cwd),
		windows.SW_SHOWNORMAL,
	)
}

func resolveDesktopOpenFileTarget(target string, workingDir string) string {
	resolved := resolvePath(target, workingDir)
	if abs, err := filepath.Abs(resolved); err == nil {
		return abs
	}
	return resolved
}

func utf16PtrOrNil(value string) *uint16 {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return windows.StringToUTF16Ptr(value)
}

func hideCommandWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
