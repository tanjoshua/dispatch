package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"dispatch/app/domain"
)

type orgProfile struct {
	BusinessName string        `json:"business_name"`
	Hours        string        `json:"hours"`
	ServiceArea  string        `json:"service_area"`
	Facts        []profileFact `json:"facts"`
}
type profileFact struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Text  string `json:"text"`
}

func profileFromSettings(raw json.RawMessage) (orgProfile, map[string]json.RawMessage) {
	bag := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &bag)
	var p orgProfile
	_ = json.Unmarshal(bag["profile"], &p)
	if p.Facts == nil {
		p.Facts = []profileFact{}
	}
	return p, bag
}
func (s *Server) handleGetOrgProfile(w http.ResponseWriter, r *http.Request) {
	requestOrgID := orgID(r)
	o, err := s.Domain.GetOrganization(r.Context(), requestOrgID, requestOrgID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	p, _ := profileFromSettings(o.Settings)
	writeJSON(w, 200, map[string]any{"profile": p, "version": o.Version})
}

type updateProfileRequest struct {
	CommandID       string     `json:"command_id"`
	ExpectedVersion int64      `json:"expected_version"`
	Profile         orgProfile `json:"profile"`
}

func (s *Server) handleUpdateOrgProfile(w http.ResponseWriter, r *http.Request) {
	var req updateProfileRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	requestOrgID := orgID(r)
	o, err := s.Domain.GetOrganization(r.Context(), requestOrgID, requestOrgID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	_, bag := profileFromSettings(o.Settings)
	profileRaw, _ := json.Marshal(req.Profile)
	bag["profile"] = profileRaw
	settings, _ := json.Marshal(bag)
	actor, err := s.actor(r)
	if err != nil {
		writeError(w, 401, err.Error())
		return
	}
	updated, err := s.Domain.UpdateOrganizationSettings(r.Context(), requestOrgID, req.ExpectedVersion, settings, req.CommandID, actor)
	if errors.Is(err, domain.ErrVersionConflict) {
		fresh, _ := s.Domain.GetOrganization(r.Context(), requestOrgID, requestOrgID)
		p, _ := profileFromSettings(fresh.Settings)
		writeJSON(w, 409, map[string]any{"error": "version_conflict", "code": "version_conflict", "current": map[string]any{"profile": p, "version": fresh.Version}})
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	p, _ := profileFromSettings(updated.Settings)
	writeJSON(w, 200, map[string]any{"profile": p, "version": updated.Version})
}
