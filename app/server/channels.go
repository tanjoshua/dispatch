package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"dispatch/app/channel"
	"dispatch/app/domain"
)

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	items, err := s.Domain.ListChannelConnections(r.Context(), orgID(r))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	connections := make([]channelResponse, 0, len(items))
	for _, item := range items {
		connections = append(connections, channelResponseFromDomain(item))
	}
	writeJSON(w, 200, map[string]any{"connections": connections, "kinds": channel.Kinds()})
}

type channelResponse struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Kind      string    `json:"kind"`
	Address   string    `json:"address"`
	Status    string    `json:"status"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

func channelResponseFromDomain(connection domain.ChannelConnection) channelResponse {
	return channelResponse{
		ID: connection.ID, OrgID: connection.OrgID, Kind: connection.Kind,
		Address: connection.Address, Status: connection.Status,
		Version: connection.Version, CreatedAt: connection.CreatedAt,
	}
}

type createChannelRequest struct {
	CommandID string `json:"command_id"`
	Kind      string `json:"kind"`
	Address   string `json:"address"`
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if decodeStrictJSON(w, r, &req) != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	req.CommandID = strings.TrimSpace(req.CommandID)
	req.Kind = strings.TrimSpace(req.Kind)
	req.Address = strings.TrimSpace(req.Address)
	fields := map[string]string{}
	if req.CommandID == "" {
		fields["command_id"] = "is required"
	} else if len(req.CommandID) > 128 {
		fields["command_id"] = "must be 128 characters or fewer"
	}
	if req.Kind != "dev" {
		fields["kind"] = "this channel kind is coming soon"
	}
	if req.Address == "" {
		fields["address"] = "is required"
	}
	if len(fields) > 0 {
		writeJSON(w, 422, map[string]any{"error": "invalid_channel", "fields": fields})
		return
	}
	actor, err := s.actor(r)
	if err != nil {
		writeError(w, 401, err.Error())
		return
	}
	c, err := s.Domain.CreateChannelConnection(r.Context(), domain.ChannelConnection{
		OrgID: orgID(r), Kind: req.Kind, Address: req.Address,
	}, req.CommandID, actor)
	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, 422, "agent behavior is not configured for organization")
		return
	}
	if errors.Is(err, domain.ErrIdempotencyKeyReuse) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "idempotency_key_reuse", "code": "idempotency_key_reuse"})
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, channelResponseFromDomain(*c))
}
