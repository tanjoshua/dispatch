package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"dispatch/app/domain"
)

const settingsBodyLimit = 64 << 10

type agentBehaviorResponse struct {
	Behavior domain.AgentBehavior `json:"behavior"`
	Version  int64                `json:"version"`
}

type updateAgentBehaviorRequest struct {
	CommandID       string               `json:"command_id"`
	ExpectedVersion int64                `json:"expected_version"`
	Behavior        domain.AgentBehavior `json:"behavior"`
}

func (s *Server) handleGetAgentBehavior(w http.ResponseWriter, r *http.Request) {
	pb, err := s.Domain.GetAgentBehavior(r.Context(), orgID(r))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			writeError(w, http.StatusNotFound, "agent behavior is not configured")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load agent behavior")
		return
	}
	response, err := agentBehaviorFromPlaybook(pb)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "agent behavior configuration is invalid")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleUpdateAgentBehavior(w http.ResponseWriter, r *http.Request) {
	var req updateAgentBehaviorRequest
	if err := decodeStrictJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.CommandID = strings.TrimSpace(req.CommandID)
	req.Behavior = normalizeAgentBehavior(req.Behavior)
	fields := validateAgentBehaviorRequest(req)
	if len(fields) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":  "invalid_behavior",
			"fields": fields,
		})
		return
	}
	actor, err := s.actor(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "actor unavailable")
		return
	}
	updated, err := s.Domain.UpdateAgentBehavior(
		r.Context(), orgID(r), req.ExpectedVersion, req.Behavior, req.CommandID, actor,
	)
	if errors.Is(err, domain.ErrVersionConflict) {
		current, responseErr := agentBehaviorFromPlaybook(updated)
		if responseErr != nil {
			writeError(w, http.StatusInternalServerError, "could not load current agent behavior")
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "version_conflict",
			"code":    "version_conflict",
			"current": current,
		})
		return
	}
	if errors.Is(err, domain.ErrIdempotencyKeyReuse) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "idempotency_key_reuse",
			"code":  "idempotency_key_reuse",
		})
		return
	}
	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, http.StatusNotFound, "agent behavior is not configured")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update agent behavior")
		return
	}
	response, err := agentBehaviorFromPlaybook(updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load updated agent behavior")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func agentBehaviorFromPlaybook(pb *domain.Playbook) (agentBehaviorResponse, error) {
	if pb == nil {
		return agentBehaviorResponse{}, errors.New("nil playbook")
	}
	var config struct {
		Schema int                  `json:"schema"`
		Voice  domain.AgentBehavior `json:"voice"`
	}
	if err := json.Unmarshal(pb.Config, &config); err != nil {
		return agentBehaviorResponse{}, err
	}
	if config.Schema != 1 && config.Schema != 2 {
		return agentBehaviorResponse{}, errors.New("unsupported behavior schema")
	}
	if fields := validateAgentBehavior(config.Voice); len(fields) > 0 {
		return agentBehaviorResponse{}, errors.New("invalid behavior values")
	}
	return agentBehaviorResponse{Behavior: config.Voice, Version: pb.Version}, nil
}

func normalizeAgentBehavior(behavior domain.AgentBehavior) domain.AgentBehavior {
	behavior.AgentName = strings.TrimSpace(behavior.AgentName)
	behavior.Tone = strings.TrimSpace(behavior.Tone)
	behavior.CustomInstructions = strings.TrimSpace(behavior.CustomInstructions)
	return behavior
}

func validateAgentBehaviorRequest(req updateAgentBehaviorRequest) map[string]string {
	fields := validateAgentBehavior(req.Behavior)
	if req.CommandID == "" {
		fields["command_id"] = "is required"
	} else if len(req.CommandID) > 128 {
		fields["command_id"] = "must be 128 characters or fewer"
	}
	if req.ExpectedVersion < 1 {
		fields["expected_version"] = "must be at least 1"
	}
	return fields
}

func validateAgentBehavior(behavior domain.AgentBehavior) map[string]string {
	fields := map[string]string{}
	if behavior.AgentName == "" {
		fields["agent_name"] = "is required"
	} else if utf8.RuneCountInString(behavior.AgentName) > 80 {
		fields["agent_name"] = "must be 80 characters or fewer"
	}
	if behavior.Tone == "" {
		fields["tone"] = "is required"
	} else if utf8.RuneCountInString(behavior.Tone) > 240 {
		fields["tone"] = "must be 240 characters or fewer"
	}
	if utf8.RuneCountInString(behavior.CustomInstructions) > 4000 {
		fields["custom_instructions"] = "must be 4000 characters or fewer"
	}
	return fields
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, settingsBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON object")
		}
		return err
	}
	return nil
}
