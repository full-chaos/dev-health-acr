package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// WHY A DECLARED KIND IS ABSENT FROM THE POOL, SAID BY ARM RATHER THAN LEFT
// TO BE GUESSED.
//
// CHAOS-5388, audit finding F1. A named-project row entered phase 4 on the rig
// with seventy candidates cut to ten, every one a ci_pipeline_run, zero
// projects, and the kind offer withheld `project` as not-in-pool. The
// withholding is the TERMINAL state; the loss is upstream, in pool
// construction. The records could not say which upstream rule lost it.
//
// That is not a wording problem, it is an equality. Measured on the tree
// before this file existed, two resolutions emitted BYTE-IDENTICAL Info:
//
//	the kind-scoped rescue arm was NOT WIRED, so the declared kind was never
//	queried at all;
//	the arm WAS wired, DID query, and matched zero rows.
//
// One is a deployment gap, the other a retrieval defect. They take different
// fixes and they read the same. This is the distinction, restored to the one
// line an operator already reads.
//
// THE SCOPE IS THE DECLARED SET, NOT EVERY HINT. The kinds reported here are
// the ones THIS QUESTION'S OWN FRAME OR RECEIPT declared -- the same set phase
// 4 reserves slots for (frameReservedKinds) -- and never
// request.ExpectedKinds. A caller must not be able to add rows to an operator's
// diagnostic line by asserting kinds, for the same reason it cannot buy itself
// a reserved slot by asserting them.
type declaredKindRescue struct {
	Kind string `json:"kind"`
	// State is one of the four constants below, always set.
	State string `json:"state"`
	// TermsQueried is how many terms this kind's scoped search was actually
	// run for. Zero with State not_run means the arm never ran; zero with any
	// other state is impossible and would be a defect in this file.
	TermsQueried int `json:"terms_queried"`
	// Matched is how many rows those queries returned, BEFORE merge, dedup,
	// authorization or ranking. It answers "did the graph have anything under
	// this kind for these terms", which is the question a retrieval fix needs
	// and the pool alone cannot answer.
	Matched int `json:"matched"`
	// Survived is how many candidates of this kind came back out of phase 4.
	// Matched > 0 with Survived == 0 is the third cause and takes a third
	// fix: the subject was retrieved and truncation dropped it.
	Survived int `json:"survived"`
}

const (
	// declaredKindRescueNotRun: the kind-scoped arm did not run for this
	// kind. Today that means deps.SearchKind is not wired -- a DEPLOYMENT
	// gap, not a retrieval one, and the two must never read alike.
	declaredKindRescueNotRun = "not_run"
	// declaredKindRescueMatchedZero: the arm ran and the graph returned
	// nothing under this kind for these terms. A MEASURED zero.
	declaredKindRescueMatchedZero = "ran_matched_zero"
	// declaredKindRescueMatchedThenCut: the arm ran, the graph returned rows,
	// and none survived phase 4.
	declaredKindRescueMatchedThenCut = "ran_matched_then_cut"
	// declaredKindRescueMatchedSurvived: the healthy path, stated explicitly
	// so a pass where nothing went wrong cannot be mistaken for a build that
	// stopped reporting.
	declaredKindRescueMatchedSurvived = "ran_matched_survived"
)

// kindRescueLedger records what the kind-scoped arm actually did, per kind.
// It is filled by applyKindHintedPoolSearch -- including on its early return,
// which is the whole point: an arm that never ran must still leave a record
// saying so, or its silence is indistinguishable from an arm that found
// nothing.
type kindRescueLedger struct {
	// declared is what THIS QUESTION'S frame or receipt declared, carried on
	// the ledger rather than read from the cut's reservedKinds parameter.
	//
	// r1 P1, executed: the retained confirmed-kind re-decision and the
	// evidence-census pass both call the exported entry point, which supplies
	// no reserved kinds -- deliberately, because the RESERVE is off for those
	// passes. Reading the declared set from that parameter therefore made
	// their later summary emit an EMPTY list, and since the last summary
	// reaching the tracer describes the pass whose resolution was returned, an
	// operator read `[]` and would conclude nothing was ever declared. That is
	// the exact "two states read alike" defect this file exists to remove,
	// reintroduced one pass later. The declared set is a fact about the
	// REQUEST, so it lives with the retrieval facts and survives every pass
	// that is handed the ledger.
	declared     []contextfabric.SubjectKind
	ran          bool
	termsQueried map[contextfabric.SubjectKind]int
	matched      map[contextfabric.SubjectKind]int
}

func newKindRescueLedger(declared []contextfabric.SubjectKind) *kindRescueLedger {
	return &kindRescueLedger{
		declared:     declared,
		termsQueried: map[contextfabric.SubjectKind]int{},
		matched:      map[contextfabric.SubjectKind]int{},
	}
}

// declaredKinds is nil-safe: a call with no ledger has no frame and therefore
// declared nothing, which renders an empty list rather than a missing key.
func (l *kindRescueLedger) declaredKinds() []contextfabric.SubjectKind {
	if l == nil {
		return nil
	}
	return l.declared
}

func (l *kindRescueLedger) recordQuery(kind contextfabric.SubjectKind, matched int) {
	if l == nil {
		return
	}
	l.ran = true
	l.termsQueried[kind]++
	l.matched[kind] += matched
}

// declaredKindRescueReport renders the line's value for the DECLARED kinds.
//
// It returns a non-nil, possibly EMPTY slice on every pass, so a frame that
// declared nothing renders `[]` and can never be confused with a build that
// stopped emitting the key. survivors is the per-kind count out of phase 4.
func declaredKindRescueReport(ledger *kindRescueLedger, survivors map[contextfabric.SubjectKind]int) []declaredKindRescue {
	declared := ledger.declaredKinds()
	out := make([]declaredKindRescue, 0, len(declared))
	seen := make(map[contextfabric.SubjectKind]bool, len(declared))
	for _, kind := range declared {
		if kind == "" || seen[kind] {
			continue
		}
		seen[kind] = true
		row := declaredKindRescue{Kind: string(kind), Survived: survivors[kind]}
		if ledger != nil {
			row.TermsQueried = ledger.termsQueried[kind]
			row.Matched = ledger.matched[kind]
		}
		switch {
		case ledger == nil || !ledger.ran || row.TermsQueried == 0:
			row.State = declaredKindRescueNotRun
		case row.Matched == 0:
			row.State = declaredKindRescueMatchedZero
		case row.Survived == 0:
			row.State = declaredKindRescueMatchedThenCut
		default:
			row.State = declaredKindRescueMatchedSurvived
		}
		out = append(out, row)
	}
	return out
}
