package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"dispatch/agentkit"
	"dispatch/agentkit/temporalkit"
	"dispatch/app/channel"
	"dispatch/app/domain"
)

type dispatcherReplyRequest struct {
	Text   string `json:"text"`
	SentBy string `json:"sent_by,omitempty"`
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
	if req.SentBy == "" {
		req.SentBy = "dispatcher"
	}

	conv, err := s.Domain.GetConversation(ctx, convID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conv.Status == domain.ConversationClosed {
		writeError(w, http.StatusConflict, "conversation is closed")
		return
	}

	// Deliver to the customer through the shared outbound path (author =
	// dispatcher). The message ID is pinned so we can reference the persisted
	// row in the event and the signal.
	msgID := agentkit.NewID()
	if err := s.Sender.Send(ctx, conv.ID, channel.OutboundMessage{
		Body:   req.Text,
		Author: domain.AuthorDispatcher,
		ID:     msgID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "deliver: "+err.Error())
		return
	}

	// Record the human act on the run's append-only log (no backing agent
	// Action), the same grain as an escalation acknowledgement.
	if conv.RunID != "" {
		payload, _ := json.Marshal(map[string]string{
			"conversation_id": conv.ID,
			"message_id":      msgID,
			"sent_by":         req.SentBy,
		})
		if err := s.Agentkit.AppendEvent(ctx, agentkit.Event{
			ID:        agentkit.NewID(),
			OrgID:     conv.OrgID,
			RunID:     conv.RunID,
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
	if conv.RunID != "" && s.runIsLive(ctx, conv.RunID) {
		if err := s.Temporal.SignalWorkflow(ctx, agentkit.WorkflowID(conv.RunID), "",
			temporalkit.SignalDispatcherMessage,
			temporalkit.DispatcherMessageSignal{MessageID: msgID, Text: req.Text}); err != nil {
			writeError(w, http.StatusInternalServerError, "signal workflow: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "sent",
		"message_id": msgID,
	})
}

// runIsLive reports whether the run's workflow is still running, so a signal
// won't error against a completed workflow.
func (s *Server) runIsLive(ctx context.Context, runID string) bool {
	run, err := s.Agentkit.GetRun(ctx, runID)
	return err == nil && run.Status == agentkit.RunRunning
}
