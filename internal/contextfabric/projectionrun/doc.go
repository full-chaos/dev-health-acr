// Package projectionrun is the hosting-composition layer that schedules
// contextfabric.ProjectionWorker across every configured organization and
// source. It owns scheduling, retries, cancellation, failure isolation, and
// the CHAOS-3753 amendment's single-flight-per-organization guarantee.
//
// It deliberately does not live inside internal/contextfabric: that domain
// package's projector.go handles one (org, source) pair with no queue,
// database, or concurrency concerns of its own
// (internal/contextfabric/AGENTS.md: "Keep HTTP, MCP, database, and queue
// concerns out of the domain engine"). This package is the queue/scheduling
// concern; it drives the domain worker, it doesn't reimplement it.
package projectionrun
