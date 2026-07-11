package packs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

// ToolEffect describes the product effect of a tool. Approval policy is
// derived from this code-owned classification, never organization config.
type ToolEffect string

const (
	EffectReadOnly              ToolEffect = "read_only"
	EffectControl               ToolEffect = "control"
	EffectCustomerCommunication ToolEffect = "customer_communication"
	EffectBusinessMutation      ToolEffect = "business_mutation"
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
	Effect   ToolEffect    `json:"effect"`
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
	ID             string     `json:"id"`
	Label          string     `json:"label"`
	Description    string     `json:"description"`
	AgentName      string     `json:"agent_name"`
	Provider       string     `json:"-"`
	Tools          []ToolInfo `json:"tools"`
	Lanes          []Lane     `json:"lanes"`
	Stages         []Stage    `json:"stages"`
	DefaultModel   string     `json:"default_model"`
	PromptTemplate string     `json:"-"`
	Catalog        Catalog    `json:"catalog"`
}

type Registry map[string]Pack

func Default() Registry {
	review := []PolicyValue{RequireReview}
	automatic := []PolicyValue{Auto}
	response := ToolInfo{Name: "propose_response", Label: "Customer response", Risk: "Sends a response to the customer.", Effect: EffectCustomerCommunication, Default: RequireReview, Settings: review}
	createCase := ToolInfo{Name: "create_case", Label: "Create case", Risk: "Creates a new service record.", Effect: EffectBusinessMutation, Default: RequireReview, Settings: review}
	selectCase := ToolInfo{Name: "select_case", Label: "Select case", Risk: "Associates this run with an existing customer case.", Effect: EffectBusinessMutation, Default: RequireReview, Settings: review}
	updateCase := ToolInfo{Name: "update_case", Label: "Update case", Risk: "Writes customer-supplied details to the service record.", Effect: EffectBusinessMutation, Default: RequireReview, Settings: review}
	listCases := ToolInfo{Name: "list_candidate_cases", Label: "Find cases", Risk: "Read-only case lookup; always automatic.", Effect: EffectReadOnly, Default: Auto, Settings: automatic}
	routeIntent := ToolInfo{Name: "route_intent", Label: "Route intent", Risk: "Records the internal conversation lane; always automatic.", Effect: EffectControl, Default: Auto, Settings: automatic}
	escalate := ToolInfo{Name: "escalate", Label: "Escalate", Risk: "Raises human attention; always automatic.", Effect: EffectControl, Default: Auto, Settings: automatic}
	standDown := ToolInfo{Name: "stand_down", Label: "Stand down", Risk: "Stops when a dispatcher takes over; always automatic.", Effect: EffectControl, Default: Auto, Settings: automatic}
	wait := ToolInfo{Name: "wait_for_external", Label: "Wait", Risk: "Pauses for outside information; always automatic.", Effect: EffectControl, Default: Auto, Settings: automatic}
	return Registry{"field-service": {
		ID: "field-service", Label: "Field Service", AgentName: "intake", Provider: "anthropic",
		Description:  "Triage inquiries and collect complete, structured service requests.",
		DefaultModel: "claude-sonnet-5", PromptTemplate: fieldservice.Prompt,
		Tools: []ToolInfo{response, createCase, selectCase, updateCase, listCases, routeIntent, escalate, standDown, wait},
		Stages: []Stage{
			{ID: "triage", Label: "Triage", Description: "Understand intent and route the conversation.", Model: "claude-sonnet-5", Status: "live"},
			{ID: "inquiry", Label: "Inquiry", Description: "Answer follow-up questions from organization knowledge.", Model: "claude-sonnet-5", Status: "live"},
			{ID: "service_job", Label: "Service job", Description: "Collect and maintain a structured service request.", Model: "claude-opus-4-8", Status: "live"},
		},
		Lanes: []Lane{
			{ID: "inquiry", Label: "Inquiry", Description: "Answers general questions from organization knowledge without opening a case.", Status: "live", Blocks: []Block{{ID: "answer", Label: "Answer from knowledge", Status: "live"}}, Tools: []ToolInfo{response}},
			{ID: "service_job", Label: "Service job", Description: "Collects and maintains the service request before handoff.", Status: "live", Blocks: []Block{{ID: "intake", Label: "Intake", Status: "live"}, {ID: "scheduling", Label: "Scheduling", Status: "coming_soon"}, {ID: "follow_up", Label: "Follow-up", Status: "coming_soon"}}, Tools: []ToolInfo{
				response,
				createCase,
				selectCase,
				updateCase,
				listCases,
				escalate,
				standDown,
				wait,
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
	Schema int         `json:"schema"`
	Voice  VoiceConfig `json:"voice"`
}

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

func defaults() Config {
	return Config{Schema: 2, Voice: VoiceConfig{AgentName: "Dispatch", Tone: "clear and helpful"}}
}

// EffectiveConfig reads voice from both the legacy schema 1 envelope and the
// schema 2 Agent Behavior document. Pack, model, and policy fields from schema
// 1 are deliberately ignored because those controls are code-owned.
func EffectiveConfig(p Pack, raw json.RawMessage) Effective {
	c := defaults()
	var supplied struct {
		Schema int         `json:"schema"`
		Voice  VoiceConfig `json:"voice"`
	}
	if json.Unmarshal(raw, &supplied) == nil && (supplied.Schema == 1 || supplied.Schema == 2) {
		if strings.TrimSpace(supplied.Voice.AgentName) != "" {
			c.Voice.AgentName = supplied.Voice.AgentName
		}
		if strings.TrimSpace(supplied.Voice.Tone) != "" {
			c.Voice.Tone = supplied.Voice.Tone
		}
		c.Voice.CustomInstructions = supplied.Voice.CustomInstructions
	}
	e := Effective{Config: c, Policy: map[string]map[string]EffectiveTool{}, Models: map[string]string{}}
	for _, stage := range p.Stages {
		e.Models[stage.ID] = ModelForStage(p, stage.ID)
	}
	e.Model = ModelForStage(p, "triage")
	for _, lane := range p.Lanes {
		if lane.Status != "live" {
			continue
		}
		e.Policy[lane.ID] = map[string]EffectiveTool{}
		for _, tool := range lane.Tools {
			e.Policy[lane.ID][tool.Name] = EffectiveTool{Value: tool.Default, Source: "pack", Locked: true}
		}
	}
	return e
}

// ValidateConfig validates the schema 2 document accepted by Agent Behavior
// writes. Unsupported fields are rejected so removed policy/model controls
// cannot remain writable through an old client.
func ValidateConfig(p Pack, raw json.RawMessage) error {
	_ = p
	var c Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return &ValidationError{Fields: map[string]string{"config": "must be valid JSON"}}
	}
	if err := ensureEOF(decoder); err != nil {
		return &ValidationError{Fields: map[string]string{"config": "must contain one JSON object"}}
	}
	fields := map[string]string{}
	if c.Schema != 2 {
		fields["schema"] = "must be 2"
	}
	if strings.TrimSpace(c.Voice.AgentName) == "" {
		fields["voice.agent_name"] = "is required"
	}
	if strings.TrimSpace(c.Voice.Tone) == "" {
		fields["voice.tone"] = "is required"
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// ValidateStoredConfig accepts legacy schema 1 while deployments migrate
// existing rows. Only its voice section is meaningful at runtime.
func ValidateStoredConfig(p Pack, raw json.RawMessage) error {
	var envelope struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return &ValidationError{Fields: map[string]string{"config": "must be valid JSON"}}
	}
	if envelope.Schema == 2 {
		return ValidateConfig(p, raw)
	}
	if envelope.Schema != 1 {
		return &ValidationError{Fields: map[string]string{"schema": "must be 1 or 2"}}
	}
	var legacy struct {
		Voice VoiceConfig `json:"voice"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return &ValidationError{Fields: map[string]string{"config": "must be valid JSON"}}
	}
	fields := validateVoice(legacy.Voice)
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// ModelForStage resolves only code-owned pack metadata. Organization config
// cannot select a provider model.
func ModelForStage(p Pack, id string) string {
	for _, stage := range p.Stages {
		if stage.ID == id && stage.Model != "" {
			return stage.Model
		}
	}
	return p.DefaultModel
}

func validateVoice(voice VoiceConfig) map[string]string {
	fields := map[string]string{}
	if strings.TrimSpace(voice.AgentName) == "" {
		fields["voice.agent_name"] = "is required"
	}
	if strings.TrimSpace(voice.Tone) == "" {
		fields["voice.tone"] = "is required"
	}
	return fields
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
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
