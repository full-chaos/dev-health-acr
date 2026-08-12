package falkorgraph

import "context"

// row is one result row from a Cypher query, decoded into this package's own
// neutral value shapes -- never falkordb-go's *Node/*Edge/*Record types,
// which stay confined to sdkConn (client.go). Keeping the boundary here (not
// at the raw wire-protocol level) is what makes fakeConn a usable unit-test
// double: it returns row values directly, with no compact-protocol response
// to fabricate.
type row map[string]interface{}

// get returns the raw value for key, or nil if absent.
func (r row) get(key string) interface{} {
	if r == nil {
		return nil
	}
	return r[key]
}

// node is a decoded graph node: identity (this backend's own ID, opaque
// outside identity.go) plus labels and properties.
type node struct {
	ID         uint64
	Labels     []string
	Properties map[string]interface{}
}

// edge is a decoded graph relationship.
type edge struct {
	ID         uint64
	Relation   string
	SourceID   uint64
	DestID     uint64
	Properties map[string]interface{}
}

// constraintStatus is one row of `CALL db.constraints()`.
type constraintStatus struct {
	Type       string // "UNIQUE" or "MANDATORY"
	Label      string
	Properties []string
	EntityType string // "NODE" or "RELATIONSHIP"
	Status     string // "PENDING", "UNDER CONSTRUCTION", "OPERATIONAL", "FAILED", ...
}

// indexStatus is one row of `CALL db.indexes()`.
type indexStatus struct {
	Label      string
	Properties []string
	Types      map[string][]string // property -> index types (e.g. "RANGE", "FULLTEXT")
	EntityType string
}

// conn is the seam between falkorgraph's Cypher-construction logic and
// FalkorDB transport. The real implementation (sdkConn, client.go) is a
// compact-protocol codec over the pinned falkordb-go client; fakeConn
// (used by unit tests) returns canned rows with no network/process
// involved, mirroring zepgraph's `api` interface / fakeAPI pattern.
type conn interface {
	// query runs a read/write Cypher statement against graphKey and returns
	// its result rows, or an error. FalkorDB's own read-only/write
	// distinction (GRAPH.QUERY vs GRAPH.RO_QUERY) is chosen by the caller
	// via readOnly.
	query(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error)
	// deleteGraph issues GRAPH.DELETE. Deleting an already-absent graph key
	// must be treated as success by the caller (idempotent purge) --
	// implementations report ErrNotFound so callers can make that call
	// uniformly.
	deleteGraph(ctx context.Context, graphKey string) error
	// listGraphs issues GRAPH.LIST, used to answer "does this org's graph
	// exist" deliberately rather than by the side effect of a read (see
	// identity.go's ensureOrgGraph doc comment -- FalkorDB auto-creates a
	// graph key on any read against it, so existence cannot be inferred
	// from a query succeeding).
	listGraphs(ctx context.Context) ([]string, error)
	// constraints issues `CALL db.constraints()`.
	constraints(ctx context.Context, graphKey string) ([]constraintStatus, error)
	// indexes issues `CALL db.indexes()`.
	indexes(ctx context.Context, graphKey string) ([]indexStatus, error)
	// createIndex issues `CREATE INDEX FOR (n:label) ON (n.prop1, n.prop2, ...)`
	// (relationship=false) or `CREATE INDEX FOR ()-[r:type]-() ON (r.prop1, ...)`
	// (relationship=true) -- a unique RELATIONSHIP constraint's supporting
	// index MUST be the relationship-shaped form (verified live: a node-shaped
	// index of the same name/property does not satisfy it -- "missing
	// supporting exact-match index" on constraint creation). Index creation is
	// NOT idempotent (verified: a second create against an already-indexed
	// property errors "Attribute 'x' is already indexed") and
	// `CREATE INDEX IF NOT EXISTS` does not parse against this server version
	// -- callers must introspect via indexes() first (identity.go's bootstrap).
	createIndex(ctx context.Context, graphKey, label string, properties []string, relationship bool) error
	// createConstraint issues `GRAPH.CONSTRAINT CREATE`, a raw command (not
	// Cypher) that always returns "PENDING" -- constraint creation is
	// asynchronous; callers must poll constraints() for status=OPERATIONAL
	// (identity.go's bootstrap) before relying on it.
	createConstraint(ctx context.Context, graphKey string, unique bool, entityType, label string, properties []string) error
}
