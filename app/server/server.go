// Package server exposes the dispatch JSON API. The API is the contract the
// React SPA builds against; the UI reads Postgres projections and writes
// signals — it never touches workflows directly.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	temporalclient "go.temporal.io/sdk/client"

	"dispatch/agentkit"
	akstore "dispatch/agentkit/store"
	"dispatch/app/channel"
	"dispatch/app/domain"
)

type Server struct {
	Domain   *domain.Store
	Agentkit akstore.Store
	Temporal temporalclient.Client
	Router   *channel.Router
	// Sender is the shared outbound path, used when a dispatcher replies to the
	// customer directly (design/003-dispatcher-as-participant.md). It is the same
	// path the agent's send_message tool uses.
	Sender            *channel.Sender
	PrincipalProvider PrincipalProvider
}

type Role string

const (
	RoleMember     Role = "member"
	RoleAdmin      Role = "admin"
	RoleDispatcher Role = "dispatcher"
)

// Principal is the authenticated organization and actor for one request.
// Client payloads never provide either value.
type Principal struct {
	OrgID   string
	ActorID string
	Roles   []Role
}

func (p Principal) HasRole(role Role) bool {
	for _, candidate := range p.Roles {
		if candidate == RoleAdmin || candidate == role {
			return true
		}
	}
	return false
}

// PrincipalProvider is the authentication seam. Production implementations
// resolve a session/token; development opts into StaticPrincipalProvider.
type PrincipalProvider interface {
	ResolvePrincipal(*http.Request) (Principal, error)
}

type StaticPrincipalProvider struct {
	Principal Principal
}

func (p StaticPrincipalProvider) ResolvePrincipal(*http.Request) (Principal, error) {
	if p.Principal.OrgID == "" || p.Principal.ActorID == "" {
		return Principal{}, errors.New("static principal is incomplete")
	}
	return p.Principal, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /api/dev/inbound", s.require(RoleDispatcher, http.HandlerFunc(s.handleDevInbound)))
	mux.Handle("POST /api/simulate/inbound", s.require(RoleDispatcher, http.HandlerFunc(s.handleDevInbound))) // deprecated alias
	mux.Handle("GET /api/conversations", s.require(RoleMember, http.HandlerFunc(s.handleListConversations)))
	mux.Handle("GET /api/conversations/{id}", s.require(RoleMember, http.HandlerFunc(s.handleGetConversation)))
	mux.Handle("POST /api/actions/{id}/decision", s.require(RoleDispatcher, http.HandlerFunc(s.handleDecision)))
	mux.Handle("POST /api/conversations/{id}/reply", s.require(RoleDispatcher, http.HandlerFunc(s.handleDispatcherReply)))
	mux.Handle("POST /api/conversations/{id}/cases/{caseID}/correction", s.require(RoleDispatcher, http.HandlerFunc(s.handleCaseCorrection)))
	mux.Handle("POST /api/conversations/{id}/acknowledge", s.require(RoleDispatcher, http.HandlerFunc(s.handleAcknowledge)))
	mux.Handle("GET /api/runs/{id}/events", s.require(RoleMember, http.HandlerFunc(s.handleRunEvents)))
	mux.Handle("GET /api/stats/decisions", s.require(RoleMember, http.HandlerFunc(s.handleDecisionStats)))
	mux.Handle("GET /api/org/agent-behavior", s.require(RoleMember, http.HandlerFunc(s.handleGetAgentBehavior)))
	mux.Handle("PATCH /api/org/agent-behavior", s.require(RoleAdmin, http.HandlerFunc(s.handleUpdateAgentBehavior)))
	mux.Handle("GET /api/org/profile", s.require(RoleMember, http.HandlerFunc(s.handleGetOrgProfile)))
	mux.Handle("PATCH /api/org/profile", s.require(RoleAdmin, http.HandlerFunc(s.handleUpdateOrgProfile)))
	mux.Handle("GET /api/channels", s.require(RoleMember, http.HandlerFunc(s.handleListChannels)))
	mux.Handle("POST /api/channels", s.require(RoleAdmin, http.HandlerFunc(s.handleCreateChannel)))
	return cors(s.authenticate(mux))
}

type principalContextKey struct{}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.PrincipalProvider == nil {
			writeError(w, http.StatusInternalServerError, "authentication is not configured")
			return
		}
		principal, err := s.PrincipalProvider.ResolvePrincipal(r)
		if err != nil || principal.OrgID == "" || principal.ActorID == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
	})
}

func (s *Server) require(role Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalForRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if !principal.HasRole(role) {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func principalForRequest(r *http.Request) (Principal, bool) {
	principal, ok := r.Context().Value(principalContextKey{}).(Principal)
	return principal, ok
}

func (s *Server) actor(r *http.Request) (string, error) {
	principal, ok := principalForRequest(r)
	if !ok {
		return "", errors.New("principal unavailable")
	}
	return principal.ActorID, nil
}

func orgID(r *http.Request) string {
	principal, _ := principalForRequest(r)
	return principal.OrgID
}

// handleDecisionStats serves per-tool outcomes and human-decision latency as
// evaluation evidence for future product-level policy changes.
func (s *Server) handleDecisionStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.Agentkit.DecisionStats(r.Context(), orgID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stats == nil {
		stats = []agentkit.ToolDecisionStats{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": stats})
}

// cors allows cross-origin browser access only when DISPATCH_DEV_CORS is set.
// The normal dev path doesn't need it (Vite proxies /api same-origin), and a
// wildcard must never ship (OVERVIEW §6.2 #10).
func cors(next http.Handler) http.Handler {
	if os.Getenv("DISPATCH_DEV_CORS") == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func isNotFound(err error) bool {
	return errors.Is(err, domain.ErrNotFound) || errors.Is(err, akstore.ErrNotFound)
}

// conversationSummary is one row in the dispatcher's conversation list.
type conversationSummary struct {
	Conversation domain.Conversation `json:"conversation"`
	Customer     *domain.Customer    `json:"customer"`
	// Contact is the customer's address on this thread's channel (their phone on
	// dev/WhatsApp) — resolved from the contact identity, since it no longer
	// lives on the customer (design/004-domain-remodel.md §3).
	Contact      string          `json:"contact"`
	LastMessage  *domain.Message `json:"last_message,omitempty"`
	PendingCount int             `json:"pending_count"`
	// OldestPendingAt is when the longest-waiting pending action was proposed.
	// Decision latency is the existential product risk (WhatsApp expectations
	// vs. review queues) — the age is worn on the row, not buried in a detail
	// view (OVERVIEW §6.3 #14).
	OldestPendingAt *time.Time `json:"oldest_pending_at,omitempty"`
}

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	requestOrgID := orgID(r)
	convs, err := s.Domain.ListConversations(ctx, requestOrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	summaries := make([]conversationSummary, 0, len(convs))
	for _, c := range convs {
		sum := conversationSummary{Conversation: c}
		if cust, err := s.Domain.GetCustomer(ctx, c.CustomerID); err == nil {
			sum.Customer = cust
		}
		sum.Contact, _ = s.Domain.ContactAddressForConversation(ctx, c.ID)
		msgs, err := s.Domain.ListMessages(ctx, c.ID)
		if err == nil && len(msgs) > 0 {
			sum.LastMessage = &msgs[len(msgs)-1]
		}
		if runID, err := s.Domain.LatestRunIDForConversation(ctx, c.ID); err == nil && runID != "" {
			actions, err := s.Agentkit.ListActionsByRun(ctx, requestOrgID, runID)
			if err == nil {
				for _, a := range actions {
					if a.State == agentkit.ActionPendingApproval {
						sum.PendingCount++
						at := a.ProposedAt
						if sum.OldestPendingAt == nil || at.Before(*sum.OldestPendingAt) {
							sum.OldestPendingAt = &at
						}
					}
				}
			}
		}
		summaries = append(summaries, sum)
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": summaries})
}

type conversationDetail struct {
	Conversation    domain.Conversation     `json:"conversation"`
	Customer        *domain.Customer        `json:"customer"`
	Contact         string                  `json:"contact"` // customer's address on this thread's channel (design/004 §3)
	Messages        []domain.Message        `json:"messages"`
	Case            *domain.Case            `json:"case,omitempty"`
	ContactIdentity *domain.ContactIdentity `json:"contact_identity,omitempty"`
	CandidateCases  []domain.Case           `json:"candidate_cases"`
	CurrentDraft    *agentkit.Action        `json:"current_draft,omitempty"`
	Run             *agentkit.Run           `json:"run,omitempty"`
	Actions         []agentkit.Action       `json:"actions"`
	CurrentStage    string                  `json:"current_stage,omitempty"`
	LastModel       string                  `json:"last_model,omitempty"`
}

func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	requestOrgID := orgID(r)
	conv, err := s.Domain.GetConversation(ctx, requestOrgID, r.PathValue("id"))
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail := conversationDetail{Conversation: *conv, Actions: []agentkit.Action{}, CandidateCases: []domain.Case{}}
	if cust, err := s.Domain.GetCustomer(ctx, conv.CustomerID); err == nil {
		detail.Customer = cust
	}
	detail.Contact, _ = s.Domain.ContactAddressForConversation(ctx, conv.ID)
	detail.ContactIdentity, _ = s.Domain.GetContactIdentity(ctx, conv.ContactIdentityID)
	detail.CandidateCases, _ = s.Domain.ListCasesForCustomer(ctx, conv.OrgID, conv.CustomerID, 3)
	detail.Messages, err = s.Domain.ListMessages(ctx, conv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runID, err := s.Domain.LatestRunIDForConversation(ctx, conv.ID); err == nil && runID != "" {
		detail.CurrentStage, _ = s.Domain.StageForRun(ctx, runID)
		if events, err := s.Agentkit.ListEventsByRun(ctx, requestOrgID, runID); err == nil {
			for i := len(events) - 1; i >= 0; i-- {
				if events[i].Type == agentkit.EventLLMCompleted {
					var usage struct {
						Model string `json:"model"`
					}
					if json.Unmarshal(events[i].Payload, &usage) == nil {
						detail.LastModel = usage.Model
					}
					break
				}
			}
		}
		if c, err := s.Domain.SelectedCaseForRun(ctx, runID); err == nil {
			detail.Case = c
		}
		if run, err := s.Agentkit.GetRun(ctx, requestOrgID, runID); err == nil {
			detail.Run = run
		}
		if actions, err := s.Agentkit.ListActionsByRun(ctx, requestOrgID, runID); err == nil {
			detail.Actions = actions
			for i := range actions {
				if actions[i].State == agentkit.ActionPendingApproval && (actions[i].Tool == "propose_response" || actions[i].Tool == "send_message") {
					a := actions[i]
					detail.CurrentDraft = &a
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Agentkit.ListEventsByRun(r.Context(), orgID(r), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []agentkit.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
