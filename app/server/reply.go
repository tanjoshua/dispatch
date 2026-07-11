package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"dispatch/agentkit"
	"dispatch/agentkit/temporalkit"
	"dispatch/app/channel"
	"dispatch/app/domain"
)

type dispatcherReplyRequest struct {
	Text                    string `json:"text"`
	CommandID               string `json:"command_id"`
	ExpectedContextRevision int64  `json:"expected_context_revision"`
}

// handleDispatcherReply sends a dispatcher's message straight to the customer.
// The dispatcher is a first-class participant, able to reply at any time
// (design/003-dispatcher-as-participant.md): the reply goes out the shared
// Sender path (author=dispatcher), is recorded as a dispatcher_message event,
// and — if the run is live — is signaled into the agent's shared context so its
// next turn is fully informed. A pending agent draft, if any, is superseded by
// the workflow when it sees the signal; there is no takeover mode.
func (s *Server) handleDispatcherReply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	convID := r.PathValue("id")
	requestOrgID := orgID(r)

	var req dispatcherReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if req.CommandID == "" {
		writeError(w, http.StatusBadRequest, "command_id is required")
		return
	}
	actor, err := s.actor(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "actor unavailable")
		return
	}

	conv, err := s.Domain.GetConversation(ctx, requestOrgID, convID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Deliver to the customer through the shared outbound path (author =
	// dispatcher). The message ID is pinned so we can reference the persisted
	// row in the event and the signal.
	msgID := req.CommandID
	_, duplicate, err := s.Domain.PrepareDispatcherReply(ctx, conv.OrgID, conv.ID, msgID, req.ExpectedContextRevision, req.Text, actor)
	if errors.Is(err, domain.ErrVersionConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "stale_action", "code": "stale_action"})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if duplicate {
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent", "message_id": msgID})
		return
	}
	if err := s.Sender.Send(ctx, conv.OrgID, conv.ID, channel.OutboundMessage{
		Body:   req.Text,
		Author: domain.AuthorDispatcher,
		ID:     msgID,
	}); err != nil {
		_ = s.Domain.SetMessageDeliveryState(ctx, msgID, domain.DeliveryUnknown, "", err.Error())
		writeError(w, http.StatusInternalServerError, "deliver: "+err.Error())
		return
	}
	_ = s.Domain.SetMessageDeliveryState(ctx, msgID, domain.DeliverySent, "", "")

	// Record the human act on the thread's latest run log (no backing agent
	// Action), the same grain as an escalation acknowledgement. A persistent
	// thread has many runs; the reply attaches to the most recent one.
	runID, err := s.Domain.LatestRunIDForConversation(ctx, conv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runID != "" {
		payload, _ := json.Marshal(map[string]string{
			"conversation_id": conv.ID,
			"message_id":      msgID,
			"sent_by":         actor,
		})
		if err := s.Agentkit.AppendEvent(ctx, agentkit.Event{
			ID:        agentkit.NewID(),
			OrgID:     conv.OrgID,
			RunID:     runID,
			Type:      domain.EventDispatcherMessage,
			Payload:   payload,
			DedupeKey: "dispatcher_message:" + msgID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	// Inform the agent — but only if there is a live run to receive it. A reply
	// on a finished run is delivered and recorded; there is simply no agent to
	// signal (design/003 §8).
	if runID != "" && s.runIsLive(ctx, requestOrgID, runID) {
		if err := s.Temporal.SignalWorkflow(ctx, agentkit.WorkflowID(runID), "",
			temporalkit.SignalDispatcherMessage,
			temporalkit.DispatcherMessageSignal{MessageID: msgID, Text: req.Text}); err != nil {
			writeError(w, http.StatusInternalServerError, "signal workflow: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "sent",
		"message_id": msgID,
	})
}

// runIsLive reports whether the run's workflow is still running, so a signal
// won't error against a completed workflow.
func (s *Server) runIsLive(ctx context.Context, orgID, runID string) bool {
	run, err := s.Agentkit.GetRun(ctx, orgID, runID)
	return err == nil && run.Status == agentkit.RunRunning
}
