package workspace

import (
	"os"
	"path/filepath"
	"strings"
)

const bootstrapStateFilename = ".anyclaw-bootstrap-state.json"

type BootstrapAdvanceResult struct {
	Active    bool
	Completed bool
	Response  string
}

type BootstrapRitualOptions struct {
	AgentName        string
	AgentDescription string
}

func AdvanceBootstrapRitual(dir string, _ string, _ BootstrapRitualOptions) (*BootstrapAdvanceResult, error) {
	_ = clearBootstrapState(strings.TrimSpace(dir))
	return &BootstrapAdvanceResult{}, nil
}

func BootstrapPending(_ string) bool {
	return false
}

func clearBootstrapState(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if err := os.Remove(bootstrapStatePath(dir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func bootstrapStatePath(dir string) string {
	return filepath.Join(strings.TrimSpace(dir), bootstrapStateFilename)
}
