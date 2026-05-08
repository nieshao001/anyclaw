package marketregistry

import (
	"strings"
	"testing"
)

func TestLocalStorageRejectsUnsafePackageSegments(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	_, err = storage.EnsurePackage(Artifact{
		ID:      "../escape",
		Kind:    ArtifactKindSkill,
		Name:    "Escape",
		Summary: "unsafe",
	}, ArtifactVersion{Version: "1.0.0"})
	if err == nil || !strings.Contains(err.Error(), "artifact id") {
		t.Fatalf("expected unsafe artifact id error, got %v", err)
	}

	_, err = storage.EnsurePackage(Artifact{
		ID:      "cloud.skill.safe",
		Kind:    ArtifactKindSkill,
		Name:    "Safe",
		Summary: "safe",
	}, ArtifactVersion{Version: "../escape"})
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected unsafe version error, got %v", err)
	}
}
