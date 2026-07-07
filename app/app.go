// Package app holds cross-cutting constants for the dispatch application.
package app

// TaskQueue is the Temporal task queue shared by the worker and the server.
const TaskQueue = "dispatch"

// OrgID is the single org of v1. Every table carries org_id from day one so
// multi-tenancy is a data change, not a schema retrofit.
const OrgID = "org_dev"
