package packs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"dispatch/app/packs/fieldservice"
)

type PolicyValue string

const (
	Auto          PolicyValue = "auto"
	RequireReview PolicyValue = "require_review"
	Forbid        PolicyValue = "forbid"
)

type Stage struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Model       string `json:"model"`
	Status      string `json:"status"`
}

type ToolInfo struct {
	Name     string        `json:"name"`
	Label    string        `json:"label"`
	Risk     string        `json:"risk"`
	Default  PolicyValue   `json:"default"`
	Settings []PolicyValue `json:"settings"`
}

type Block struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
}

type Lane struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	Blocks      []Block    `json:"blocks"`
	Tools       []ToolInfo `json:"tools"`
}

type Catalog struct {
	Label       string `json:"label"`
	Status      string `json:"status"`
	Description string `json:"description"`
}

type Pack struct {
	ID             string      `json:"id"`
	Label          string      `json:"label"`
	Description    string      `json:"description"`
	AgentName      string      `json:"agent_name"`
	Lanes          []Lane      `json:"lanes"`
	Stages         []Stage     `json:"stages"`
	DefaultModel   string      `json:"default_model"`
	PromptTemplate string      `json:"-"`
	Catalog        Catalog     `json:"catalog"`
}

type Registry map[string]Pack

func Default() Registry {
	all := []PolicyValue{Auto, RequireReview, Forbid}
	output := []PolicyValue{Auto, RequireReview}
	fixed := []PolicyValue{Auto}
	return Registry{"field-service": {
		ID: "field-service", Label: "Field Service", AgentName: "intake",
		Description: "Triage inquiries and collect complete, structured service requests.",
		DefaultModel: "claude-sonnet-5", PromptTemplate: fieldservice.Prompt,
		Stages: []Stage{
			{ID: "triage", Label: "Triage", Description: "Understand intent and route the conversation.", Model: "claude-sonnet-5", Status: "live"},
			{ID: "inquiry", Label: "Inquiry", Description: "Answer follow-up questions from organization knowledge.", Model: "claude-sonnet-5", Status: "live"},
			{ID: "service_job", Label: "Service job", Description: "Collect and maintain a structured service request.", Model: "claude-opus-4-8", Status: "live"},
		},
		Lanes: []Lane{
			{ID: "inquiry", Label: "Inquiry", Description: "Answers general questions from organization knowledge without opening a case.", Status: "live", Blocks: []Block{{ID: "answer", Label: "Answer from knowledge", Status: "live"}}, Tools: []ToolInfo{{Name: "propose_response", Label: "Customer response", Risk: "Sends an organization-grounded answer to the customer.", Default: RequireReview, Settings: output}}},
			{ID: "service_job", Label: "Service job", Description: "Collects and maintains the service request before handoff.", Status: "live", Blocks: []Block{{ID: "intake", Label: "Intake", Status: "live"}, {ID: "scheduling", Label: "Scheduling", Status: "coming_soon"}, {ID: "follow_up", Label: "Follow-up", Status: "coming_soon"}}, Tools: []ToolInfo{
				{Name: "propose_response", Label: "Customer response", Risk: "Sends a service-intake response to the customer.", Default: RequireReview, Settings: output},
				{Name: "create_case", Label: "Create case", Risk: "Creates a new service record.", Default: Auto, Settings: all},
				{Name: "select_case", Label: "Select case", Risk: "Associates this run with an existing customer case.", Default: Auto, Settings: all},
				{Name: "update_case", Label: "Update case", Risk: "Writes customer-supplied details to the service record.", Default: Auto, Settings: all},
				{Name: "list_candidate_cases", Label: "Find cases", Risk: "Read-only case lookup; always automatic.", Default: Auto, Settings: fixed},
				{Name: "escalate", Label: "Escalate", Risk: "Raises human attention; always automatic.", Default: Auto, Settings: fixed},
				{Name: "stand_down", Label: "Stand down", Risk: "Stops when a dispatcher takes over; always automatic.", Default: Auto, Settings: fixed},
				{Name: "wait_for_external", Label: "Wait", Risk: "Pauses for outside information; always automatic.", Default: Auto, Settings: fixed},
			}},
			{ID: "quote_request", Label: "Quote request", Description: "A dedicated quote pipeline.", Status: "coming_soon", Blocks: []Block{{ID: "quote", Label: "Quote", Status: "coming_soon"}}},
		},
		Catalog: Catalog{Label: "Service catalog", Status: "coming_soon", Description: "Define services such as water heaters and drains."},
	}}
}

type VoiceConfig struct {
	AgentName          string `json:"agent_name"`
	Tone               string `json:"tone"`
	CustomInstructions string `json:"custom_instructions"`
}

type Config struct {
	Schema        int                               `json:"schema"`
	Pack          string                            `json:"pack"`
	Models        ModelsConfig                      `json:"models,omitempty"`
	Voice         VoiceConfig                       `json:"voice"`
	Policy        map[string]map[string]PolicyValue `json:"policy"`
}

type ModelsConfig struct { PerStage map[string]StageModelConfig `json:"per_stage,omitempty"` }
type StageModelConfig struct { Override string `json:"override,omitempty"` }

type EffectiveTool struct {
	Value  PolicyValue `json:"value"`
	Source string      `json:"source"`
	Locked bool        `json:"locked"`
}

type Effective struct {
	Config Config                              `json:"config"`
	Policy map[string]map[string]EffectiveTool `json:"policy"`
	Model  string                              `json:"model"`
	Models map[string]string                   `json:"models"`
}

type ValidationError struct {
	Fields map[string]string `json:"fields"`
}

func (e *ValidationError) Error() string { return "invalid playbook configuration" }

func defaults(p Pack) Config {
	c := Config{Schema: 1, Pack: p.ID, Voice: VoiceConfig{AgentName: "Dispatch", Tone: "clear and helpful"}, Policy: map[string]map[string]PolicyValue{}}
	for _, lane := range p.Lanes {
		if lane.Status != "live" {
			continue
		}
		c.Policy[lane.ID] = map[string]PolicyValue{}
		for _, tool := range lane.Tools {
			if len(tool.Settings) > 1 {
				c.Policy[lane.ID][tool.Name] = tool.Default
			}
		}
	}
	return c
}

// EffectiveConfig is deliberately forgiving: malformed or partial persisted
// sections fall back independently to pack defaults so bad config cannot brick
// an agent run. ValidateConfig remains strict for writes.
func EffectiveConfig(p Pack, raw json.RawMessage) Effective {
	c := defaults(p)
	configured := map[string]map[string]bool{}
	var supplied Config
	if json.Unmarshal(raw, &supplied) == nil {
		if supplied.Schema == 1 {
			c.Schema = 1
		}
		if supplied.Pack == p.ID {
			c.Pack = supplied.Pack
		}
		c.Models = supplied.Models
		if strings.TrimSpace(supplied.Voice.AgentName) != "" {
			c.Voice.AgentName = supplied.Voice.AgentName
		}
		if strings.TrimSpace(supplied.Voice.Tone) != "" {
			c.Voice.Tone = supplied.Voice.Tone
		}
		c.Voice.CustomInstructions = supplied.Voice.CustomInstructions
		for lane, tools := range supplied.Policy {
			for tool, value := range tools {
				if info, ok := findTool(p, lane, tool); ok && allowed(info, value) && len(info.Settings) > 1 {
					c.Policy[lane][tool] = value
					if configured[lane] == nil {
						configured[lane] = map[string]bool{}
					}
					configured[lane][tool] = true
				}
			}
		}
	}
	e := Effective{Config: c, Policy: map[string]map[string]EffectiveTool{}, Models: map[string]string{}}
	for _, stage := range p.Stages { e.Models[stage.ID] = ResolveModelForStage(p, c, stage.ID) }
	e.Model = ResolveModelForStage(p, c, "triage")
	for _, lane := range p.Lanes {
		if lane.Status != "live" {
			continue
		}
		e.Policy[lane.ID] = map[string]EffectiveTool{}
		for _, tool := range lane.Tools {
			v, source, locked := tool.Default, "pack_default", len(tool.Settings) == 1
			if !locked && configured[lane.ID][tool.Name] {
				v, source = c.Policy[lane.ID][tool.Name], "organization"
			}
			e.Policy[lane.ID][tool.Name] = EffectiveTool{Value: v, Source: source, Locked: locked}
		}
	}
	return e
}

func ValidateConfig(p Pack, raw json.RawMessage) error {
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return &ValidationError{Fields: map[string]string{"config": "must be valid JSON"}}
	}
	fields := map[string]string{}
	if c.Schema != 1 {
		fields["schema"] = "must be 1"
	}
	if c.Pack != p.ID {
		fields["pack"] = "must be " + p.ID
	}
	for stage, model := range c.Models.PerStage {
		if !stageExists(p, stage) { fields["models.per_stage."+stage] = "unknown stage"; continue }
		if strings.TrimSpace(model.Override) == "" { fields["models.per_stage."+stage+".override"] = "must not be empty" }
	}
	if strings.TrimSpace(c.Voice.AgentName) == "" {
		fields["voice.agent_name"] = "is required"
	}
	for lane, tools := range c.Policy {
		for tool, value := range tools {
			info, ok := findTool(p, lane, tool)
			path := "policy." + lane + "." + tool
			if !ok {
				fields[path] = "unknown lane or tool"
				continue
			}
			if len(info.Settings) == 1 {
				fields[path] = "is fixed by the pack"
				continue
			}
			if !allowed(info, value) {
				fields[path] = fmt.Sprintf("%s is below the pack floor", value)
			}
		}
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func ResolveModelForStage(p Pack, c Config, id string) string {
	if override := strings.TrimSpace(c.Models.PerStage[id].Override); override != "" { return override }
	for _, stage := range p.Stages { if stage.ID == id && stage.Model != "" { return stage.Model } }
	return p.DefaultModel
}

type Fact struct{ ID, Label, Text string }
type Profile struct {
	BusinessName, Hours, ServiceArea string
	Facts                            []Fact
}

func RenderPrompt(p Pack, c Config, profile Profile) (string, error) {
	knowledge := []string{}
	if profile.Hours != "" {
		knowledge = append(knowledge, "Hours: "+profile.Hours)
	}
	if profile.ServiceArea != "" {
		knowledge = append(knowledge, "Service area: "+profile.ServiceArea)
	}
	for _, f := range profile.Facts {
		if strings.TrimSpace(f.Text) != "" {
			knowledge = append(knowledge, f.Label+": "+f.Text)
		}
	}
	if len(knowledge) == 0 {
		knowledge = append(knowledge, "No organization knowledge is configured. Do not guess; explain that a dispatcher must confirm.")
	}
	business := profile.BusinessName
	if business == "" {
		business = "the organization"
	}
	t, err := template.New(p.ID).Parse(p.PromptTemplate)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	err = t.Execute(&out, map[string]string{"AgentName": c.Voice.AgentName, "Tone": c.Voice.Tone, "BusinessName": business, "Knowledge": strings.Join(knowledge, "\n"), "CustomInstructions": c.Voice.CustomInstructions})
	return out.String(), err
}

func stageExists(p Pack, id string) bool {
	for _, t := range p.Stages {
		if t.ID == id {
			return true
		}
	}
	return false
}
func findTool(p Pack, lane, name string) (ToolInfo, bool) {
	for _, l := range p.Lanes {
		if l.ID == lane {
			for _, t := range l.Tools {
				if t.Name == name {
					return t, true
				}
			}
		}
	}
	return ToolInfo{}, false
}
func allowed(t ToolInfo, value PolicyValue) bool {
	for _, v := range t.Settings {
		if v == value {
			return true
		}
	}
	return false
}
