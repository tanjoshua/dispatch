package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"log"

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
	return &Resolver{Store: store, Packs: registry, Tools: tools}
}

func (r *Resolver) Resolve(ctx context.Context, orgID, runID, name string) (temporalkit.AgentDefinition, error) {
	var raw json.RawMessage
	var selected packs.Pack
	stage := "triage"
	if runID != "" {
		if pb, currentStage, err := r.Store.PlaybookAndStageForRun(ctx, orgID, runID); err == nil {
			stage = currentStage
			raw = pb.Config
			var envelope struct{ Pack string `json:"pack"` }
			_ = json.Unmarshal(raw, &envelope)
			selected = r.Packs[envelope.Pack]
		} else if !errors.Is(err, domain.ErrNotFound) {
			return temporalkit.AgentDefinition{}, err
		}
	}
	if selected.ID == "" {
		for _, candidate := range r.Packs { if candidate.AgentName == name { selected = candidate; break } }
	}
	if selected.ID == "" { return temporalkit.AgentDefinition{}, errors.New("resolve: no pack for agent " + name) }
	effective := packs.EffectiveConfig(selected, raw)
	if len(raw) > 0 {
		if err := packs.ValidateConfig(selected, raw); err != nil { log.Printf("resolve: playbook config degraded to safe defaults where invalid: %v", err) }
	}
	profile := packs.Profile{}
	if org, err := r.Store.GetOrganization(ctx, orgID, orgID); err == nil {
		var settings struct { Profile struct {
			BusinessName string `json:"business_name"`; Hours string `json:"hours"`; ServiceArea string `json:"service_area"`; Facts []struct{ID string `json:"id"`; Label string `json:"label"`; Text string `json:"text"`} `json:"facts"`
		} `json:"profile"` }
		if json.Unmarshal(org.Settings, &settings) == nil {
			profile.BusinessName, profile.Hours, profile.ServiceArea = settings.Profile.BusinessName, settings.Profile.Hours, settings.Profile.ServiceArea
			for _, f := range settings.Profile.Facts { profile.Facts = append(profile.Facts, packs.Fact{ID:f.ID,Label:f.Label,Text:f.Text}) }
		}
	} else if !errors.Is(err, domain.ErrNotFound) { return temporalkit.AgentDefinition{}, err }
	system, err := packs.RenderPrompt(selected, effective.Config, profile); if err != nil { return temporalkit.AgentDefinition{}, err }
	return temporalkit.AgentDefinition{Name:selected.AgentName, Model:packs.ResolveModelForStage(selected, effective.Config, stage), Tags:map[string]string{"stage":stage}, System:system, MaxTokens:4096, Tools:r.Tools, Policy:packs.ConfigPolicy{Pack:selected,Effective:effective}, TerminalTools:[]string{"escalate","stand_down"}}, nil
}
