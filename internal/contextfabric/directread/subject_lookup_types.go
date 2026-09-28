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
	// Truncated: a name read hit its per-kind pool bound, so a match past the
	// bound is unreachable in this read.
	Truncated bool
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
	// FindSubjectsByExactName returns the nodes of the given kinds whose
	// label, alias or provider key equals query exactly (case-insensitive).
	FindSubjectsByExactName(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, query string, kinds []string) (LookupPage, error)
}

// MaxLookupPageSize bounds one graph page and one find_subjects page.
const MaxLookupPageSize = 200
