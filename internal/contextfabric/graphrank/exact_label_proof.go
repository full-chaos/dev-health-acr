package graphrank

import (
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// ErrIdentityLookupNotRunForTimeAxis is what an AliasLookup returns when it
// did not read the identity universe because the question is on a historical
// time axis. It is not a fault: the resolution continues without the keyed
// read, exactly as before, and records the state as not_run_time_axis.
var ErrIdentityLookupNotRunForTimeAxis = errors.New("graphrank: identity lookup not run for a historical time axis")

// ErrIdentityLookupGraphLag is what an AliasLookup returns, beside its
// claimants and complete=false, when the identity universe it read was
// complete and the only gap is a matched claimant missing from the graph. It
// is not a fault. Such a claimant has no node, so its authorization cannot be
// read for any caller: the exact-label gate does not count it, and every
// other gate still treats the read as incomplete.
var ErrIdentityLookupGraphLag = errors.New("graphrank: identity lookup matched a claimant missing from the graph")

// IdentityLookupState is what the keyed identity read (ResolveDeps.AliasLookup)
// established for this resolution.
type IdentityLookupState string

const (
	// IdentityLookupComplete: the read enumerated every label, alias and
	// provider-key claimant of the lookup-scoped kinds for the terms.
	IdentityLookupComplete IdentityLookupState = "complete"
	// IdentityLookupIncomplete: the read ran and cannot prove it saw every
	// claimant (a kind over the row budget, or a claimant missing from the
	// graph).
	IdentityLookupIncomplete IdentityLookupState = "incomplete"
	// IdentityLookupGraphLag: the identity universe was read complete, and a
	// claimant it matched is not in the graph yet (projection lag).
	IdentityLookupGraphLag IdentityLookupState = "graph_lag"
	// IdentityLookupNotRunTimeAxis: the read did not run because the question
	// is on a historical time axis.
	IdentityLookupNotRunTimeAxis IdentityLookupState = "not_run_time_axis"
	// IdentityLookupNotWired: the backend has no keyed identity read.
	IdentityLookupNotWired IdentityLookupState = "not_wired"
)

// identityLookupStateOf classifies one AliasLookup call. A non-nil error other
// than ErrIdentityLookupNotRunForTimeAxis is returned unchanged.
func identityLookupStateOf(complete bool, err error) (IdentityLookupState, error) {
	switch {
	case errors.Is(err, ErrIdentityLookupNotRunForTimeAxis):
		return IdentityLookupNotRunTimeAxis, nil
	case errors.Is(err, ErrIdentityLookupGraphLag):
		return IdentityLookupGraphLag, nil
	case err != nil:
		return "", err
	case complete:
		return IdentityLookupComplete, nil
	default:
		return IdentityLookupIncomplete, nil
	}
}

// legacyIdentityLookupState maps the exported resolution entry points' bool
// to a state: true is a complete read; false keeps the behaviour those entry
// points always had, which never distinguished a missing read from an
// incomplete one.
func legacyIdentityLookupState(aliasIdentityComplete bool) IdentityLookupState {
	if aliasIdentityComplete {
		return IdentityLookupComplete
	}
	return IdentityLookupNotWired
}

// exactLabelUnproven reports whether an exact label match of kind may not
// commit on the exact-label tier: the kind's whole label population is
// enumerated by the keyed identity read, that read ran, and it could not
// prove it saw every subject carrying the label. A same-label subject the
// caller can read may then be missing from the pool, so the term does not
// prove one subject. Kinds outside the read keep the exact-label commit: no
// proof exists for them. A read that did not run for the time axis keeps it
// too, and so does a read whose only gap is a claimant missing from the
// graph: that claimant cannot be served or authorized for anyone, so counting
// it would let a subject the caller may not read change the decision.
func exactLabelUnproven(kind contextfabric.SubjectKind, lookup IdentityLookupState) bool {
	return isAliasLookupScopedKind(kind) && lookup == IdentityLookupIncomplete
}

// exactLabelRefusalCommitGate names, on the ambiguous decision line, the one
// refusal exactLabelUnproven decided.
const exactLabelRefusalCommitGate = "exact_index_unproven"

func exactLabelRefusalGate(refused bool) string {
	if refused {
		return exactLabelRefusalCommitGate
	}
	return ""
}
