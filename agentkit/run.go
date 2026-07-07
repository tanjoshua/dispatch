package agentkit

import "time"

// RunStatus is the lifecycle state of a Run.
type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
)

// Run is one durable agent execution — e.g. one intake conversation from
// first inbound message to close. Its ordered history lives in the event
// log; its live state is the workflow.
type Run struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Agent     string    `json:"agent"` // agent definition name
	Status    RunStatus `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WorkflowID returns the Temporal workflow ID for a run: "run-{runID}".
func WorkflowID(runID string) string { return "run-" + runID }
