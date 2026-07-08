// Package server exposes the dispatch JSON API. The API is the contract the
// React SPA builds against; the UI reads Postgres projections and writes
// signals — it never touches workflows directly.
package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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
	Sender *channel.Sender
	// DefaultOrgID scopes the read API (conversation list) until auth lands (a
	// 000 §8 non-goal). The inbound path no longer reads an org global — it
	// resolves org from the channel connection (design/002).
	DefaultOrgID string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/dev/inbound", s.handleDevInbound)
	mux.HandleFunc("POST /api/simulate/inbound", s.handleDevInbound) // deprecated alias (design/002 §9)
	mux.HandleFunc("GET /api/conversations", s.handleListConversations)
	mux.HandleFunc("GET /api/conversations/{id}", s.handleGetConversation)
	mux.HandleFunc("POST /api/actions/{id}/decision", s.handleDecision)
	mux.HandleFunc("POST /api/conversations/{id}/reply", s.handleDispatcherReply)
	mux.HandleFunc("POST /api/conversations/{id}/acknowledge", s.handleAcknowledge)
	mux.HandleFunc("GET /api/runs/{id}/events", s.handleRunEvents)
	return cors(mux)
}

// cors allows the Vite dev server during development.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
	LastMessage  *domain.Message     `json:"last_message,omitempty"`
	PendingCount int                 `json:"pending_count"`
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
		msgs, err := s.Domain.ListMessages(ctx, c.ID)
		if err == nil && len(msgs) > 0 {
			sum.LastMessage = &msgs[len(msgs)-1]
		}
		if c.RunID != "" {
			actions, err := s.Agentkit.ListActionsByRun(ctx, c.RunID)
			if err == nil {
				for _, a := range actions {
					if a.State == agentkit.ActionPendingApproval {
						sum.PendingCount++
					}
				}
			}
		}
		summaries = append(summaries, sum)
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": summaries})
}

type conversationDetail struct {
	Conversation domain.Conversation `json:"conversation"`
	Customer     *domain.Customer    `json:"customer"`
	Messages     []domain.Message    `json:"messages"`
	Job          *domain.Job         `json:"job,omitempty"`
	Run          *agentkit.Run       `json:"run,omitempty"`
	Actions      []agentkit.Action   `json:"actions"`
}

func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	conv, err := s.Domain.GetConversation(ctx, r.PathValue("id"))
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail := conversationDetail{Conversation: *conv, Actions: []agentkit.Action{}}
	if cust, err := s.Domain.GetCustomer(ctx, conv.CustomerID); err == nil {
		detail.Customer = cust
	}
	detail.Messages, err = s.Domain.ListMessages(ctx, conv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if job, err := s.Domain.GetJobByConversation(ctx, conv.ID); err == nil {
		detail.Job = job
	}
	if conv.RunID != "" {
		if run, err := s.Agentkit.GetRun(ctx, conv.RunID); err == nil {
			detail.Run = run
		}
		if actions, err := s.Agentkit.ListActionsByRun(ctx, conv.RunID); err == nil {
			detail.Actions = actions
		}
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Agentkit.ListEventsByRun(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []agentkit.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
