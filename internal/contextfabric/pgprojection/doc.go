// Package pgprojection holds the production, PostgreSQL-backed
// ProjectionCheckpointStore. Checkpoints are a contextfabric-only concept
// (no other package needs them), so this package owns its own SQL and
// *sql.DB dependency directly -- mirroring how
// internal/contextfabric/falkorgraph owns its backend -- rather than adding
// a checkpoints table to internal/storage's shared surface.
//
// Approved-episode reads deliberately do NOT live here: they route through
// storage.EpisodeStore.ListSince (internal/storage/postgres and
// internal/storage/memory), because episodes are already an
// internal/storage-owned concept with its own writers
// (CreateIdempotent, Redact) and the projection worker is just one more
// EpisodeStore caller, not a reason to fork a parallel read path.
package pgprojection
