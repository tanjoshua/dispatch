package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookNotify(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %s", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("payload not JSON: %v", err)
		}
	}))
	defer srv.Close()

	w := NewWebhook(srv.URL)
	err := w.Notify(context.Background(), Escalation{
		OrgID:          "org_dev",
		ConversationID: "conv_1",
		CustomerName:   "Pat",
		Reason:         "gas smell reported",
		Source:         "escalate_tool",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}

	text, _ := got["text"].(string)
	if !strings.Contains(text, "Pat") || !strings.Contains(text, "gas smell reported") {
		t.Errorf("text field missing customer/reason: %q", text)
	}
	if got["conversation_id"] != "conv_1" || got["source"] != "escalate_tool" {
		t.Errorf("structured fields wrong: %v", got)
	}
}

func TestWebhookNotifyNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	if err := NewWebhook(srv.URL).Notify(context.Background(), Escalation{Reason: "x"}); err == nil {
		t.Fatal("want error on non-2xx response")
	}
}
