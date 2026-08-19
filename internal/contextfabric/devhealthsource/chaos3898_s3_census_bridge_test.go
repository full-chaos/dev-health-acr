package devhealthsource_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// censusNaturalKey mirrors the census registry's own identityColumn
// concatenation ("<org>:<segment>:<segment>...") in pure Go, exactly the
// shape RunCensus's SatisfierNaturalKey is populated from at the SQL
// layer (chaos3899_census_registry.go's per-kind identityColumn
// expressions) -- this test file's own fixture builder, not a second
// production implementation.
func censusNaturalKey(orgID string, segments ...string) string {
	parts := append([]string{orgID}, segments...)
	return strings.Join(parts, ":")
}

// TestBridgeSatisfierToCanonicalID_MatchesIdentityDerive is the injectivity
// pin CHAOS-3898 S3 owes CHAOS-3896 (design brief v4.1 §6 S3 row): for
// every changed census kind, bridging a census SatisfierNaturalKey must
// equal identity.Derive'ing the SAME segments directly -- proving the
// SPLIT recovers exactly the tuple the concatenation was built from, so
// the bridge inherits identity.Derive's own already-pinned injectivity
// (identity/registry_test.go, identity/codec_test.go) rather than
// re-deriving a parallel, possibly-diverging id.
func TestBridgeSatisfierToCanonicalID_MatchesIdentityDerive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		kind     graphrank.CensusKind
		orgID    string
		segments []string
		wantKind string
	}{
		{"ci_pipeline_run plain", contractsv1.ContextFabricSubjectCIRun, "org-1", []string{"repo-1", "run-42"}, identity.KindCIPipelineRun},
		{"work_item plain", contextfabric.SubjectWorkItem, "org-1", []string{"repo-1", "ITEM-9"}, identity.KindWorkItem},
		// The load-bearing case: a work_item_id shaped like a ticket-key
		// alias ("linear:CHAOS-3896", embed_fields.go's own ticketKeyAlias
		// precedent) carries an embedded ':' in its OWN raw value -- the
		// split must treat it as one segment, not fragment it at that
		// colon.
		{"work_item embedded colon", contextfabric.SubjectWorkItem, "org-1", []string{"repo-1", "linear:CHAOS-3896"}, identity.KindWorkItem},
		{"pull_request_review plain", contractsv1.ContextFabricSubjectPullRequestReview, "org-1", []string{"repo-1", "532", "review-7"}, identity.KindPullRequestReview},
		{"pull_request_review embedded colon", contractsv1.ContextFabricSubjectPullRequestReview, "org-1", []string{"repo-1", "532", "gh:review:7"}, identity.KindPullRequestReview},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			natKey := censusNaturalKey(tc.orgID, tc.segments...)
			gotID, gotOmitted, err := devhealthsource.BridgeSatisfierToCanonicalID(tc.kind, natKey)
			if err != nil {
				t.Fatalf("BridgeSatisfierToCanonicalID(%q, %q) error = %v", tc.kind, natKey, err)
			}
			wantID, wantOmitted, err := identity.Derive(tc.wantKind, tc.segments, nil)
			if err != nil {
				t.Fatalf("identity.Derive(%q, %v) error = %v", tc.wantKind, tc.segments, err)
			}
			if gotID != wantID || gotOmitted != wantOmitted {
				t.Fatalf("BridgeSatisfierToCanonicalID(%q, %q) = (%q, %v), want (%q, %v)", tc.kind, natKey, gotID, gotOmitted, wantID, wantOmitted)
			}
		})
	}
}

// TestBridgeSatisfierToCanonicalID_PullRequestGrandfathered pins the
// non-.v2 grandfathered scheme (design brief §1.2): no identity.Derive
// call, no codec, plain "pull_request:<repo_id>:<number>" -- and never
// omits, since it never runs the byte-length guard.
func TestBridgeSatisfierToCanonicalID_PullRequestGrandfathered(t *testing.T) {
	t.Parallel()
	natKey := censusNaturalKey("org-1", "repo-1", "532")
	gotID, gotOmitted, err := devhealthsource.BridgeSatisfierToCanonicalID(contextfabric.SubjectPullRequest, natKey)
	if err != nil {
		t.Fatalf("BridgeSatisfierToCanonicalID error = %v", err)
	}
	if gotOmitted {
		t.Fatalf("BridgeSatisfierToCanonicalID(pull_request) omitted = true, want false (grandfathered scheme never omits)")
	}
	if want := "pull_request:repo-1:532"; gotID != want {
		t.Fatalf("BridgeSatisfierToCanonicalID(pull_request) = %q, want %q", gotID, want)
	}
}

// TestBridgeSatisfierToCanonicalID_Injective spot-checks that DISTINCT
// satisfier tuples never collide on a bridged canonical id -- the
// property the whole hand-off exists to guarantee (3896 brief v6 §1.4:
// "for every census kind, (source natural key) <-> (graph canonical id)
// is injective").
func TestBridgeSatisfierToCanonicalID_Injective(t *testing.T) {
	t.Parallel()
	tuples := [][2]string{
		{"repo-1", "ITEM-1"},
		{"repo-1", "ITEM-2"},
		{"repo-2", "ITEM-1"},
		{"repo-1", "linear:CHAOS-1"},
		{"repo-1", "linear:CHAOS-2"},
	}
	seen := map[string][2]string{}
	for _, tuple := range tuples {
		id, omitted, err := devhealthsource.BridgeSatisfierToCanonicalID(contextfabric.SubjectWorkItem, censusNaturalKey("org-1", tuple[0], tuple[1]))
		if err != nil || omitted {
			t.Fatalf("BridgeSatisfierToCanonicalID(%v) = (%q, %v, %v)", tuple, id, omitted, err)
		}
		if prior, ok := seen[id]; ok {
			t.Fatalf("collision: %v and %v both bridge to %q", prior, tuple, id)
		}
		seen[id] = tuple
	}
}

// TestBridgeSatisfierToCanonicalID_MalformedKeyFailsClosed pins the
// fail-closed discipline every other registry parser in this package
// already follows (identity.Segments' own doc comment: "callers must
// treat that as cannot parse, not attempt a partial recovery") -- a
// satisfier key with too few segments must error, never silently bridge
// a truncated/wrong id.
func TestBridgeSatisfierToCanonicalID_MalformedKeyFailsClosed(t *testing.T) {
	t.Parallel()
	_, _, err := devhealthsource.BridgeSatisfierToCanonicalID(contextfabric.SubjectWorkItem, "org-1:repo-1")
	if err == nil {
		t.Fatal("BridgeSatisfierToCanonicalID with a too-short natural key: want error, got nil")
	}
}

// TestBridgeSatisfierToCanonicalID_UnregisteredKind pins the "no exemptions"
// discipline: a kind BuildCensusDiscriminator/RunCensus also refuse.
func TestBridgeSatisfierToCanonicalID_UnregisteredKind(t *testing.T) {
	t.Parallel()
	_, _, err := devhealthsource.BridgeSatisfierToCanonicalID(contextfabric.SubjectRepository, "org-1:repo-1")
	if err == nil {
		t.Fatal("BridgeSatisfierToCanonicalID(SubjectRepository): want error (not a registered census kind), got nil")
	}
}

// anchorCollisionFakeClient is a minimal ClickHouseQueryClient double for
// AnchorCollision's single-statement "SELECT count() FROM projects..."
// check -- deliberately separate from censusFakeClient (chaos3899_census_test.go),
// which targets the two-statement aggregate-first protocol's own SQL shape.
type anchorCollisionFakeClient struct {
	count      uint64
	err        error
	calls      int
	statements []string
}

func (c *anchorCollisionFakeClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.calls++
	c.statements = append(c.statements, statement)
	if c.err != nil {
		return nil, c.err
	}
	return &anchorCollisionFakeScanner{count: c.count}, nil
}

type anchorCollisionFakeScanner struct {
	count  uint64
	served bool
}

func (s *anchorCollisionFakeScanner) Next() bool {
	if s.served {
		return false
	}
	s.served = true
	return true
}
func (s *anchorCollisionFakeScanner) Scan(dest ...any) error {
	*dest[0].(*uint64) = s.count
	return nil
}
func (s *anchorCollisionFakeScanner) Err() error   { return nil }
func (s *anchorCollisionFakeScanner) Close() error { return nil }

// TestAnchorCollision_OnlyProjectKindIsChecked pins that no query is ever
// issued for a non-project anchor kind -- design brief v4.1 §1.4's defect
// is project-specific (projects.id alone lacks a provider); every other
// anchor kind (SubjectRepository) has no such column collapse.
func TestAnchorCollision_OnlyProjectKindIsChecked(t *testing.T) {
	t.Parallel()
	client := &anchorCollisionFakeClient{count: 5}
	collision, err := devhealthsource.AnchorCollision(context.Background(), client, "org-1", contextfabric.SubjectRepository, "repo:repo-1")
	if err != nil {
		t.Fatalf("AnchorCollision error = %v", err)
	}
	if collision {
		t.Fatal("AnchorCollision(SubjectRepository) = true, want false")
	}
	if client.calls != 0 {
		t.Fatalf("AnchorCollision(SubjectRepository) issued %d queries, want 0 -- a non-project anchor must never be queried", client.calls)
	}
}

// TestAnchorCollision_DetectsAmbiguousProviderID pins the collision case
// itself: key_resolution_count > 1 (two providers sharing the same raw
// projects.id, the SAME shape queryWorkItemProjects' own omission guard
// checks at projection time) must report collision=true.
func TestAnchorCollision_DetectsAmbiguousProviderID(t *testing.T) {
	t.Parallel()
	client := &anchorCollisionFakeClient{count: 2}
	collision, err := devhealthsource.AnchorCollision(context.Background(), client, "org-1", contextfabric.SubjectProject, "project.v2:github:p-1")
	if err != nil {
		t.Fatalf("AnchorCollision error = %v", err)
	}
	if !collision {
		t.Fatal("AnchorCollision with key_resolution_count=2: want collision=true")
	}
}

// TestAnchorCollision_UniqueProviderIDNoCollision is the ordinary,
// live-verified-zero-collisions case (queryWorkItemProjects' own doc
// comment: "zero such collisions across every organization checked").
func TestAnchorCollision_UniqueProviderIDNoCollision(t *testing.T) {
	t.Parallel()
	client := &anchorCollisionFakeClient{count: 1}
	collision, err := devhealthsource.AnchorCollision(context.Background(), client, "org-1", contextfabric.SubjectProject, "project.v2:github:p-1")
	if err != nil {
		t.Fatalf("AnchorCollision error = %v", err)
	}
	if collision {
		t.Fatal("AnchorCollision with key_resolution_count=1: want collision=false")
	}
}

// TestAnchorCollision_QueryErrorPropagates pins that a backend error fails
// closed (propagated, never swallowed into collision=false).
func TestAnchorCollision_QueryErrorPropagates(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	client := &anchorCollisionFakeClient{err: wantErr}
	_, err := devhealthsource.AnchorCollision(context.Background(), client, "org-1", contextfabric.SubjectProject, "project.v2:github:p-1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("AnchorCollision error = %v, want %v", err, wantErr)
	}
}

// TestAnchorCollision_NilClientFailsClosed pins that a nil client errors
// rather than panicking or silently reporting no collision.
func TestAnchorCollision_NilClientFailsClosed(t *testing.T) {
	t.Parallel()
	_, err := devhealthsource.AnchorCollision(context.Background(), nil, "org-1", contextfabric.SubjectProject, "project.v2:github:p-1")
	if err == nil {
		t.Fatal("AnchorCollision(nil client): want error, got nil")
	}
}

// TestAnchorCollision_PinsEmptyResultForAggregationSetting is an
// adversarial-review finding (confidence 82, fixed): AnchorCollision's
// aggregate must carry the SAME empty_result_for_aggregation_by_empty_set=0
// pin RunCensus's own aggregate statement carries
// (chaos3899_census_test.go's own TestRunCensus statement-shape pin) -- on
// a profile where this defaults to 1, an anchor naming a project id that
// no longer exists (a deleted project, a stale bound anchor -- the
// ordinary "genuinely zero rows match" case, not an error) would otherwise
// turn into a spurious "statement returned no row" error instead of the
// correct collision=false.
func TestAnchorCollision_PinsEmptyResultForAggregationSetting(t *testing.T) {
	t.Parallel()
	client := &anchorCollisionFakeClient{count: 1}
	if _, err := devhealthsource.AnchorCollision(context.Background(), client, "org-1", contextfabric.SubjectProject, "project.v2:github:p-1"); err != nil {
		t.Fatalf("AnchorCollision error = %v", err)
	}
	if len(client.statements) != 1 {
		t.Fatalf("AnchorCollision issued %d statements, want 1", len(client.statements))
	}
	if !strings.Contains(client.statements[0], "SETTINGS empty_result_for_aggregation_by_empty_set = 0") {
		t.Fatalf("AnchorCollision statement missing the empty_result_for_aggregation_by_empty_set=0 setting pin: %s", client.statements[0])
	}
}

// TestCensusKindRegistryEntries_EveryKindHasABridge is the completeness
// pin an adversarial review flagged as missing (mirrors
// TestKindHasAnchorFKMatchesCensusRegistry's own "registries must never
// silently drift" discipline, chaos3899_census_registry_test.go): every
// registered census kind must have a non-nil bridge, so a future kind
// added to the registry without one fails this test rather than only
// failing at BridgeSatisfierToCanonicalID call time in production.
func TestCensusKindRegistryEntries_EveryKindHasABridge(t *testing.T) {
	t.Parallel()
	for _, kind := range []graphrank.CensusKind{
		contextfabric.SubjectPullRequest,
		contextfabric.SubjectWorkItem,
		contractsv1.ContextFabricSubjectCIRun,
		contractsv1.ContextFabricSubjectPullRequestReview,
	} {
		if !graphrank.IsCensusKindRegistered(kind) {
			t.Fatalf("test's own kind list is stale: %s is not IsCensusKindRegistered", kind)
		}
		// Three tail segments satisfies every kind's own wantSegments (the
		// most demanding, pull_request_review, wants 3) without a
		// malformed-key parse error masking the nil-bridge check this test
		// targets -- BridgeSatisfierToCanonicalID checks bridgeCanonicalID
		// for nil BEFORE ever calling it, so a nil bridge always surfaces
		// "no registered census bridge" regardless of key shape; this
		// three-segment key just keeps the non-nil-bridge path noise-free.
		if _, _, err := devhealthsource.BridgeSatisfierToCanonicalID(kind, censusNaturalKey("org-1", "repo-1", "1", "extra")); err != nil && strings.Contains(err.Error(), "no registered census bridge") {
			t.Fatalf("kind=%s has no registered census bridge", kind)
		}
	}
}

// TestReasonAnchorCollision_IsClosedVocabularyValue pins the graphrank-side
// half of this hand-off: the DegradationReason constant this package's
// AnchorCollision exists to justify actually carries the exact wire value
// design brief v4.1 §1.4 names ("anchor_collision"), not a typo'd
// near-miss a future caller would silently branch past.
func TestReasonAnchorCollision_IsClosedVocabularyValue(t *testing.T) {
	t.Parallel()
	if graphrank.ReasonAnchorCollision != "anchor_collision" {
		t.Fatalf("graphrank.ReasonAnchorCollision = %q, want %q", graphrank.ReasonAnchorCollision, "anchor_collision")
	}
}
