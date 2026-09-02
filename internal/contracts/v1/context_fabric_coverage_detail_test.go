package v1

import (
	"strings"
	"testing"
)

// CHAOS-4690 substrate tests. Red-first: none of these types/validators
// exist on origin/main (0f68cc7a), so the whole file fails to build there;
// the planted-defect cases below are the per-clause guards (each observes
// its specific guard failing), and the mutation ledger in the lane handoff
// records the mutate-back runs.

// validDetailForCode returns a minimal VALID detail for each code — the
// fixture the planted-defect cases perturb one field at a time.
func validDetailForCode(code ContextFabricCoverageDetailCode) ContextFabricCoverageDetail {
	d := ContextFabricCoverageDetail{
		DetailID: "cov-01",
		Code:     code,
		Label:    "coverage was limited",
	}
	switch code {
	case ContextFabricCoverageDetailFactUnconfigured:
		d.Source, d.FactKind, d.SourceState = "canonical_fact:blockers", ContextFabricFactBlockers, ContextFabricSourceUnconfigured
	case ContextFabricCoverageDetailFactScopeUnexpanded:
		d.Source, d.FactKind, d.SourceState = "canonical_fact:blockers", ContextFabricFactBlockers, ContextFabricSourceUnconfigured
		d.ScopeOutcome, d.OriginKind, d.Policy, d.Basis = "policy_unavailable", ContextFabricSubjectTeam, "none", "activity_proxy"
	case ContextFabricCoverageDetailFactReadFailed:
		d.Source, d.FactKind, d.SourceState = "canonical_fact:metrics", ContextFabricFactMetrics, ContextFabricSourceUnavailable
	case ContextFabricCoverageDetailFactProviderReported:
		d.Source, d.FactKind, d.SourceState = "canonical_fact:health", ContextFabricFactHealth, ContextFabricSourceStale
	case ContextFabricCoverageDetailFactPruned:
		d.Source, d.FactKind, d.SourceState = "canonical_fact:reviews", ContextFabricFactReviews, ContextFabricSourcePruned
		d.SupportedKinds = []ContextFabricSubjectKind{ContextFabricSubjectRepository}
	case ContextFabricCoverageDetailFactNarrowed:
		d.Source, d.FactKind, d.SourceState = "canonical_fact:status", ContextFabricFactStatus, ContextFabricSourceAvailable
		d.Narrowed, d.SkippedKinds, d.Count = true, []ContextFabricSubjectKind{ContextFabricSubjectTeam}, intPtr(2)
	case ContextFabricCoverageDetailGraphEndpointLookupFailed,
		ContextFabricCoverageDetailGraphCohortDeniedByAuthorization,
		ContextFabricCoverageDetailGraphUnknownRelationshipType:
		d.Source, d.Count = "context-fabric:graph", intPtr(3)
	case ContextFabricCoverageDetailGraphExactNameCandidatesTruncated:
		d.Source = "context-fabric:graph"
	case ContextFabricCoverageDetailGraphValidityUnbounded:
		d.Source, d.Count = "context-fabric:graph-validity-windows", intPtr(1)
	case ContextFabricCoverageDetailReuseAuxiliaryRefsStripped:
		d.Source, d.Count, d.Degrading = "context-fabric:answer-reuse", intPtr(4), true
	}
	return d
}

// TestCoverageDetailEveryCodeHasValidFixtureRuleAndLabel is the totality
// walk: every member of the closed code vocabulary must (a) validate with
// its minimal fixture, (b) have a field rule, (c) compose a non-empty,
// in-bounds deterministic label.
func TestCoverageDetailEveryCodeHasValidFixtureRuleAndLabel(t *testing.T) {
	for _, code := range ContextFabricCoverageDetailCodeVocabulary() {
		detail := validDetailForCode(code)
		if err := detail.Validate(); err != nil {
			t.Errorf("%s: minimal fixture does not validate: %v", code, err)
		}
		if _, ok := coverageDetailFieldRules[code]; !ok {
			t.Errorf("%s: no field rule", code)
		}
		label := ComposeCoverageDetailLabel(detail)
		if strings.TrimSpace(label) == "" {
			t.Errorf("%s: composed label is empty", code)
		}
		if len([]rune(label)) > ContextFabricCoverageDetailLabelMaxLength {
			t.Errorf("%s: composed label exceeds the bound", code)
		}
	}
}

// TestCoverageDetailPlantedDefects observes each validation clause fail on
// the exact defect it exists to catch (dev-health AGENTS.md verification
// rule 2).
func TestCoverageDetailPlantedDefects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ContextFabricCoverageDetail)
		base    ContextFabricCoverageDetailCode
		message string
	}{
		{"unknown code", func(d *ContextFabricCoverageDetail) { d.Code = "made_up" }, ContextFabricCoverageDetailFactPruned, "closed vocabulary"},
		{"empty detail id", func(d *ContextFabricCoverageDetail) { d.DetailID = "" }, ContextFabricCoverageDetailFactPruned, "detail id"},
		{"empty label", func(d *ContextFabricCoverageDetail) { d.Label = " " }, ContextFabricCoverageDetailFactPruned, "label"},
		{"degrading pruned", func(d *ContextFabricCoverageDetail) { d.Degrading = true }, ContextFabricCoverageDetailFactPruned, "can never degrade"},
		{"degrading validity", func(d *ContextFabricCoverageDetail) { d.Degrading = true }, ContextFabricCoverageDetailGraphValidityUnbounded, "can never degrade"},
		{"scope fields on narrowed", func(d *ContextFabricCoverageDetail) {
			d.ScopeOutcome = "failed"
			d.OriginKind = ContextFabricSubjectTeam
			d.Policy = "none"
			d.Basis = "direct"
		}, ContextFabricCoverageDetailFactNarrowed, "forbids scope fields"},
		{"missing scope fields", func(d *ContextFabricCoverageDetail) { d.Basis = "" }, ContextFabricCoverageDetailFactScopeUnexpanded, "requires scope_outcome"},
		{"missing fact kind", func(d *ContextFabricCoverageDetail) { d.FactKind = "" }, ContextFabricCoverageDetailFactReadFailed, "requires fact_kind"},
		{"fact kind on graph code", func(d *ContextFabricCoverageDetail) { d.FactKind = ContextFabricFactHealth }, ContextFabricCoverageDetailGraphEndpointLookupFailed, "forbids fact_kind"},
		{"missing count", func(d *ContextFabricCoverageDetail) { d.Count = nil }, ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, "requires count"},
		{"negative count", func(d *ContextFabricCoverageDetail) { d.Count = intPtr(-1) }, ContextFabricCoverageDetailGraphUnknownRelationshipType, "non-negative"},
		{"narrowed without skipped", func(d *ContextFabricCoverageDetail) { d.SkippedKinds = nil }, ContextFabricCoverageDetailFactNarrowed, "requires narrowed"},
		{"skipped without narrowed flag", func(d *ContextFabricCoverageDetail) { d.Narrowed = false }, ContextFabricCoverageDetailFactNarrowed, "requires narrowed"},
		{"narrowing on unconfigured", func(d *ContextFabricCoverageDetail) {
			d.Narrowed = true
			d.SkippedKinds = []ContextFabricSubjectKind{ContextFabricSubjectTeam}
		}, ContextFabricCoverageDetailFactUnconfigured, "forbids narrowing"},
		{"bad scope outcome", func(d *ContextFabricCoverageDetail) { d.ScopeOutcome = "nope" }, ContextFabricCoverageDetailFactScopeUnexpanded, "scope outcome"},
		{"bad policy", func(d *ContextFabricCoverageDetail) { d.Policy = "nope" }, ContextFabricCoverageDetailFactScopeUnexpanded, "policy"},
		{"bad basis", func(d *ContextFabricCoverageDetail) { d.Basis = "nope" }, ContextFabricCoverageDetailFactScopeUnexpanded, "basis"},
		{"oversized phrasing", func(d *ContextFabricCoverageDetail) {
			d.Phrasing = strings.Repeat("x", ContextFabricCoverageDetailPhrasingMaxLength+1)
		}, ContextFabricCoverageDetailFactPruned, "phrasing"},
		{"untrimmed phrasing", func(d *ContextFabricCoverageDetail) { d.Phrasing = " padded " }, ContextFabricCoverageDetailFactPruned, "phrasing"},
		{"oversized raw", func(d *ContextFabricCoverageDetail) {
			d.Raw = strings.Repeat("x", ContextFabricCoverageDetailRawMaxLength+1)
		}, ContextFabricCoverageDetailFactPruned, "raw"},
	}
	for _, tc := range cases {
		detail := validDetailForCode(tc.base)
		tc.mutate(&detail)
		err := detail.Validate()
		if err == nil {
			t.Errorf("%s: planted defect was accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.message) {
			t.Errorf("%s: rejected for the wrong reason: %v (want mention of %q)", tc.name, err, tc.message)
		}
	}
}

// TestValidateCoverageDetailsPairing pins the dual-write derivation
// invariant: degrading details' Raw strings pair 1:1, in order, with
// degraded_reasons.
func TestValidateCoverageDetailsPairing(t *testing.T) {
	degrading := validDetailForCode(ContextFabricCoverageDetailFactReadFailed)
	degrading.Degrading = true
	degrading.Raw = "metrics: read failed"
	nonDegrading := validDetailForCode(ContextFabricCoverageDetailFactPruned)
	nonDegrading.DetailID = "cov-02"
	nonDegrading.Raw = "pruned:subject_kind_unsupported: ..."

	ok := []ContextFabricCoverageDetail{degrading, nonDegrading}
	if err := validateCoverageDetails(ok, []string{"metrics: read failed"}, 100); err != nil {
		t.Fatalf("valid pairing rejected: %v", err)
	}
	if err := validateCoverageDetails(ok, nil, 100); err == nil {
		t.Fatal("degrading detail with no degraded_reasons entry was accepted")
	}
	if err := validateCoverageDetails(ok, []string{"a different string"}, 100); err == nil {
		t.Fatal("raw/reason mismatch was accepted")
	}
	if err := validateCoverageDetails([]ContextFabricCoverageDetail{nonDegrading}, []string{"metrics: read failed"}, 100); err == nil {
		t.Fatal("degraded_reasons entry with no paired degrading detail was accepted")
	}
	dup := degrading
	if err := validateCoverageDetails([]ContextFabricCoverageDetail{degrading, dup}, []string{"metrics: read failed", "metrics: read failed"}, 100); err == nil {
		t.Fatal("duplicate detail ids were accepted")
	}
	stripped := degrading
	stripped.Raw = ""
	if err := validateCoverageDetails([]ContextFabricCoverageDetail{stripped}, []string{}, 100); err == nil {
		t.Fatal("degrading detail without raw was accepted")
	}
	if err := validateCoverageDetails(ok, []string{"metrics: read failed"}, 1); err == nil {
		t.Fatal("details past the entry bound were accepted")
	}
}

// TestEvidenceRefLabelsExactClosure pins sol r1 F4's fix: a non-nil label
// map must key EXACTLY on the result's own evidence-ref closure — an
// unlabeled ref, or a label naming no ref, is unrepresentable on a fresh
// write.
func TestEvidenceRefLabelsExactClosure(t *testing.T) {
	result := ContextFabricInvestigationResult{
		EvidenceRefIDs: []string{"acr:v1:team:gh:ops-team"},
		Drivers: []ContextFabricDriverJudgment{{
			EvidenceRefIDs: []string{"acr:v1:pull-request:42"},
		}},
	}
	full := map[string]string{
		"acr:v1:team:gh:ops-team": "Team: gh:ops-team",
		"acr:v1:pull-request:42":  "Pull request: 42",
	}
	result.EvidenceRefLabels = full
	if err := validateEvidenceRefLabels(result); err != nil {
		t.Fatalf("exact closure rejected: %v", err)
	}
	result.EvidenceRefLabels = map[string]string{"acr:v1:team:gh:ops-team": "Team: gh:ops-team"}
	if err := validateEvidenceRefLabels(result); err == nil {
		t.Fatal("missing label (driver ref unlabeled) was accepted")
	}
	result.EvidenceRefLabels = map[string]string{
		"acr:v1:team:gh:ops-team": "Team: gh:ops-team",
		"acr:v1:pull-request:42":  "Pull request: 42",
		"acr:v1:commit:deadbeef":  "Commit: deadbeef",
	}
	if err := validateEvidenceRefLabels(result); err == nil {
		t.Fatal("label naming no evidence ref was accepted")
	}
	result.EvidenceRefLabels = map[string]string{
		"acr:v1:team:gh:ops-team": "",
		"acr:v1:pull-request:42":  "Pull request: 42",
	}
	if err := validateEvidenceRefLabels(result); err == nil {
		t.Fatal("empty label was accepted")
	}
	result.EvidenceRefLabels = map[string]string{
		"acr:v1:team:gh:ops-team": strings.Repeat("x", ContextFabricCoverageDetailLabelMaxLength+1),
		"acr:v1:pull-request:42":  "Pull request: 42",
	}
	if err := validateEvidenceRefLabels(result); err == nil {
		t.Fatal("oversized label was accepted")
	}
}

// TestEvidenceRefClosureWalksEverySite pins the closure walk over every
// ref-bearing site the synthesis grounding closure admits — a site dropped
// from the walker would let a fresh write ship that site's chips unlabeled
// while still validating.
func TestEvidenceRefClosureWalksEverySite(t *testing.T) {
	cohort := &ContextFabricCohort{Members: []ContextFabricCohortMember{{EvidenceRefIDs: []string{"m"}}}}
	result := ContextFabricInvestigationResult{
		EvidenceRefIDs: []string{"r"},
		Drivers:        []ContextFabricDriverJudgment{{EvidenceRefIDs: []string{"d"}}},
		RemainingWork:  []ContextFabricFinding{{EvidenceRefIDs: []string{"rw"}}},
		ReadinessGaps:  []ContextFabricFinding{{EvidenceRefIDs: []string{"rg"}}},
		Conflicts:      []ContextFabricFinding{{EvidenceRefIDs: []string{"c"}}},
		Paths: []ContextFabricRelationshipPath{{
			EvidenceRefIDs: []string{"p"},
			Edges:          []ContextFabricRelationshipEdge{{EvidenceRefIDs: []string{"e"}}},
		}},
		Cohort: cohort,
	}
	result.SubjectResolution.Candidates = []ContextFabricSubjectCandidate{{EvidenceRefIDs: []string{"sc"}}}
	closure := ContextFabricEvidenceRefClosure(result)
	for _, ref := range []string{"r", "d", "rw", "rg", "c", "p", "e", "m", "sc"} {
		if _, ok := closure[ref]; !ok {
			t.Errorf("closure misses the %q site", ref)
		}
	}
	if len(closure) != 9 {
		t.Errorf("closure has %d entries, want 9", len(closure))
	}
}

// TestCoverageValidateAppliesDetailChecksOnWriteOnly pins the
// write-strict / read-lenient split: the same mismatched details reject a
// fresh write and pass a stored read (immutability).
func TestCoverageValidateAppliesDetailChecksOnWriteOnly(t *testing.T) {
	detail := validDetailForCode(ContextFabricCoverageDetailFactReadFailed)
	detail.Degrading = true
	detail.Raw = "metrics: read failed"
	coverage := ContextFabricCoverage{
		Sources: []ContextFabricSourceObservation{{Source: "canonical_fact:metrics", State: ContextFabricSourceUnavailable, Reason: "read failed"}},
		Partial: true,
		// Deliberately NOT paired: no degraded_reasons entry.
		Details: []ContextFabricCoverageDetail{detail},
	}
	if err := coverage.Validate(); err == nil {
		t.Fatal("write path accepted unpaired details")
	}
	if err := coverage.validateStored(); err != nil {
		t.Fatalf("stored read rejected an immutable legacy row: %v", err)
	}
}
