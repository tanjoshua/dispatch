package packs

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"dispatch/agentkit"
)

const (
	// Functions cannot be hashed at runtime. This token is deliberately part of
	// the content address and must change with renderer semantics; defaults are
	// included directly below.
	promptRendererSemantics = "agent-behavior-renderer-v2:voice-defaults+knowledge-grounding"
)

// PromptVersion is a content address for the exact code-owned prompt.
func PromptVersion(pack Pack) string {
	defaultConfig, _ := json.Marshal(defaults())
	return contentVersion(pack.ID + "\x00" + promptRendererSemantics + "\x00" + string(defaultConfig) + "\x00" + pack.PromptTemplate)
}

// PolicyVersion is a content address for the tool-effect policy matrix.
func PolicyVersion(pack Pack) string {
	entries := make([]string, 0, len(pack.Tools))
	for _, tool := range pack.Tools {
		entries = append(entries, tool.Name+"="+string(tool.Effect))
	}
	for _, effect := range []ToolEffect{
		EffectReadOnly, EffectControl, EffectCustomerCommunication, EffectBusinessMutation, ToolEffect("unknown"),
	} {
		entries = append(entries, "effect:"+string(effect)+"="+decisionForEffect(effect).String())
	}
	sort.Strings(entries)
	return contentVersion(strings.Join(entries, "\n"))
}

// ToolsetVersion covers the actual tool names, descriptions, and input
// schemas sent to the model for this pack.
func ToolsetVersion(tools agentkit.ToolSet) string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]string, 0, len(names))
	for _, name := range names {
		tool := tools[name]
		entries = append(entries, name+"\x00"+tool.Description()+"\x00"+string(tool.InputSchema()))
	}
	return contentVersion(strings.Join(entries, "\n"))
}

// ToolInfo returns the canonical declaration for one pack capability.
func (p Pack) ToolInfo(name string) (ToolInfo, bool) {
	for _, tool := range p.Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return ToolInfo{}, false
}

// ToolSetForPack exposes only tools declared by the selected pack. Registry
// wiring is validated separately at worker construction.
func ToolSetForPack(p Pack, available agentkit.ToolSet) agentkit.ToolSet {
	filtered := make(agentkit.ToolSet)
	for name, tool := range available {
		if _, ok := p.ToolInfo(name); ok {
			filtered[name] = tool
		}
	}
	return filtered
}

// ValidateRegistryTools catches missing effect classifications and tools that
// were registered without belonging to any pack. It is a startup wiring check,
// not organization-controlled validation.
func ValidateRegistryTools(registry Registry, available agentkit.ToolSet) error {
	declared := map[string]struct{}{}
	for key, pack := range registry {
		if key != pack.ID {
			return fmt.Errorf("registry key %q does not match pack ID %q", key, pack.ID)
		}
		if err := validatePackTools(pack); err != nil {
			return err
		}
		for _, tool := range pack.Tools {
			declared[tool.Name] = struct{}{}
		}
	}
	for name := range available {
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("registered tool %q is not declared by a pack", name)
		}
	}
	for name := range declared {
		if _, ok := available[name]; !ok {
			return fmt.Errorf("pack-declared tool %q is not registered", name)
		}
	}
	return nil
}

func validatePackTools(pack Pack) error {
	if pack.Provider == "" {
		return fmt.Errorf("pack %q has no provider", pack.ID)
	}
	seen := map[string]ToolInfo{}
	for _, tool := range pack.Tools {
		if tool.Name == "" {
			return fmt.Errorf("pack %q declares a tool without a name", pack.ID)
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return fmt.Errorf("pack %q declares tool %q more than once", pack.ID, tool.Name)
		}
		decision := decisionForEffect(tool.Effect)
		if decision == agentkit.Forbid {
			return fmt.Errorf("pack %q tool %q has unknown effect %q", pack.ID, tool.Name, tool.Effect)
		}
		want := Auto
		if decision == agentkit.RequireApproval {
			want = RequireReview
		}
		if tool.Default != want || len(tool.Settings) != 1 || tool.Settings[0] != want {
			return fmt.Errorf("pack %q tool %q policy metadata does not match effect %q", pack.ID, tool.Name, tool.Effect)
		}
		seen[tool.Name] = tool
	}
	for _, lane := range pack.Lanes {
		for _, tool := range lane.Tools {
			canonical, ok := seen[tool.Name]
			if !ok {
				return fmt.Errorf("pack %q lane %q references undeclared tool %q", pack.ID, lane.ID, tool.Name)
			}
			if tool.Effect != canonical.Effect {
				return fmt.Errorf("pack %q lane %q changes effect for tool %q", pack.ID, lane.ID, tool.Name)
			}
		}
	}
	return nil
}

func contentVersion(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("sha256:%x", sum)
}
