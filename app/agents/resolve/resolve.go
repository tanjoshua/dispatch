package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"dispatch/agentkit"
	"dispatch/agentkit/temporalkit"
	"dispatch/app/domain"
	"dispatch/app/packs"
)

type Resolver struct {
	Store *domain.Store
	Packs packs.Registry
	Tools agentkit.ToolSet
}

func New(store *domain.Store, registry packs.Registry, tools agentkit.ToolSet) *Resolver {
	if err := packs.ValidateRegistryTools(registry, tools); err != nil {
		panic("resolve: invalid pack tool wiring: " + err.Error())
	}
	return &Resolver{Store: store, Packs: registry, Tools: tools}
}

func (r *Resolver) Resolve(ctx context.Context, orgID, runID, name string) (temporalkit.AgentDefinition, error) {
	var raw json.RawMessage
	var selected packs.Pack
	stage := "triage"
	behaviorVersion := "default"
	profileVersion := "missing"
	var runtimeSnapshot *domain.AgentRuntimeSnapshot
	if runID != "" {
		if snapshot, err := r.Store.AgentRuntimeSnapshotForRun(ctx, orgID, runID); err == nil {
			runtimeSnapshot = snapshot
			stage = snapshot.Stage
			raw = snapshot.Playbook.Config
			behaviorVersion = strconv.FormatInt(snapshot.Playbook.Version, 10)
			selected = r.Packs[snapshot.Playbook.PackID]
			if selected.ID == "" {
				return temporalkit.AgentDefinition{}, fmt.Errorf("resolve: playbook %q references unknown pack %q", snapshot.Playbook.ID, snapshot.Playbook.PackID)
			}
			if snapshot.Playbook.Agent != selected.AgentName {
				return temporalkit.AgentDefinition{}, fmt.Errorf("resolve: playbook %q agent %q does not match pack %q agent %q", snapshot.Playbook.ID, snapshot.Playbook.Agent, selected.ID, selected.AgentName)
			}
		} else {
			return temporalkit.AgentDefinition{}, err
		}
	}
	if selected.ID == "" {
		for _, candidate := range r.Packs {
			if candidate.AgentName == name {
				if selected.ID != "" {
					return temporalkit.AgentDefinition{}, errors.New("resolve: multiple packs for agent " + name)
				}
				selected = candidate
			}
		}
	}
	if selected.ID == "" {
		return temporalkit.AgentDefinition{}, errors.New("resolve: no pack for agent " + name)
	}
	if runID != "" {
		if err := packs.ValidateStoredConfig(selected, raw); err != nil {
			return temporalkit.AgentDefinition{}, fmt.Errorf("resolve: invalid Agent Behavior config: %w", err)
		}
	}
	effective := packs.EffectiveConfig(selected, raw)
	profile := packs.Profile{}
	var organization *domain.Organization
	if runtimeSnapshot != nil {
		organization = &runtimeSnapshot.Organization
	} else if org, err := r.Store.GetOrganization(ctx, orgID, orgID); err == nil {
		organization = org
	} else if !errors.Is(err, domain.ErrNotFound) {
		return temporalkit.AgentDefinition{}, err
	}
	if organization != nil {
		profileVersion = strconv.FormatInt(organization.Version, 10)
		var settings struct {
			Profile struct {
				BusinessName string `json:"business_name"`
				Hours        string `json:"hours"`
				ServiceArea  string `json:"service_area"`
				Facts        []struct {
					ID    string `json:"id"`
					Label string `json:"label"`
					Text  string `json:"text"`
				} `json:"facts"`
			} `json:"profile"`
		}
		if json.Unmarshal(organization.Settings, &settings) == nil {
			profile.BusinessName, profile.Hours, profile.ServiceArea = settings.Profile.BusinessName, settings.Profile.Hours, settings.Profile.ServiceArea
			for _, f := range settings.Profile.Facts {
				profile.Facts = append(profile.Facts, packs.Fact{ID: f.ID, Label: f.Label, Text: f.Text})
			}
		}
	}
	system, err := packs.RenderPrompt(selected, effective.Config, profile)
	if err != nil {
		return temporalkit.AgentDefinition{}, err
	}
	model := packs.ModelForStage(selected, stage)
	tools := packs.ToolSetForPack(selected, r.Tools)
	definition := temporalkit.AgentDefinition{
		Name:  selected.AgentName,
		Model: model,
		Tags: map[string]string{
			"stage":                stage,
			"pack_id":              selected.ID,
			"behavior_version":     behaviorVersion,
			"playbook_version":     behaviorVersion,
			"organization_version": profileVersion,
			"profile_version":      profileVersion,
			"prompt_version":       packs.PromptVersion(selected),
			"policy_version":       packs.PolicyVersion(selected),
			"toolset_version":      packs.ToolsetVersion(tools),
			"provider":             selected.Provider,
		},
		System:        system,
		MaxTokens:     4096,
		Tools:         tools,
		Policy:        packs.ToolPolicy{Pack: selected},
		TerminalTools: []string{"escalate", "stand_down"},
		PauseTools:    []string{"wait_for_external"},
	}
	if runtimeSnapshot != nil {
		definition.Snapshot = &temporalkit.AgentSnapshot{
			ContextRevision:    runtimeSnapshot.ContextRevision,
			EventToSeq:         runtimeSnapshot.EventToSeq,
			DependencyVersions: runtimeSnapshot.DependencyVersions,
		}
	}
	return definition, nil
}
