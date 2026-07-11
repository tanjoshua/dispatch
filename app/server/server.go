// Package server exposes the dispatch JSON API. The API is the contract the
// React SPA builds against; the UI reads Postgres projections and writes
// signals — it never touches workflows directly.
package server

import (
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
	"dispatch/app/packs"
)

type Server struct {
	Domain   *domain.Store
	Agentkit akstore.Store
	Temporal temporalclient.Client
	Router   *channel.Router
	// Sender is the shared outbound path, used when a dispatcher replies to the
	// customer directly (design/003-dispatcher-as-participant.md). It is the same
	// path the agent's send_message tool uses.
	Sender *channel.Sender
	// DefaultOrgID scopes the read API (conversation list) until auth lands (a
	// 000 §8 non-goal). The inbound path no longer reads an org global — it
	// resolves org from the channel connection (design/002).
	DefaultOrgID  string
	ActorProvider ActorProvider
	Packs         packs.Registry
}

// ActorProvider is the authentication seam for audit attribution. Development
// uses a configured actor; production can replace it without changing command
// payloads or trusting client-supplied identities.
type ActorProvider interface {
	ActorID(*http.Request) (string, error)
}
type StaticActorProvider string

func (p StaticActorProvider) ActorID(*http.Request) (string, error) {
	if p == "" {
		return "dispatcher:dev", nil
	}
	return string(p), nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/dev/inbound", s.handleDevInbound)
	mux.HandleFunc("POST /api/simulate/inbound", s.handleDevInbound) // deprecated alias (design/002 §9)
	mux.HandleFunc("GET /api/conversations", s.handleListConversations)
	mux.HandleFunc("GET /api/conversations/{id}", s.handleGetConversation)
	mux.HandleFunc("POST /api/actions/{id}/decision", s.handleDecision)
	mux.HandleFunc("POST /api/conversations/{id}/reply", s.handleDispatcherReply)
	mux.HandleFunc("POST /api/conversations/{id}/cases/{caseID}/correction", s.handleCaseCorrection)
	mux.HandleFunc("POST /api/conversations/{id}/acknowledge", s.handleAcknowledge)
	mux.HandleFunc("GET /api/runs/{id}/events", s.handleRunEvents)
	mux.HandleFunc("GET /api/stats/decisions", s.handleDecisionStats)
	mux.HandleFunc("GET /api/packs", s.handlePacks)
	mux.HandleFunc("GET /api/playbooks", s.handleListPlaybooks)
	mux.HandleFunc("GET /api/playbooks/{id}", s.handleGetPlaybook)
	mux.HandleFunc("PATCH /api/playbooks/{id}", s.handleUpdatePlaybook)
	mux.HandleFunc("GET /api/org/profile", s.handleGetOrgProfile)
	mux.HandleFunc("PATCH /api/org/profile", s.handleUpdateOrgProfile)
	mux.HandleFunc("GET /api/channels", s.handleListChannels)
	mux.HandleFunc("POST /api/channels", s.handleCreateChannel)
	mux.HandleFunc("PATCH /api/channels/{id}", s.handleUpdateChannel)
	return cors(mux)
}

// handleDecisionStats serves per-tool decision outcomes and human-decision
// latency — the evidence for moving tools between RequireApproval and
// AutoApprove (OVERVIEW §6.3 #14).
func (s *Server) handleDecisionStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.Agentkit.DecisionStats(r.Context(), s.DefaultOrgID)
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
	convs, err := s.Domain.ListConversations(ctx, s.DefaultOrgID)
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
			actions, err := s.Agentkit.ListActionsByRun(ctx, s.DefaultOrgID, runID)
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
	conv, err := s.Domain.GetConversation(ctx, s.DefaultOrgID, r.PathValue("id"))
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
		if events, err := s.Agentkit.ListEventsByRun(ctx, s.DefaultOrgID, runID); err == nil {
			for i := len(events)-1; i >= 0; i-- { if events[i].Type == agentkit.EventLLMCompleted { var usage struct{Model string `json:"model"`}; if json.Unmarshal(events[i].Payload,&usage)==nil { detail.LastModel=usage.Model }; break } }
		}
		if c, err := s.Domain.SelectedCaseForRun(ctx, runID); err == nil {
			detail.Case = c
		}
		if run, err := s.Agentkit.GetRun(ctx, s.DefaultOrgID, runID); err == nil {
			detail.Run = run
		}
		if actions, err := s.Agentkit.ListActionsByRun(ctx, s.DefaultOrgID, runID); err == nil {
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
	events, err := s.Agentkit.ListEventsByRun(r.Context(), s.DefaultOrgID, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []agentkit.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
