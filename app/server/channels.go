package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"dispatch/app/channel"
	"dispatch/app/domain"
)

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	items, err := s.Domain.ListChannelConnections(r.Context(), s.DefaultOrgID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"connections": items, "kinds": channel.Kinds()})
}

type createChannelRequest struct {
	CommandID         string `json:"command_id"`
	Kind              string `json:"kind"`
	Address           string `json:"address"`
	DefaultPlaybookID string `json:"default_playbook_id"`
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.Kind != "dev" {
		writeJSON(w, 422, map[string]any{"error": "coming soon", "fields": map[string]string{"kind": "this channel kind is coming soon"}})
		return
	}
	if strings.TrimSpace(req.Address) == "" {
		writeJSON(w, 422, map[string]any{"error": "invalid_channel", "fields": map[string]string{"address": "is required"}})
		return
	}
	actor, err := s.actor(r)
	if err != nil {
		writeError(w, 401, err.Error())
		return
	}
	c, err := s.Domain.CreateChannelConnection(r.Context(), domain.ChannelConnection{ID: req.CommandID, OrgID: s.DefaultOrgID, Kind: req.Kind, Address: req.Address, DefaultPlaybookID: req.DefaultPlaybookID}, req.CommandID, actor)
	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, 422, "playbook does not belong to organization")
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, c)
}

type updateChannelRequest struct {
	CommandID         string `json:"command_id"`
	ExpectedVersion   int64  `json:"expected_version"`
	DefaultPlaybookID string `json:"default_playbook_id"`
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	var req updateChannelRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	actor, err := s.actor(r)
	if err != nil {
		writeError(w, 401, err.Error())
		return
	}
	c, err := s.Domain.UpdateChannelConnectionPlaybook(r.Context(), s.DefaultOrgID, r.PathValue("id"), req.DefaultPlaybookID, req.ExpectedVersion, req.CommandID, actor)
	if errors.Is(err, domain.ErrVersionConflict) {
		connections, _ := s.Domain.ListChannelConnections(r.Context(), s.DefaultOrgID)
		var current *domain.ChannelConnection
		for i := range connections {
			if connections[i].ID == r.PathValue("id") {
				current = &connections[i]
			}
		}
		writeJSON(w, 409, map[string]any{"error": "version_conflict", "code": "version_conflict", "current": current})
		return
	}
	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, 404, "channel or playbook not found")
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, c)
}
