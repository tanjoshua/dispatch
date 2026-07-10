package server

import (
	"encoding/json"
	"net/http"
	"time"

	"dispatch/agentkit"
	"dispatch/app/domain"
)

type acknowledgeRequest struct {
	AcknowledgedBy string `json:"acknowledged_by,omitempty"`
	Note           string `json:"note,omitempty"`
}

// handleAcknowledge records a dispatcher engaging with a flagged conversation.
// Unlike a decision on an action, this has no backing agent Action — the human
// initiated it — so it appends its own escalation_acknowledged event to the
// run's log and flips the attention projection to acknowledged. It does not
// touch the agent loop; the run stays as it was. See design/001-escalation.md.
func (s *Server) handleAcknowledge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	convID := r.PathValue("id")

	var req acknowledgeRequest
	// Body is optional; ignore a decode error on an empty/malformed body and
	// fall back to defaults.
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.AcknowledgedBy == "" {
		req.AcknowledgedBy = "dispatcher"
	}

	conv, err := s.Domain.GetConversation(ctx, s.DefaultOrgID, convID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conv.AttentionState != domain.AttentionFlagged {
		writeError(w, http.StatusConflict,
			"conversation is not flagged (attention: "+string(conv.AttentionState)+")")
		return
	}

	// The acknowledge event lives on the thread's latest run log. escalated_at
	// scopes the dedupe key to this escalation episode, so acknowledging a later
	// re-flag records its own event rather than being swallowed as a duplicate.
	dedupe := "escalation_acknowledged:" + conv.ID
	if conv.EscalatedAt != nil {
		dedupe += ":" + conv.EscalatedAt.UTC().Format(time.RFC3339Nano)
	}
	runID, err := s.Domain.LatestRunIDForConversation(ctx, conv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	payload, _ := json.Marshal(map[string]string{
		"conversation_id": conv.ID,
		"acknowledged_by": req.AcknowledgedBy,
		"note":            req.Note,
	})
	if err := s.Agentkit.AppendEvent(ctx, agentkit.Event{
		ID:        agentkit.NewID(),
		OrgID:     conv.OrgID,
		RunID:     runID,
		Type:      domain.EventEscalationAcknowledged,
		Payload:   payload,
		DedupeKey: dedupe,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Domain.AcknowledgeEscalation(ctx, conv.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":          "acknowledged",
		"conversation_id": conv.ID,
	})
}
