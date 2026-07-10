// Package notify delivers escalation notifications to dispatchers who don't
// have the UI open (OVERVIEW §6.3 #13). The escalate tool's description tells
// the model its call summons a human *now*; the model calibrates its safety
// behavior on that claim, so before any pilot there must be a real delivery
// path behind it — this package is that path.
//
// Delivery is best-effort by design: the attention_state projection on the
// conversation is the durable record (set before any notification is
// attempted), and a lost page degrades to the pre-notification behavior of
// flagged-in-UI. Callers notify only on the not-flagged → flagged transition,
// which both dedupes activity retries and stops a repeat-escalating agent
// from re-paging an already-summoned dispatcher.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Escalation is one "a human should engage now" event.
type Escalation struct {
	OrgID          string `json:"org_id"`
	ConversationID string `json:"conversation_id"`
	CustomerName   string `json:"customer_name,omitempty"`
	Reason         string `json:"reason"`
	// Source says what raised it: "escalate_tool" (the agent judged a human
	// should step in) or "turn_budget" (the loop guard paused the agent).
	Source string `json:"source"`
}

// Notifier delivers an escalation to a dispatcher outside the app UI.
type Notifier interface {
	Notify(ctx context.Context, e Escalation) error
}

// Webhook POSTs each escalation as JSON to a fixed URL. The payload carries a
// human-readable "text" field alongside the structured fields, so a Slack
// (or compatible) incoming-webhook URL works with no adapter in between.
type Webhook struct {
	URL    string
	Client *http.Client
}

func NewWebhook(url string) *Webhook {
	return &Webhook{URL: url, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (w *Webhook) Notify(ctx context.Context, e Escalation) error {
	who := e.CustomerName
	if who == "" {
		who = "a customer"
	}
	payload := struct {
		Text string `json:"text"`
		Escalation
	}{
		Text:       fmt.Sprintf("🚨 Dispatch needs a human: conversation with %s — %s", who, e.Reason),
		Escalation: e,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notify webhook: %s returned %s", w.URL, resp.Status)
	}
	return nil
}
