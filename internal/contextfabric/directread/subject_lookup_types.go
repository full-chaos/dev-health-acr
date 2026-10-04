package directread

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// LookupNode is one Subject node a lookup read returned, BEFORE the subject
// gate has decided it. CanonicalID is the stored id byte for byte. Match is
// empty for a list read and one of exact, alias or provider_key for a name
// read. Attributes are the node's stored properties, so the authorization
// attributes travel with the node. A LookupNode is a candidate, never proof
// of admission: only SubjectGate.Authorize admits.
type LookupNode struct {
	Kind        string
	CanonicalID string
	Label       string
	Match       string
	Attributes  map[string]interface{}
}

// LookupPage is one bounded read of the caller's own organization graph.
type LookupPage struct {
	Nodes []LookupNode
	// More: a list read stopped at its page size and the kind holds nodes
	// after the last one returned.
	More bool
	// Truncated: the read itself was cut, so a match past the cut is
	// unreachable in this read.
	Truncated bool
	// After is the last canonical id the store returned for this page, the
	// keyset position for the next name page (empty when the page was empty).
	After string
}

// SubjectGraph is the graph side of find_subjects. falkorgraph.Adapter
// implements it. Every method reads the CALLER's own organization graph and
// calls no embedding model or interpreter: a lookup is structural.
type SubjectGraph interface {
	ResolveInvestigationBinding(ctx context.Context, principal storage.Principal) (contextfabric.ResolvedGraphBinding, error)
	// ListSubjectsByKind returns up to pageSize nodes of kind, ordered by
	// canonical id, whose canonical id sorts after afterCanonicalID ("" for
	// the first page). pageSize is at most MaxLookupPageSize.
	ListSubjectsByKind(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, kind, afterCanonicalID string, pageSize int) (LookupPage, error)
	// FindSubjectsByExactName returns one keyset page (at most pageSize,
	// ordered by canonical id, after afterCanonicalID) of the nodes of the
	// given kind whose label, alias or provider key equals query exactly
	// (case-insensitive). The equality is evaluated by the store over every
	// node of the kind; More says matches follow the page.
	FindSubjectsByExactName(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, query, kind, afterCanonicalID string, pageSize int) (LookupPage, error)
}

// MaxLookupPageSize bounds one graph page and one find_subjects page.
const MaxLookupPageSize = 200

// SubjectNodeReader reads stored Subject nodes by identity, so a candidate
// id that did not come from a graph read (a find_subjects handle candidate
// from the census) is served with its stored label. falkorgraph.Adapter
// implements it. It reads the CALLER's own organization graph; it is not an
// authorization: every node still goes through the subject gate.
type SubjectNodeReader interface {
	ReadSubjectNodes(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]LookupNode, error)
}
