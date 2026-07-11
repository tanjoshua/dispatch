package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dispatch/app/domain"
)

func TestAgentBehaviorFromPlaybookSupportsMigratingSchemas(t *testing.T) {
	for _, schema := range []string{"1", "2"} {
		t.Run("schema_"+schema, func(t *testing.T) {
			pb := &domain.Playbook{Version: 7, Config: []byte(`{
				"schema":` + schema + `,
				"voice":{"agent_name":"Dispatch","tone":"clear","custom_instructions":"Be concise."},
				"policy":{"propose_response":"auto"},
				"models":{"inquiry":"ignored"}
			}`)}
			got, err := agentBehaviorFromPlaybook(pb)
			if err != nil {
				t.Fatalf("agentBehaviorFromPlaybook: %v", err)
			}
			if got.Version != 7 || got.Behavior.AgentName != "Dispatch" || got.Behavior.Tone != "clear" {
				t.Fatalf("unexpected response: %#v", got)
			}
		})
	}
}

func TestValidateAgentBehaviorRequest(t *testing.T) {
	req := updateAgentBehaviorRequest{
		ExpectedVersion: 0,
		Behavior: domain.AgentBehavior{
			AgentName: strings.Repeat("a", 81),
			Tone:      "",
		},
	}
	fields := validateAgentBehaviorRequest(req)
	for _, name := range []string{"command_id", "expected_version", "agent_name", "tone"} {
		if fields[name] == "" {
			t.Errorf("missing validation error for %s: %#v", name, fields)
		}
	}
}

func TestDecodeStrictJSONRejectsOrganizationUnsafeFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPatch, "/api/org/agent-behavior", strings.NewReader(`{
		"command_id":"cmd_1",
		"expected_version":1,
		"behavior":{"agent_name":"Dispatch","tone":"clear","custom_instructions":""},
		"policy":{"propose_response":"auto"}
	}`))
	recorder := httptest.NewRecorder()
	var target updateAgentBehaviorRequest
	if err := decodeStrictJSON(recorder, req, &target); err == nil {
		t.Fatal("expected unknown policy field to be rejected")
	}
}

func TestHandlerFailsClosedWithoutPrincipalProvider(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/org/agent-behavior", nil)
	(&Server{}).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

func TestHandlerRequiresAdminForBehaviorUpdate(t *testing.T) {
	server := &Server{PrincipalProvider: StaticPrincipalProvider{Principal: Principal{
		OrgID: "org_1", ActorID: "user_1", Roles: []Role{RoleMember},
	}}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/org/agent-behavior", strings.NewReader(`{}`))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}
