package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"dispatch/app/domain"
	"dispatch/app/packs"
)

func (s *Server) registry() packs.Registry {
	if s.Packs == nil {
		return packs.Default()
	}
	return s.Packs
}

func (s *Server) handlePacks(w http.ResponseWriter, _ *http.Request) {
	items := []packs.Pack{}
	for _, p := range s.registry() {
		items = append(items, p)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"packs": items})
}

func (s *Server) handleListPlaybooks(w http.ResponseWriter, r *http.Request) {
	items, err := s.Domain.ListPlaybooks(r.Context(), s.DefaultOrgID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"playbooks": items})
}

func packForConfig(reg packs.Registry, raw json.RawMessage, agent string) (packs.Pack, bool) {
	var envelope struct {
		Pack string `json:"pack"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if p, ok := reg[envelope.Pack]; ok {
		return p, true
	}
	for _, p := range reg {
		if p.AgentName == agent {
			return p, true
		}
	}
	return packs.Pack{}, false
}

func (s *Server) handleGetPlaybook(w http.ResponseWriter, r *http.Request) {
	pb, err := s.Domain.GetPlaybookForOrg(r.Context(), s.DefaultOrgID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			writeError(w, 404, "playbook not found")
		} else {
			writeError(w, 500, err.Error())
		}
		return
	}
	p, ok := packForConfig(s.registry(), pb.Config, pb.Agent)
	if !ok {
		writeError(w, 422, "playbook references an unavailable pack")
		return
	}
	writeJSON(w, 200, map[string]any{"playbook": pb, "effective": packs.EffectiveConfig(p, pb.Config)})
}

type updatePlaybookRequest struct {
	CommandID       string          `json:"command_id"`
	ExpectedVersion int64           `json:"expected_version"`
	Config          json.RawMessage `json:"config"`
}

func (s *Server) handleUpdatePlaybook(w http.ResponseWriter, r *http.Request) {
	var req updatePlaybookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	current, err := s.Domain.GetPlaybookForOrg(r.Context(), s.DefaultOrgID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			writeError(w, 404, "playbook not found")
		} else {
			writeError(w, 500, err.Error())
		}
		return
	}
	p, ok := packForConfig(s.registry(), req.Config, current.Agent)
	if !ok {
		writeJSON(w, 422, map[string]any{"error": "invalid_config", "fields": map[string]string{"pack": "unknown pack"}})
		return
	}
	if err := packs.ValidateConfig(p, req.Config); err != nil {
		if validation, ok := err.(*packs.ValidationError); ok {
			writeJSON(w, 422, map[string]any{"error": "invalid_config", "fields": validation.Fields})
			return
		}
		writeError(w, 422, err.Error())
		return
	}
	actor, err := s.actor(r)
	if err != nil {
		writeError(w, 401, err.Error())
		return
	}
	updated, err := s.Domain.UpdatePlaybookConfig(r.Context(), s.DefaultOrgID, current.ID, req.ExpectedVersion, req.Config, req.CommandID, actor)
	if errors.Is(err, domain.ErrVersionConflict) {
		fresh, _ := s.Domain.GetPlaybookForOrg(r.Context(), s.DefaultOrgID, current.ID)
		writeJSON(w, 409, map[string]any{"error": "version_conflict", "code": "version_conflict", "current": fresh})
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"playbook": updated, "effective": packs.EffectiveConfig(p, updated.Config)})
}

func (s *Server) actor(r *http.Request) (string, error) {
	if s.ActorProvider == nil {
		return StaticActorProvider("dispatcher:dev").ActorID(r)
	}
	return s.ActorProvider.ActorID(r)
}
