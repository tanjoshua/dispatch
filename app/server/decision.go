package server

import (
	"encoding/json"
	"net/http"

	"dispatch/agentkit"
	"dispatch/agentkit/temporalkit"
)

type decisionRequest struct {
	Kind        agentkit.DecisionKind `json:"kind"` // approve | approve_with_edits | reject
	EditedInput json.RawMessage       `json:"edited_input,omitempty"`
	Reason      string                `json:"reason,omitempty"`
	DecidedBy   string                `json:"decided_by,omitempty"`
}

// handleDecision validates a human decision and signals the run's workflow.
// The workflow records it through the action pipeline; the UI sees the state
// change on its next poll (projection lag is accepted by design).
func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actionID := r.PathValue("id")

	var req decisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	switch req.Kind {
	case agentkit.DecisionApprove:
		if len(req.EditedInput) > 0 {
			writeError(w, http.StatusBadRequest, "edited_input requires kind approve_with_edits")
			return
		}
	case agentkit.DecisionApproveWithEdits:
		if len(req.EditedInput) == 0 {
			writeError(w, http.StatusBadRequest, "approve_with_edits requires edited_input")
			return
		}
		if !json.Valid(req.EditedInput) {
			writeError(w, http.StatusBadRequest, "edited_input must be valid JSON")
			return
		}
	case agentkit.DecisionReject:
		if req.Reason == "" {
			writeError(w, http.StatusBadRequest, "rejections require a reason")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "kind must be approve, approve_with_edits, or reject")
		return
	}

	action, err := s.Agentkit.GetAction(ctx, actionID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "action not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if action.State != agentkit.ActionPendingApproval {
		writeError(w, http.StatusConflict, "action is not pending approval (state: "+string(action.State)+")")
		return
	}

	err = s.Temporal.SignalWorkflow(ctx, agentkit.WorkflowID(action.RunID), "",
		temporalkit.SignalDecision,
		temporalkit.DecisionSignal{
			ActionID:    action.ID,
			Kind:        req.Kind,
			DecidedBy:   req.DecidedBy,
			EditedInput: req.EditedInput,
			Reason:      req.Reason,
		})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "signal workflow: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "decision_sent", "action_id": action.ID})
}
