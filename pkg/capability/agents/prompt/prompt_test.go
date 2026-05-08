package prompt

import (
	"strings"
	"testing"
)

func TestBuildSystemPromptIncludesCapabilityPlanning(t *testing.T) {
	out, err := BuildSystemPrompt("Ava", "Default description", PromptData{
		Description: "Override description",
		CapabilityPlan: CapabilityPlanInfo{
			TaskClass:                "specialized",
			Route:                    "search_market",
			Need:                     strings.Repeat("find release-note helper ", 12),
			KindHint:                 "skill",
			TopLocalMatch:            CapabilityMatchInfo{Kind: "skill", Name: "release-notes", Score: 0.87, Reason: "matches docs"},
			Reason:                   "local tools are insufficient",
			ShouldExposeMarketSearch: true,
		},
		Tools: []ToolInfo{
			{Name: "market_search_artifacts", Description: "Search market"},
			{Name: "market_install_artifact", Description: "Install market item"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Override description",
		"## Capability Planning",
		"search_market",
		"market_search_artifacts",
		"market_install_artifact only after explicit user confirmation",
		"release-notes",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("prompt missing %q:\n%s", want, out)
		}
	}
}
