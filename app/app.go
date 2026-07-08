// Package app holds cross-cutting constants for the dispatch application.
package app

// TaskQueue is the Temporal task queue shared by the worker and the server.
const TaskQueue = "dispatch"

// OrgID is the seeded dev org (see migration 0003). The inbound path resolves
// org from the channel connection a message arrives on (design/002); this
// constant remains only as the read-side default for the dispatcher UI until
// auth lands, and as the id the migration seeds.
const OrgID = "org_dev"
