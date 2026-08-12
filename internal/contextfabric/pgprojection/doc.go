// Package pgprojection holds the production, PostgreSQL-backed adapters
// contextfabric owns directly, mirroring how internal/contextfabric/zepgraph
// owns its backend rather than routing through internal/storage. It stays
// self-contained (its own SQL, its own *sql.DB dependency) so it never
// creates a storage <-> contextfabric import cycle and never has to extend
// internal/storage's shared interfaces for a contextfabric-only need.
package pgprojection
