//go:build !windows

package tools

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

func openDesktopTarget(ctx context.Context, target string, kind string, browser string, workingDir string) error {
	return fmt.Errorf("desktop_open is currently supported on Windows host mode only: %s", runtime.GOOS)
}

func hideCommandWindow(cmd *exec.Cmd) {}
