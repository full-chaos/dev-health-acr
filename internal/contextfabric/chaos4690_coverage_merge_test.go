package contextfabric

import (
	"log/slog"
	"strings"
	"sync"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4690 §3.4: MergeCoverage is the ONE pure normalizer both merge call
// sites (this package's own Synthesize and genkitruntime's
// synthesisInputFromDomain) route through. These tests pin the six golden
// fixture classes design §3.4/§8 names, the fail-open reconcile path, the
// ordering/dedup rules, and DetailID minting -- directly against the
// normalizer, so a defect anywhere in it is caught at the smallest
// reproducing unit rather than only through a full Investigate() round
// trip.

func detailFixture(code contractsv1.ContextFabricCoverageDetailCode, source string, degrading bool, raw string, mutate func(*CoverageDetail)) CoverageDetail {
	d := CoverageDetail{DetailID: "cov-provisional", Source: source, Code: code, Degrading: degrading, Raw: raw}
	if mutate != nil {
		mutate(&d)
	}
	d.Label = contractsv1.ComposeCoverageDetailLabel(d)
	return d
}

// --- Golden fixture class 1: fact-only ---

func TestChaos4690_MergeCoverage_GoldenFactOnly(t *testing.T) {
	t.Parallel()
	raw := "readiness: canonical fact capability timed out"
	facts := Coverage{
		Sources:         []SourceObservation{{Source: "canonical_fact:readiness", State: SourceUnavailable, Reason: "canonical fact capability timed out"}},
		Partial:         true,
		DegradedReasons: []string{raw},
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactReadFailed, "canonical_fact:readiness", true, raw, func(d *CoverageDetail) {
			d.FactKind = FactReadiness
			d.SourceState = SourceUnavailable
		})},
	}
	graph := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}

	merged := MergeCoverage("org_test", graph, facts)

	if got := merged.DegradedReasons; len(got) != 1 || got[0] != raw {
		t.Fatalf("DegradedReasons = %#v, want exactly [%q] (byte-identical to today's dedupe+sort computation)", got, raw)
	}
	if len(merged.Details) != 1 {
		t.Fatalf("Details = %#v, want exactly one paired detail", merged.Details)
	}
	got := merged.Details[0]
	if got.DetailID != "cov-01" {
		t.Fatalf("DetailID = %q, want the minted ordinal cov-01", got.DetailID)
	}
	if got.Raw != raw || !got.Degrading || got.Code != contractsv1.ContextFabricCoverageDetailFactReadFailed {
		t.Fatalf("merged detail = %#v, want the fact detail preserved verbatim (only DetailID re-minted)", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("merged detail fails contract Validate: %v", err)
	}
	if len(merged.Sources) != 1 || merged.Sources[0].Label == "" || merged.Sources[0].StateLabel == "" {
		t.Fatalf("Sources = %#v, want the merged source stamped with Label/StateLabel", merged.Sources)
	}
	wantLabel := contractsv1.ContextFabricSourceObservationLabel("canonical_fact:readiness")
	if merged.Sources[0].Label != wantLabel {
		t.Fatalf("Sources[0].Label = %q, want the registry label %q", merged.Sources[0].Label, wantLabel)
	}
}

// --- Golden fixture class 2: graph-only ---

func TestChaos4690_MergeCoverage_GoldenGraphOnly(t *testing.T) {
	t.Parallel()
	raw := "endpoint_lookup_failed:2"
	count := 2
	graph := Coverage{
		Sources:         []SourceObservation{},
		Partial:         true,
		DegradedReasons: []string{raw},
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed, "context-fabric:graph", true, raw, func(d *CoverageDetail) {
			d.Count = &count
		})},
	}
	facts := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}

	merged := MergeCoverage("org_test", graph, facts)

	if got := merged.DegradedReasons; len(got) != 1 || got[0] != raw {
		t.Fatalf("DegradedReasons = %#v, want exactly [%q]", got, raw)
	}
	if len(merged.Details) != 1 || merged.Details[0].DetailID != "cov-01" || merged.Details[0].Raw != raw {
		t.Fatalf("Details = %#v, want the single graph detail re-minted as cov-01", merged.Details)
	}
	if err := merged.Details[0].Validate(); err != nil {
		t.Fatalf("merged detail fails contract Validate: %v", err)
	}
}

// --- Golden fixture class 3: mixed graph+fact ---

func TestChaos4690_MergeCoverage_GoldenMixedGraphAndFact(t *testing.T) {
	t.Parallel()
	factRaw := "readiness: canonical fact capability timed out"
	graphRaw := "endpoint_lookup_failed:2"
	count := 2
	facts := Coverage{
		Sources:         []SourceObservation{{Source: "canonical_fact:readiness", State: SourceUnavailable, Reason: "canonical fact capability timed out"}},
		Partial:         true,
		DegradedReasons: []string{factRaw},
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactReadFailed, "canonical_fact:readiness", true, factRaw, func(d *CoverageDetail) {
			d.FactKind = FactReadiness
			d.SourceState = SourceUnavailable
		})},
	}
	graph := Coverage{
		Sources:         []SourceObservation{},
		Partial:         true,
		DegradedReasons: []string{graphRaw},
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed, "context-fabric:graph", true, graphRaw, func(d *CoverageDetail) {
			d.Count = &count
		})},
	}

	// Insertion order (facts, then graph) is DELIBERATELY the reverse of
	// sorted order (graphRaw < factRaw alphabetically) -- a normalizer that
	// forgot to sort would still concatenate-in-input-order and pass a
	// weaker assertion; this call order is what actually exercises the
	// sort.
	merged := MergeCoverage("org_test", facts, graph)

	wantReasons := []string{}
	wantReasons = append(wantReasons, factRaw, graphRaw)
	// today's computation is sort.Strings over the dedupe-map keys.
	if factRaw > graphRaw {
		wantReasons[0], wantReasons[1] = graphRaw, factRaw
	}
	if len(merged.DegradedReasons) != 2 || merged.DegradedReasons[0] != wantReasons[0] || merged.DegradedReasons[1] != wantReasons[1] {
		t.Fatalf("DegradedReasons = %#v, want %#v (byte-identical to today's sort.Strings order)", merged.DegradedReasons, wantReasons)
	}
	if len(merged.Details) != 2 {
		t.Fatalf("Details = %#v, want both groups' details concatenated", merged.Details)
	}
	// Details are sorted by Raw within the degrading half -- same order the
	// derivation compares against reasons above.
	if merged.Details[0].Raw != wantReasons[0] || merged.Details[1].Raw != wantReasons[1] {
		t.Fatalf("Details order = [%q, %q], want it sorted by Raw exactly like DegradedReasons: %#v", merged.Details[0].Raw, merged.Details[1].Raw, wantReasons)
	}
	if merged.Details[0].DetailID != "cov-01" || merged.Details[1].DetailID != "cov-02" {
		t.Fatalf("DetailIDs = [%q, %q], want ordinal cov-01/cov-02 over the final order", merged.Details[0].DetailID, merged.Details[1].DetailID)
	}
}

// --- Golden fixture class 4: narrowed-then-failed ---

func TestChaos4690_MergeCoverage_GoldenNarrowedThenFailed(t *testing.T) {
	t.Parallel()
	raw := "blockers: canonical fact capability returned unavailable (2 subject(s) not supported by this capability)"
	facts := Coverage{
		Sources:         []SourceObservation{{Source: "canonical_fact:blockers", State: SourceUnavailable, Reason: raw}},
		Partial:         true,
		DegradedReasons: []string{raw},
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactReadFailed, "canonical_fact:blockers", true, raw, func(d *CoverageDetail) {
			d.FactKind = FactBlockers
			d.SourceState = SourceUnavailable
			d.Narrowed = true
			d.SkippedKinds = []SubjectKind{SubjectTeam}
		})},
	}
	graph := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}

	merged := MergeCoverage("org_test", graph, facts)

	if len(merged.Details) != 1 {
		t.Fatalf("Details = %#v, want exactly one narrowed-then-failed detail", merged.Details)
	}
	got := merged.Details[0]
	if got.Code != contractsv1.ContextFabricCoverageDetailFactReadFailed || !got.Narrowed || len(got.SkippedKinds) != 1 {
		t.Fatalf("merged detail = %#v, want fact_read_failed carrying Narrowed+SkippedKinds riding along (precedence: read_failed beats narrowed)", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("merged detail fails contract Validate: %v", err)
	}
}

// --- Golden fixture class 5: SourceNoData-with-reason (non-degrading) ---

func TestChaos4690_MergeCoverage_GoldenSourceNoDataNonDegrading(t *testing.T) {
	t.Parallel()
	raw := "no rows observed"
	facts := Coverage{
		Sources:         []SourceObservation{{Source: "canonical_fact:status", State: SourceNoData, Reason: raw}},
		Partial:         false,
		DegradedReasons: []string{},
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactProviderReported, "canonical_fact:status", false, raw, func(d *CoverageDetail) {
			d.FactKind = FactStatus
			d.SourceState = SourceNoData
		})},
	}
	graph := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}

	merged := MergeCoverage("org_test", graph, facts)

	if len(merged.DegradedReasons) != 0 {
		t.Fatalf("DegradedReasons = %#v, want NONE: a SourceNoData provider reason never degrades (r1 F2a)", merged.DegradedReasons)
	}
	if len(merged.Details) != 1 || merged.Details[0].Degrading {
		t.Fatalf("Details = %#v, want exactly one NON-degrading detail preserved even though nothing degraded", merged.Details)
	}
	if merged.Details[0].DetailID != "cov-01" {
		t.Fatalf("DetailID = %q, want cov-01 even for the non-degrading-only case", merged.Details[0].DetailID)
	}
	if err := merged.Details[0].Validate(); err != nil {
		t.Fatalf("merged detail fails contract Validate: %v", err)
	}
}

// --- Golden fixture class 6: composite narrowed+scope-gap+success+truncated
// (the chaos4099_fact_scope_test.go:2365 live shape) ---

func TestChaos4690_MergeCoverage_GoldenCompositeScopeGapNarrowedTruncated(t *testing.T) {
	t.Parallel()
	raw := "metrics: fact scope unexpanded (origin: project; supported: repository; policy: project_work_item_repository_v1; basis: activity_proxy) (1 subject(s) narrowed: work_item)"
	facts := Coverage{
		Sources:         []SourceObservation{{Source: "canonical_fact:metrics", State: SourceTruncated, Reason: raw}},
		Partial:         true,
		DegradedReasons: []string{raw},
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactScopeUnexpanded, "canonical_fact:metrics", true, raw, func(d *CoverageDetail) {
			d.FactKind = FactMetrics
			d.SourceState = SourceTruncated
			d.ScopeOutcome = "expanded_partial"
			d.OriginKind = SubjectProject
			d.Policy = "project_work_item_repository_v1"
			d.Basis = "activity_proxy"
			d.Narrowed = true
			d.SkippedKinds = []SubjectKind{SubjectWorkItem}
		})},
	}
	graph := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}

	merged := MergeCoverage("org_test", graph, facts)

	if len(merged.Details) != 1 {
		t.Fatalf("Details = %#v, want exactly ONE detail for the composite observation (one detail per observation, always)", merged.Details)
	}
	got := merged.Details[0]
	if got.Code != contractsv1.ContextFabricCoverageDetailFactScopeUnexpanded {
		t.Fatalf("Code = %q, want fact_scope_unexpanded: it is the most specific cause in the fixed precedence order", got.Code)
	}
	if !got.Narrowed || len(got.SkippedKinds) != 1 || got.SourceState != SourceTruncated {
		t.Fatalf("merged detail = %#v, want Narrowed+SkippedKinds riding along a truncated, degrading scope-unexpanded detail", got)
	}
	if got.ScopeOutcome == "" || got.OriginKind == "" || got.Policy == "" || got.Basis == "" {
		t.Fatalf("merged detail = %#v, want all four scope fields present together", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("merged detail fails contract Validate: %v", err)
	}
	if len(merged.DegradedReasons) != 1 || merged.DegradedReasons[0] != raw {
		t.Fatalf("DegradedReasons = %#v, want exactly [%q]", merged.DegradedReasons, raw)
	}
}

// --- Fail-open reconcile (planted defect) ---

// captureDefaultLogger temporarily swaps slog's default logger for one
// backed by an in-memory buffer, so a test can assert a WARN line was (or
// was not) emitted without depending on process-wide log output. Restores
// the previous default on cleanup -- t.Parallel() tests must not use this
// helper (package-level slog state is not test-isolated), so every test
// that does is deliberately NOT marked parallel.
func captureDefaultLogger(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestChaos4690_MergeCoverage_FailOpenReconcile plants the exact defect
// design §3.4's RECONCILE step exists to catch: a group carries a
// DegradedReasons entry with NO paired detail (a legacy test double, or a
// future producer bug). The normalizer must fail OPEN -- Details: nil,
// degraded_reasons[] byte-identical and completely unaffected, a WARN
// logged with the org id and counts (never content).
func TestChaos4690_MergeCoverage_FailOpenReconcile(t *testing.T) {
	buf := captureDefaultLogger(t)
	raw := "readiness: canonical fact capability timed out"
	facts := Coverage{
		Sources:         []SourceObservation{{Source: "canonical_fact:readiness", State: SourceUnavailable, Reason: "canonical fact capability timed out"}},
		Partial:         true,
		DegradedReasons: []string{raw},
		Details:         nil, // the planted defect: no paired detail at all
	}
	graph := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}

	merged := MergeCoverage("org_reconcile_test", graph, facts)

	if merged.Details != nil {
		t.Fatalf("Details = %#v, want nil: an unpaired degraded reason must fail OPEN, never ship a mismatched structured surface", merged.Details)
	}
	if len(merged.DegradedReasons) != 1 || merged.DegradedReasons[0] != raw {
		t.Fatalf("DegradedReasons = %#v, want the legacy computation completely unaffected by the Details fail-open", merged.DegradedReasons)
	}
	if !merged.Partial {
		t.Fatal("Partial = false, want true: fail-open must not touch Partial/DegradedReasons semantics")
	}
	logged := buf.String()
	if !strings.Contains(logged, "org_reconcile_test") {
		t.Fatalf("log output = %q, want the org id present (content-safe: org id + counts only)", logged)
	}
	if strings.Contains(logged, raw) {
		t.Fatalf("log output = %q, want it to NEVER contain the reason string/content (engine.go:251's telemetry contract)", logged)
	}
}

// TestChaos4690_MergeCoverage_FailOpenDoesNotFireWhenReconciled is the
// mutation-proof companion: the SAME two-group input, but with the paired
// detail present, must NOT fail open and must NOT log.
func TestChaos4690_MergeCoverage_FailOpenDoesNotFireWhenReconciled(t *testing.T) {
	buf := captureDefaultLogger(t)
	raw := "readiness: canonical fact capability timed out"
	facts := Coverage{
		Sources:         []SourceObservation{{Source: "canonical_fact:readiness", State: SourceUnavailable, Reason: "canonical fact capability timed out"}},
		Partial:         true,
		DegradedReasons: []string{raw},
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactReadFailed, "canonical_fact:readiness", true, raw, func(d *CoverageDetail) {
			d.FactKind = FactReadiness
			d.SourceState = SourceUnavailable
		})},
	}
	graph := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}

	merged := MergeCoverage("org_reconcile_test", graph, facts)

	if merged.Details == nil || len(merged.Details) != 1 {
		t.Fatalf("Details = %#v, want the single paired detail preserved", merged.Details)
	}
	if logged := buf.String(); logged != "" {
		t.Fatalf("log output = %q, want NOTHING logged when the reconcile succeeds", logged)
	}
}

// --- Dedup rules ---

func TestChaos4690_MergeCoverage_DegradingDedupedByRawKeepingFirst(t *testing.T) {
	t.Parallel()
	raw := "endpoint_lookup_failed:2"
	countA, countB := 2, 2
	groupA := Coverage{
		DegradedReasons: []string{raw},
		Details:         []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed, "context-fabric:graph", true, raw, func(d *CoverageDetail) { d.Count = &countA })},
	}
	// A second group somehow produced the SAME composed raw string (e.g. a
	// retry path) -- the dedupe-by-Raw rule must collapse this to one
	// detail, mirroring degraded_reasons[]'s own set semantics.
	groupB := Coverage{
		DegradedReasons: []string{raw},
		Details:         []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed, "context-fabric:graph", true, raw, func(d *CoverageDetail) { d.Count = &countB })},
	}

	merged := MergeCoverage("org_test", groupA, groupB)

	if len(merged.DegradedReasons) != 1 {
		t.Fatalf("DegradedReasons = %#v, want the duplicate collapsed to one entry", merged.DegradedReasons)
	}
	if len(merged.Details) != 1 {
		t.Fatalf("Details = %#v, want the duplicate Raw collapsed to ONE detail (keep first)", merged.Details)
	}
}

func TestChaos4690_MergeCoverage_NonDegradingDedupedBySourceCodeRaw(t *testing.T) {
	t.Parallel()
	raw := "no rows observed"
	groupA := Coverage{
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactProviderReported, "canonical_fact:status", false, raw, func(d *CoverageDetail) {
			d.FactKind = FactStatus
			d.SourceState = SourceNoData
		})},
	}
	groupB := Coverage{
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactProviderReported, "canonical_fact:status", false, raw, func(d *CoverageDetail) {
			d.FactKind = FactStatus
			d.SourceState = SourceNoData
		})},
	}

	merged := MergeCoverage("org_test", groupA, groupB)

	if len(merged.Details) != 1 {
		t.Fatalf("Details = %#v, want the (Source, Code, Raw)-identical duplicate collapsed to one", merged.Details)
	}
}

func TestChaos4690_MergeCoverage_NonDegradingDifferentSourceNotDeduped(t *testing.T) {
	t.Parallel()
	raw := "no rows observed"
	groupA := Coverage{
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactProviderReported, "canonical_fact:status", false, raw, func(d *CoverageDetail) {
			d.FactKind = FactStatus
			d.SourceState = SourceNoData
		})},
	}
	groupB := Coverage{
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactProviderReported, "canonical_fact:workload", false, raw, func(d *CoverageDetail) {
			d.FactKind = FactWorkload
			d.SourceState = SourceNoData
		})},
	}

	merged := MergeCoverage("org_test", groupA, groupB)

	if len(merged.Details) != 2 {
		t.Fatalf("Details = %#v, want BOTH kept: same Raw but different Source, not a duplicate", merged.Details)
	}
}

// --- DetailID minting order (degrading first, then non-degrading) ---

func TestChaos4690_MergeCoverage_DetailIDsOrdinalDegradingThenNonDegrading(t *testing.T) {
	t.Parallel()
	degradedRaw := "endpoint_lookup_failed:1"
	nonDegradedRaw := "no rows observed"
	count := 1
	group := Coverage{
		DegradedReasons: []string{degradedRaw},
		Details: []CoverageDetail{
			detailFixture(contractsv1.ContextFabricCoverageDetailFactProviderReported, "canonical_fact:status", false, nonDegradedRaw, func(d *CoverageDetail) {
				d.FactKind = FactStatus
				d.SourceState = SourceNoData
			}),
			detailFixture(contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed, "context-fabric:graph", true, degradedRaw, func(d *CoverageDetail) { d.Count = &count }),
		},
	}
	empty := Coverage{}

	merged := MergeCoverage("org_test", group, empty)

	if len(merged.Details) != 2 {
		t.Fatalf("Details = %#v, want both details", merged.Details)
	}
	if !merged.Details[0].Degrading || merged.Details[0].DetailID != "cov-01" {
		t.Fatalf("Details[0] = %#v, want the degrading detail first as cov-01", merged.Details[0])
	}
	if merged.Details[1].Degrading || merged.Details[1].DetailID != "cov-02" {
		t.Fatalf("Details[1] = %#v, want the non-degrading detail second as cov-02", merged.Details[1])
	}
}

func TestChaos4690_MergeCoverage_ZeroDetailsYieldsNilNotEmptySlice(t *testing.T) {
	t.Parallel()
	merged := MergeCoverage("org_test", Coverage{}, Coverage{})
	if merged.Details != nil {
		t.Fatalf("Details = %#v, want nil when nothing was merged", merged.Details)
	}
}
