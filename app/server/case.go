package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"dispatch/app/domain"
)

type caseCorrectionRequest struct {
	ExpectedVersion  int64           `json:"expected_version"`
	Patch            json.RawMessage `json:"patch"`
	SourceMessageIDs []string        `json:"source_message_ids"`
}

func (s *Server) handleCaseCorrection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	conv, err := s.Domain.GetConversation(ctx, s.DefaultOrgID, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "conversation not found")
		return
	}
	var req caseCorrectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ExpectedVersion < 1 || !json.Valid(req.Patch) {
		writeError(w, http.StatusBadRequest, "expected_version and a valid patch are required")
		return
	}
	runID, err := s.Domain.LatestRunIDForConversation(ctx, conv.ID)
	if err != nil || runID == "" {
		writeError(w, http.StatusConflict, "conversation has no run for case correction")
		return
	}
	c, err := s.Domain.UpdateCase(ctx, runID, r.PathValue("caseID"), req.ExpectedVersion, req.Patch, req.SourceMessageIDs)
	if errors.Is(err, domain.ErrVersionConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "version_conflict", "code": "version_conflict"})
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}
