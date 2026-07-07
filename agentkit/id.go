package agentkit

import "github.com/oklog/ulid/v2"

// NewID returns a new ULID string. IDs are non-deterministic: inside
// Temporal, generate them only in activities, never in workflow code.
func NewID() string { return ulid.Make().String() }
