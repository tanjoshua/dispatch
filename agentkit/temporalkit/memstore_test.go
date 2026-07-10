package temporalkit

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"dispatch/agentkit"
	"dispatch/agentkit/llm"
	"dispatch/agentkit/store"
)

// memStore is an in-memory store.Store for workflow tests. It mirrors the
// Postgres store's idempotency semantics — first write wins for decisions,
// results, transcript rows, and event dedupe keys — so the workflow tests
// exercise the same contracts the real store provides.
type memStore struct {
	mu          sync.Mutex
	runs        map[string]*agentkit.Run
	actions     map[string]*agentkit.Action
	actionOrder []string
	byToolCall  map[string]string // runID+"|"+toolCallID → actionID
	events      []agentkit.Event
	eventKeys   map[string]bool // runID+"|"+dedupeKey
	transcripts map[string]map[int]llm.Message
}

var _ store.Store = (*memStore)(nil)

func newMemStore() *memStore {
	return &memStore{
		runs:        map[string]*agentkit.Run{},
		actions:     map[string]*agentkit.Action{},
		byToolCall:  map[string]string{},
		eventKeys:   map[string]bool{},
		transcripts: map[string]map[int]llm.Message{},
	}
}

func (m *memStore) CreateRun(_ context.Context, run agentkit.Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[run.ID]; !ok {
		r := run
		m.runs[run.ID] = &r
	}
	return nil
}

func (m *memStore) GetRun(_ context.Context, orgID, id string) (*agentkit.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok || r.OrgID != orgID {
		return nil, store.ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (m *memStore) FinishRun(_ context.Context, runID string, status agentkit.RunStatus, event agentkit.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[runID]; ok && r.Status == agentkit.RunRunning {
		r.Status = status
	}
	m.appendEventLocked(event)
	return nil
}

func (m *memStore) ProposeAction(_ context.Context, action agentkit.Action, event agentkit.Event) (*agentkit.Action, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := action.RunID + "|" + action.ToolCallID
	if id, ok := m.byToolCall[key]; ok {
		cp := *m.actions[id]
		return &cp, nil
	}
	a := action
	a.ProposedAt = time.Now()
	m.actions[a.ID] = &a
	m.actionOrder = append(m.actionOrder, a.ID)
	m.byToolCall[key] = a.ID
	m.appendEventLocked(event)
	cp := a
	return &cp, nil
}

func (m *memStore) RecordDecision(_ context.Context, actionID string, decision agentkit.Decision, editedInput json.RawMessage, event agentkit.Event) (*agentkit.Action, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.actions[actionID]
	if !ok {
		return nil, store.ErrNotFound
	}
	if a.DecidedAt == nil {
		state := agentkit.ActionApproved
		switch decision.Kind {
		case agentkit.DecisionApproveWithEdits:
			state = agentkit.ActionApprovedWithEdits
		case agentkit.DecisionReject, agentkit.DecisionDismiss, agentkit.DecisionSupersede:
			state = agentkit.ActionRejected
		}
		now := time.Now()
		d := decision
		a.State = state
		a.Decision = &d
		a.EditedInput = editedInput
		a.DecidedAt = &now
		m.appendEventLocked(event)
	}
	cp := *a
	return &cp, nil
}

func (m *memStore) FinishAction(_ context.Context, actionID string, result json.RawMessage, execErr string, event agentkit.Event) (*agentkit.Action, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.actions[actionID]
	if !ok {
		return nil, store.ErrNotFound
	}
	if a.ExecutedAt == nil {
		now := time.Now()
		a.State = agentkit.ActionCompleted
		if execErr != "" {
			a.State = agentkit.ActionFailed
		}
		a.Result = result
		a.Error = execErr
		a.ExecutedAt = &now
		m.appendEventLocked(event)
	}
	cp := *a
	return &cp, nil
}

func (m *memStore) GetAction(_ context.Context, orgID, id string) (*agentkit.Action, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.actions[id]
	if !ok || a.OrgID != orgID {
		return nil, store.ErrNotFound
	}
	cp := *a
	return &cp, nil
}

func (m *memStore) ListActionsByRun(_ context.Context, orgID, runID string) ([]agentkit.Action, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []agentkit.Action
	for _, id := range m.actionOrder {
		a := m.actions[id]
		if a.RunID == runID && a.OrgID == orgID {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (m *memStore) DecisionStats(_ context.Context, _ string) ([]agentkit.ToolDecisionStats, error) {
	return nil, nil
}

func (m *memStore) AppendEvent(_ context.Context, event agentkit.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendEventLocked(event)
	return nil
}

func (m *memStore) appendEventLocked(event agentkit.Event) {
	key := event.RunID + "|" + event.DedupeKey
	if m.eventKeys[key] {
		return
	}
	m.eventKeys[key] = true
	event.CreatedAt = time.Now()
	m.events = append(m.events, event)
}

func (m *memStore) ListEventsByRun(_ context.Context, orgID, runID string) ([]agentkit.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []agentkit.Event
	for _, e := range m.events {
		if e.RunID == runID && e.OrgID == orgID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memStore) AppendRunMessages(_ context.Context, runID, _ string, baseSeq int, msgs []llm.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.transcripts[runID]
	if t == nil {
		t = map[int]llm.Message{}
		m.transcripts[runID] = t
	}
	for i, msg := range msgs {
		seq := baseSeq + i
		if _, ok := t[seq]; !ok { // first write wins
			t[seq] = msg
		}
	}
	return nil
}

func (m *memStore) ListRunMessages(_ context.Context, runID string, upTo int) ([]llm.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []llm.Message
	for seq := 0; seq < upTo; seq++ {
		if msg, ok := m.transcripts[runID][seq]; ok {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (m *memStore) GetRunMessage(_ context.Context, runID string, seq int) (*llm.Message, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msg, ok := m.transcripts[runID][seq]
	if !ok {
		return nil, false, nil
	}
	return &msg, true, nil
}

// --- test helpers over the memStore ---

func (m *memStore) pendingActionID(runID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.actionOrder {
		a := m.actions[id]
		if a.RunID == runID && a.State == agentkit.ActionPendingApproval {
			return a.ID
		}
	}
	return ""
}

func (m *memStore) actionByTool(runID, tool string) *agentkit.Action {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.actionOrder {
		a := m.actions[id]
		if a.RunID == runID && a.Tool == tool {
			cp := *a
			return &cp
		}
	}
	return nil
}

func (m *memStore) eventsOfType(runID string, t agentkit.EventType) []agentkit.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []agentkit.Event
	for _, e := range m.events {
		if e.RunID == runID && e.Type == t {
			out = append(out, e)
		}
	}
	return out
}
