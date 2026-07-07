package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	temporalclient "go.temporal.io/sdk/client"

	"dispatch/agentkit"
	"dispatch/agentkit/temporalkit"
	"dispatch/app/channel/simulated"
	"dispatch/app/domain"
)

type simulateInboundRequest struct {
	Phone string `json:"phone"`
	Name  string `json:"name"`
	Text  string `json:"text"`
}

// handleSimulateInbound is the simulated channel's inbound adapter. It does
// exactly what a real WhatsApp webhook handler would: normalize the message,
// find/create the conversation and run, persist message + event, then
// signal-with-start the run's workflow.
func (s *Server) handleSimulateInbound(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req simulateInboundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Phone = strings.TrimSpace(req.Phone)
	req.Text = strings.TrimSpace(req.Text)
	if req.Phone == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "phone and text are required")
		return
	}

	customer, err := s.Domain.GetOrCreateCustomer(ctx, s.OrgID, req.Phone, strings.TrimSpace(req.Name))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	conv, err := s.Domain.OpenConversationForCustomer(ctx, s.OrgID, customer.ID)
	if isNotFound(err) {
		conv, err = s.Domain.CreateConversation(ctx, s.OrgID, customer.ID, simulated.Name)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	runID, err := s.ensureRun(ctx, conv)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	msg := domain.Message{
		ID:             agentkit.NewID(),
		OrgID:          s.OrgID,
		ConversationID: conv.ID,
		Direction:      domain.Inbound,
		Body:           req.Text,
	}
	if err := s.Domain.AddMessage(ctx, msg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	payload, _ := json.Marshal(map[string]string{"message_id": msg.ID, "conversation_id": conv.ID})
	if err := s.Agentkit.AppendEvent(ctx, agentkit.Event{
		ID:        agentkit.NewID(),
		OrgID:     s.OrgID,
		RunID:     runID,
		Type:      agentkit.EventMessageReceived,
		Payload:   payload,
		DedupeKey: "message_received:" + msg.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_, err = s.Temporal.SignalWithStartWorkflow(ctx,
		agentkit.WorkflowID(runID),
		temporalkit.SignalInboundMessage,
		temporalkit.InboundMessage{MessageID: msg.ID, Text: req.Text},
		temporalclient.StartWorkflowOptions{
			ID:        agentkit.WorkflowID(runID),
			TaskQueue: s.TaskQueue,
		},
		temporalkit.AgentLoopWorkflowName,
		temporalkit.AgentLoopInput{RunID: runID, OrgID: s.OrgID, Agent: s.AgentName},
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("signal workflow: %v", err))
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"conversation_id": conv.ID,
		"run_id":          runID,
		"message_id":      msg.ID,
	})
}

// ensureRun returns the conversation's live run, creating a fresh one when
// the conversation has none or its last run already finished.
func (s *Server) ensureRun(ctx context.Context, conv *domain.Conversation) (string, error) {
	if conv.RunID != "" {
		run, err := s.Agentkit.GetRun(ctx, conv.RunID)
		if err != nil && !isNotFound(err) {
			return "", err
		}
		if err == nil && run.Status == agentkit.RunRunning {
			return run.ID, nil
		}
	}
	runID := agentkit.NewID()
	if err := s.Agentkit.CreateRun(ctx, agentkit.Run{
		ID:     runID,
		OrgID:  s.OrgID,
		Agent:  s.AgentName,
		Status: agentkit.RunRunning,
	}); err != nil {
		return "", err
	}
	if err := s.Domain.SetConversationRun(ctx, conv.ID, runID); err != nil {
		return "", err
	}
	return runID, nil
}
