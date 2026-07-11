package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"dispatch/agentkit"
	akstore "dispatch/agentkit/store"
	"dispatch/agentkit/temporalkit"
)

type decisionRequest struct {
	DecisionID              string                `json:"decision_id"`
	ExpectedActionVersion   int64                 `json:"expected_action_version"`
	ExpectedContextRevision int64                 `json:"expected_context_revision"`
	Kind                    agentkit.DecisionKind `json:"kind"` // approve | approve_with_edits | reject | dismiss
	EditedInput             json.RawMessage       `json:"edited_input,omitempty"`
	Reason                  string                `json:"reason,omitempty"`
}

// handleDecision validates a human decision and signals the run's workflow.
// The workflow records it through the action pipeline; the UI sees the state
// change on its next poll (projection lag is accepted by design).
func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actionID := r.PathValue("id")
	requestOrgID := orgID(r)

	var req decisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.DecisionID == "" || req.ExpectedActionVersion < 1 || req.ExpectedContextRevision < 0 {
		writeError(w, http.StatusBadRequest, "decision_id and expected versions are required")
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
	case agentkit.DecisionDismiss:
		// A dismiss is an escape, not coaching: no reason required. It carries
		// no edited input either.
		if len(req.EditedInput) > 0 {
			writeError(w, http.StatusBadRequest, "edited_input requires kind approve_with_edits")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "kind must be approve, approve_with_edits, or reject")
		return
	}

	action, err := s.Agentkit.GetAction(ctx, requestOrgID, actionID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "action not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	actor, err := s.actor(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "actor unavailable")
		return
	}
	action, _, err = s.Agentkit.DecideAction(ctx, akstore.DecisionCommand{ID: req.DecisionID, OrgID: requestOrgID, ActionID: actionID,
		ExpectedActionVersion: req.ExpectedActionVersion, ExpectedContextRevision: req.ExpectedContextRevision,
		Decision: agentkit.Decision{Kind: req.Kind, DecidedBy: actor, Reason: req.Reason}, EditedInput: req.EditedInput})
	if err != nil {
		code := "version_conflict"
		if errors.Is(err, akstore.ErrStaleAction) {
			code = "stale_action"
		}
		if errors.Is(err, akstore.ErrAlreadyResolved) {
			code = "already_resolved"
		}
		if errors.Is(err, akstore.ErrStaleAction) || errors.Is(err, akstore.ErrAlreadyResolved) || errors.Is(err, akstore.ErrVersionConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": code, "code": code, "current_action": action})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	err = s.Temporal.SignalWorkflow(ctx, agentkit.WorkflowID(action.RunID), "",
		temporalkit.SignalDecision,
		temporalkit.DecisionSignal{
			ActionID:    action.ID,
			Kind:        req.Kind,
			DecidedBy:   actor,
			EditedInput: req.EditedInput,
			Reason:      req.Reason,
		})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "signal workflow: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "recorded", "action": action})
}
