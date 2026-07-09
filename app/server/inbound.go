package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"dispatch/app/channel"
	"dispatch/app/channel/dev"
)

type devInboundRequest struct {
	Phone string `json:"phone"`
	Name  string `json:"name"`
	Text  string `json:"text"`
	// ProviderMessageID optionally simulates a transport message ID so the
	// inbound dedupe path (webhook retries) can be exercised on the dev channel.
	ProviderMessageID string `json:"provider_message_id,omitempty"`
}

// handleDevInbound is the dev channel's inbound transport edge. It resolves the
// dev ChannelConnection and hands off to the shared Router, exactly as a real
// WhatsApp webhook handler would resolve its connection and call the same
// Router.Receive. All the logic that matters (org resolution, conversation/run
// lifecycle, message + event, signal-with-start) lives in the Router
// (design/002-organization-and-channels.md §6).
func (s *Server) handleDevInbound(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req devInboundRequest
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

	conn, err := s.Domain.GetChannelConnectionByAddress(ctx, dev.Name, dev.Address)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusInternalServerError, "dev channel connection not seeded")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	res, err := s.Router.Receive(ctx, *conn, channel.InboundMessage{
		From:              req.Phone,
		Name:              strings.TrimSpace(req.Name),
		Text:              req.Text,
		ProviderMessageID: strings.TrimSpace(req.ProviderMessageID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, res)
}
