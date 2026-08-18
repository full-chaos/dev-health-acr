package devhealthsource

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// NewCensusFunc adapts BuildCensusDiscriminator+RunCensus to
// graphrank.CensusFunc -- the shape ResolveDeps.CensusFunc (CHAOS-3899,
// shadow-only) expects. A composition root that wants to run the shadow
// round for real (a measurement harness, or eventually a gated production
// wiring) passes NewCensusFunc(client) over the SAME ClickHouseQueryClient
// this package's ordinary producers already use.
//
// SCOPE NOTE: this ticket does not itself wire NewCensusFunc into any
// composition root (production's open.go, or the existing CHAOS-3884
// replay harness) -- see the PR description for why that wiring is
// deliberately left as a follow-up rather than bundled here.
func NewCensusFunc(client contextpacket.ClickHouseQueryClient) graphrank.CensusFunc {
	return func(ctx context.Context, orgID string, kind graphrank.CensusKind, handleValue string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorCanonicalID string, anchorBound bool) (graphrank.CensusOutcome, error) {
		predicate, err := BuildCensusDiscriminator(kind, handleValue, handleBound, anchorKind, anchorCanonicalID, anchorBound)
		if err != nil {
			return graphrank.CensusOutcome{}, err
		}
		result, err := RunCensus(ctx, client, orgID, kind, predicate)
		if err != nil {
			return graphrank.CensusOutcome{}, err
		}
		return graphrank.CensusOutcome{
			Count: result.Count, CensusReadAt: result.CensusReadAt, SatisfierNaturalKey: result.SatisfierNaturalKey,
			ClosureMismatch: result.ClosureMismatch, StatementCount: result.StatementCount, RowsRead: result.RowsRead,
		}, nil
	}
}

// VerifyExactlyOneSourceNaturalKey is CHAOS-3899's Slice-A SETUP INVARIANT
// (design brief v5 §6 "Setup oracle", sol v3 #6): a harness-facing check
// that ONE scored expected referent maps to EXACTLY ONE source natural key
// under its expected discriminator -- a miss here fails SETUP, never
// scoring (brief's own "the zero-false-no_match argument's premise becomes
// a setup invariant"). Deliberately NOT the shadow round's would-commit/
// no_match/would-clarify machinery: this asks one narrower question only
// -- "is this referent actually present, uniquely, in the source of record
// the census reads" -- so a harness can call it once per scored case in
// its own setup phase, independent of any resolution/pool/anchor-lookup
// machinery.
//
// SCOPE NOTE: this function is intentionally NOT wired into the existing
// live ambiguity/replay harness by this ticket (that harness needs live
// credentials this environment cannot exercise) -- it is delivered
// production-ready and unit-tested so that wiring is a small, mechanical
// follow-up: call it once per scored corpus case, at setup, and fail the
// run if ok is ever false.
func VerifyExactlyOneSourceNaturalKey(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, kind graphrank.CensusKind, handleValue string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorCanonicalID string, anchorBound bool) (ok bool, count int, readAt time.Time, err error) {
	predicate, err := BuildCensusDiscriminator(kind, handleValue, handleBound, anchorKind, anchorCanonicalID, anchorBound)
	if err != nil {
		return false, 0, time.Time{}, err
	}
	result, err := RunCensus(ctx, client, orgID, kind, predicate)
	if err != nil {
		return false, 0, time.Time{}, err
	}
	return result.Count == 1 && !result.ClosureMismatch, result.Count, result.CensusReadAt, nil
}
