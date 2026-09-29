package directread

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread/gatevocab"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// EdgeWithheldReason re-exports the edge gate vocabulary.
type EdgeWithheldReason = gatevocab.EdgeWithheldReason

const (
	EdgeVisible            = gatevocab.EdgeVisible
	EdgeWithheldAttributes = gatevocab.EdgeWithheldAttributes
	EdgeWithheldSource     = gatevocab.EdgeWithheldSource
	EdgeWithheldTarget     = gatevocab.EdgeWithheldTarget
)

// EdgeDirection is the direction of an edge read, relative to the origin
// subject(s) of the page.
type EdgeDirection string

const (
	EdgeDirectionOut  EdgeDirection = "out"
	EdgeDirectionIn   EdgeDirection = "in"
	EdgeDirectionBoth EdgeDirection = "both"
)

// EdgeKey is the keyset position of a direct edge page: the relationship id.
// It is a total order: relationship_id is the edge's MERGE key and the graph
// holds a UNIQUE constraint on it (falkorgraph identity.go bootstrapSchema),
// so no two edges share one and a page can never skip or repeat an edge.
type EdgeKey struct {
	RelationshipID string `json:"r"`
}

// Less orders keys by relationship id: the ORDER BY of the graph query.
func (k EdgeKey) Less(other EdgeKey) bool { return k.RelationshipID < other.RelationshipID }

// EdgePageQuery is one bounded graph read of the edges that touch a set of
// origin subjects.
type EdgePageQuery struct {
	// Origins are the subjects whose edges are read (1 to MaxEdgeFrontier).
	Origins []contextfabric.SubjectRef
	// Exclude drops every edge that touches this subject (hop 2 does not
	// repeat the root's own hop-1 edges). Nil excludes nothing.
	Exclude *contextfabric.SubjectRef
	// Types are normalized relation types (graphrank.NormalizeRelation). An
	// empty list reads every type.
	Types     []string
	Direction EdgeDirection
	// EndKinds, when set, keeps only edges whose NON-origin end is one of
	// these subject kinds (find_subjects owned_by reads repository and
	// project ends of OWNED_BY_TEAM, not the team's work-item
	// attributions). Empty keeps every kind.
	EndKinds []string
	// After is the exclusive keyset lower bound. Nil starts at the first
	// edge.
	After *EdgeKey
	// Limit is the number of edges returned. The graph reads Limit+1 so the
	// page knows whether more follow.
	Limit int
	// ValidAt is the valid-time instant: an edge or node whose validity
	// window does not contain it is not read. It is always set: a current
	// read passes the reader's clock, so an ENDED edge (an ownership whose
	// valid_to is in the past) is never read as current.
	ValidAt time.Time
}

// EdgeEnd is one end node of a candidate edge, as stored.
type EdgeEnd struct {
	Subject    contextfabric.SubjectRef
	Attributes map[string]interface{}
}

// EdgeCandidate is one edge a page read, before the edge gate.
type EdgeCandidate struct {
	Key          EdgeKey
	RelationType string
	Attributes   map[string]interface{}
	From         EdgeEnd
	To           EdgeEnd
}

// EdgePage is one bounded read. More is true when the graph held an edge past
// the last one returned.
type EdgePage struct {
	Edges []EdgeCandidate
	More  bool
}

// EdgeGraph is the graph side of read_relationships. falkorgraph.Adapter
// implements it. Every read is scoped to the caller's own organization graph.
type EdgeGraph interface {
	ResolveInvestigationBinding(ctx context.Context, principal storage.Principal) (contextfabric.ResolvedGraphBinding, error)
	DirectEdgePage(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, query EdgePageQuery) (EdgePage, error)
}

// Bounds of one edge page.
const (
	// MaxEdgeFrontier is the most origins one page reads from, and so the
	// most hop-1 neighbours a depth-2 walk continues from.
	MaxEdgeFrontier = 100
	// MaxEdgePageLimit is the most edges one graph page reads (callers get
	// at most MaxRelationshipsLimit; the frontier scan uses the rest).
	MaxEdgePageLimit = 1000
)

// EdgeGate decides one edge. It is the whole edge rule of design E.4, and it
// is a pure function so each clause is testable on its own:
//
//  1. the edge's own authorization attributes pass the shared predicate;
//  2. the subject gate admitted the source node;
//  3. the subject gate admitted the target node.
//
// All three must hold. A node the subject gate did not admit (denied,
// absent, ownership unproven) withholds every edge that touches it, so an
// edge never shows a node the caller could not read by id.
func EdgeGate(principal storage.Principal, edgeAttributes map[string]interface{}, sourceAdmitted, targetAdmitted bool) EdgeWithheldReason {
	if !graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, edgeAttributes) {
		return EdgeWithheldAttributes
	}
	if !sourceAdmitted {
		return EdgeWithheldSource
	}
	if !targetAdmitted {
		return EdgeWithheldTarget
	}
	return EdgeVisible
}
