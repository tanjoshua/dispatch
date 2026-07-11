package temporalkit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"dispatch/agentkit"
	"dispatch/agentkit/llm"
)

type preparedTurnRecorder struct {
	prepared *PreparedModelTurn
	record   *ModelTurnRecord
}

func (r *preparedTurnRecorder) PrepareModelTurn(_ context.Context, record ModelTurnRecord) (*PreparedModelTurn, error) {
	r.record = &record
	return r.prepared, nil
}

func TestCompleteUsesResolverSnapshotForRequest(t *testing.T) {
	store := newMemStore()
	response := &llm.CompletionResponse{Content: []llm.ContentBlock{{Type: "text", Text: "ok"}}, StopReason: llm.StopEndTurn}
	recorder := &preparedTurnRecorder{prepared: &PreparedModelTurn{
		ID: "turn-1", Response: response, ContextRevision: 4, EventToSeq: 9,
		DependencyVersions: json.RawMessage(`{"playbook_version":2}`),
	}}
	activities := Activities{
		LLM:   shouldNotRunLLM{},
		Store: store,
		Agents: StaticAgents{"test": {
			Name: "test", Model: "test-model", System: "system", MaxTokens: 32,
			Policy: agentkit.StaticPolicy{},
			Snapshot: &AgentSnapshot{ContextRevision: 4, EventToSeq: 9,
				DependencyVersions: json.RawMessage(`{"playbook_version":2}`)},
		}},
		ActionContext: func(context.Context, string) (int64, json.RawMessage, error) {
			return 0, nil, errors.New("mutable ActionContext must not be read")
		},
		ModelTurns: recorder,
	}
	if _, err := activities.Complete(context.Background(), CompleteInput{
		RunID: "run-1", OrgID: "org-1", Agent: "test", Seq: 1,
		TriggeringMessageIDs: []string{"msg-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if recorder.record == nil || recorder.record.ContextRevision != 4 || recorder.record.EventToSeq != 9 {
		t.Fatalf("recorded snapshot = %+v", recorder.record)
	}
	if len(recorder.record.TriggeringMessageIDs) != 1 || recorder.record.TriggeringMessageIDs[0] != "msg-1" {
		t.Fatalf("triggering IDs = %v", recorder.record.TriggeringMessageIDs)
	}
}
func (r *preparedTurnRecorder) CompleteModelTurn(_ context.Context, _ string, response *llm.CompletionResponse) (*llm.CompletionResponse, error) {
	return response, nil
}

func TestExecuteResultMarksPauseTool(t *testing.T) {
	activities := Activities{}
	result := activities.executeResult(AgentDefinition{PauseTools: []string{"wait_for_external"}}, &agentkit.Action{
		Tool: "wait_for_external", State: agentkit.ActionCompleted,
	})
	if !result.Pause || result.Terminal {
		t.Fatalf("pause result = %+v", result)
	}
}

type shouldNotRunLLM struct{}

func (shouldNotRunLLM) Complete(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return nil, errors.New("provider should not be called for a prepared model turn")
}

func TestCompleteReusesPreparedModelTurn(t *testing.T) {
	store := newMemStore()
	response := &llm.CompletionResponse{Content: []llm.ContentBlock{{Type: "text", Text: "stored response"}}, StopReason: llm.StopEndTurn}
	activities := Activities{
		LLM:   shouldNotRunLLM{},
		Store: store,
		Agents: StaticAgents{"test": {
			Name: "test", Model: "test-model", System: "system", MaxTokens: 32,
			Policy: agentkit.StaticPolicy{},
		}},
		ActionContext: func(context.Context, string) (int64, json.RawMessage, error) {
			return 7, json.RawMessage(`{"playbook_version":3}`), nil
		},
		ModelTurns: &preparedTurnRecorder{prepared: &PreparedModelTurn{
			ID:                 "turn-1",
			Response:           response,
			ContextRevision:    3,
			EventToSeq:         8,
			DependencyVersions: json.RawMessage(`{"playbook_version":2}`),
		}},
	}
	result, err := activities.Complete(context.Background(), CompleteInput{RunID: "run-1", OrgID: "org-1", Agent: "test", Seq: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.ModelTurnID != "turn-1" || result.ContextRevision != 3 || result.EventToSeq != 8 || string(result.DependencyVersions) != `{"playbook_version":2}` {
		t.Fatalf("snapshot = %+v", result)
	}
	if result.Response.Text() != "stored response" {
		t.Fatalf("response = %q", result.Response.Text())
	}
}

func TestProposeActionKeepsModelTurnSnapshot(t *testing.T) {
	store := newMemStore()
	activities := Activities{Store: store, Agents: StaticAgents{"test": {
		Name: "test", Policy: agentkit.StaticPolicy{},
	}}}
	action, err := activities.ProposeAction(context.Background(), ProposeActionInput{
		RunID: "run-1", OrgID: "org-1", Agent: "test", ModelTurnID: "turn-1",
		ContextRevision: 4, EventToSeq: 11, DependencyVersions: json.RawMessage(`{"playbook_version":2}`),
		Call: llm.ToolCall{ID: "call-1", Name: "propose_response", Input: json.RawMessage(`{"responds_through_event_seq":1}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.ModelTurnID != "turn-1" || action.ContextRevision != 4 || action.RespondsThroughEventSeq != 11 || string(action.DependencyVersions) != `{"playbook_version":2}` {
		t.Fatalf("action snapshot = %+v", action)
	}
}
