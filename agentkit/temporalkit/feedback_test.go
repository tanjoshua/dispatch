package temporalkit

import (
	"strings"
	"testing"
)

// TestRejectionFeedbackRecognized locks the two halves of the rejection
// contract together: whatever wording RejectionFeedback shows the agent,
// IsRejectionFeedback must recognize. This is the guardrail against the two
// drifting apart — the failure mode the old hand-copied 25-char prefix match
// invited (change the wording in one place, silently break recognition in the
// other).
func TestRejectionFeedbackRecognized(t *testing.T) {
	for _, reason := range []string{"Too formal", "", "has: punctuation\nand a newline"} {
		content := RejectionFeedback(reason)
		if !IsRejectionFeedback(content) {
			t.Errorf("IsRejectionFeedback(RejectionFeedback(%q)) = false, want true", reason)
		}
	}
	if r := RejectionFeedback(""); !strings.Contains(r, "no reason given") {
		t.Errorf("empty reason should render a placeholder, got %q", r)
	}
}

// TestNonRejectionNotRecognized guards the other direction: ordinary tool
// results and failures must not read as rejections.
func TestNonRejectionNotRecognized(t *testing.T) {
	for _, content := range []string{
		"",
		`{"status":"sent"}`,
		"Tool execution failed: boom",
		"ok",
		"The dispatcher approved this action.",
	} {
		if IsRejectionFeedback(content) {
			t.Errorf("IsRejectionFeedback(%q) = true, want false", content)
		}
	}
}
