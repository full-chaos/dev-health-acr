package answerprojection

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestProjectionIsClosedOverCanonicalMaxima is the closure property behind
// the whole answer surface: Project(any valid canonical result) must yield a
// projection that validates.
//
// Codex round 2 found three breaches of it that shared no code, only this
// property -- a nil array where the schema requires one, and two projection
// bounds tighter than the canonical bounds they copy from. Each produced an
// internal error on the MCP path and a schema-violating body on the API
// path, for entirely ordinary inputs.
//
// This drives every copied field to its CANONICAL maximum at once. If any
// projection bound is ever tightened below its canonical source without a
// declared narrowing, this fails immediately rather than waiting for a real
// answer to hit the limit in production.
func TestProjectionIsClosedOverCanonicalMaxima(t *testing.T) {
	result := canonicalMaximumResult(t)
	if err := result.Validate(); err != nil {
		t.Fatalf("the canonical-maximum fixture is not a valid result, so it proves nothing: %v", err)
	}

	// Both the default budget and a deliberately generous one: the default
	// exercises the narrowing paths, the generous one exercises the
	// widest projection the contract can express.
	for name, budget := range map[string]Budget{
		"default": {},
		"maximum": {
			MaxDrivers:       contractsv1.ContextFabricProjectedDriversMaxCount,
			MaxCohortMembers: contractsv1.ContextFabricProjectedCohortMaxCount,
			MaxCandidates:    contractsv1.ContextFabricProjectedCandidatesMaxCount,
			MaxFacts:         contractsv1.ContextFabricProjectedFactsMaxCount,
			MaxEvidenceRefs:  contractsv1.ContextFabricProjectedEvidenceMaxCount,
		},
	} {
		t.Run(name, func(t *testing.T) {
			projection := Project(result, budget)
			if err := projection.Validate(); err != nil {
				t.Fatalf("a valid canonical result produced an INVALID projection: %v", err)
			}
			assertNoNullArrays(t, projection)
		})
	}
}

// TestProjectionIsClosedOverAMinimalResult covers the other extreme: an
// answer that cites nothing and carries no optional content. That is the
// shape that produced the nil evidence array (codex round-2 F1).
func TestProjectionIsClosedOverAMinimalResult(t *testing.T) {
	result := richResult()
	result.Cohort = nil
	result.Drivers = result.Drivers[1:2]
	result.Drivers[0].EvidenceRefIDs = []string{}
	result.Drivers[0].PathIDs = []string{"path_minimal_0001"}
	result.Drivers[0].ClaimedFactIDs = nil
	result.Drivers[0].Category = "narrative"
	result.Limitations = []string{}
	result.Warnings = []string{}
	result.StrongestPressures = []string{}
	result.Coverage.Sources = []contractsv1.ContextFabricSourceObservation{}
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{}
	result.SubjectResolution.Candidates = []contractsv1.ContextFabricSubjectCandidate{}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	if err := result.Validate(); err != nil {
		t.Fatalf("minimal fixture is not a valid canonical result: %v", err)
	}

	projection := Project(result, Budget{})
	if err := projection.Validate(); err != nil {
		t.Fatalf("a citation-free answer produced an invalid projection: %v", err)
	}
	assertNoNullArrays(t, projection)
}

// assertNoNullArrays proves every required array member serialized as an
// array. A nil Go slice marshals to null, which the schema rejects -- and
// an API that serves null for a required array is publishing an invalid
// document even when nothing errored server-side.
func assertNoNullArrays(t *testing.T, projection contractsv1.ContextFabricAnswerProjection) {
	t.Helper()
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("marshal projection: %v", err)
	}
	required := []string{
		"strongest_pressures", "committed_subjects", "principal_drivers",
		"key_facts", "coverage_summary", "limitations", "warnings",
		"evidence_ref_ids", "subject_receipts",
	}
	for _, member := range required {
		if strings.Contains(string(encoded), "\""+member+"\":null") {
			t.Errorf("required array %q serialized as null", member)
		}
	}
	for _, driver := range projection.PrincipalDrivers {
		if driver.EvidenceRefIDs == nil {
			t.Errorf("driver %q has a nil required evidence array", driver.DriverID)
		}
	}
}

// canonicalMaximumResult builds a valid canonical result that sits at the
// contract maximum for every field the projection copies.
func canonicalMaximumResult(t *testing.T) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	project := subject(contractsv1.ContextFabricSubjectProject, "project_ask_dev", "Ask Dev")
	result := richResult()

	result.Question = strings.Repeat("q", 8000)
	result.DirectJudgment = strings.Repeat("j", 4000)
	result.CurrentState = strings.Repeat("c", 4000)
	result.DeterministicAnswer = strings.Repeat("d", 12000)

	result.StrongestPressures = filled(50, func(i int) string { return pad("pressure", i, 2000) })
	result.Limitations = filled(100, func(i int) string { return pad("limitation", i, 2000) })
	result.Warnings = filled(100, func(i int) string { return pad("warning", i, 2000) })

	// 50 drivers, each at the maximum text bounds, each citing a claim.
	result.Drivers = nil
	result.ClaimedFacts = nil
	value := "amber"
	for i := 0; i < 50; i++ {
		claimID := "claim_max_" + strconv.Itoa(1000+i)
		result.ClaimedFacts = append(result.ClaimedFacts, contractsv1.ContextFabricClaimedFact{
			ClaimID: claimID, Kind: contractsv1.ContextFabricFactStatus, Subject: project,
			Field: strings.Repeat("f", 128), Value: contractsv1.ContextFabricScalarValue{String: &value},
		})
		result.Drivers = append(result.Drivers, contractsv1.ContextFabricDriverJudgment{
			DriverID: "driver_max_" + strconv.Itoa(1000+i),
			Standing: contractsv1.ContextFabricDriverPrincipal, Category: "status",
			Title: strings.Repeat("t", 512), Summary: strings.Repeat("s", 4000),
			Qualification:    strings.Repeat("u", 2000),
			AffectedSubjects: []contractsv1.ContextFabricSubjectRef{project},
			EvidenceRefIDs:   []string{"evidence_max_" + strconv.Itoa(1000+i)},
			ClaimedFactIDs:   []string{claimID},
			Derivation:       contractsv1.ContextFabricDerivationCanonicalStructured,
			EpistemicStatus:  contractsv1.ContextFabricEpistemicObserved,
			Confidence:       1, Current: true,
		})
	}

	// A cohort at the member maximum, each member at the inclusion-reason
	// maximum.
	members := make([]contractsv1.ContextFabricCohortMember, 0, 250)
	for i := 0; i < 250; i++ {
		members = append(members, contractsv1.ContextFabricCohortMember{
			Subject:          subject(contractsv1.ContextFabricSubjectTeam, "team_max_"+strconv.Itoa(i), "Team "+strconv.Itoa(i)),
			Rank:             i + 1,
			InclusionReasons: filled(32, func(j int) string { return pad("reason", i*100+j, 1000) }),
			EvidenceRefIDs:   []string{"evidence_cohort_" + strconv.Itoa(1000+i)},
		})
	}
	result.Cohort = &contractsv1.ContextFabricCohort{
		Kind: contractsv1.ContextFabricSubjectTeam, Members: members,
		Rationale: strings.Repeat("r", 4000), Complete: true,
	}

	// Coverage at the source maximum, each with a maximum-length reason.
	sources := make([]contractsv1.ContextFabricSourceObservation, 0, 100)
	for i := 0; i < 100; i++ {
		sources = append(sources, contractsv1.ContextFabricSourceObservation{
			Source: "source_" + strconv.Itoa(1000+i),
			State:  contractsv1.ContextFabricSourceUnavailable,
			Reason: strings.Repeat("e", 2000),
		})
	}
	result.Coverage = contractsv1.ContextFabricCoverage{
		Sources: sources, Partial: true,
		DegradedReasons: filled(100, func(i int) string { return pad("degraded", i, 2000) }),
	}

	// Clarification candidates at their maximum.
	candidates := make([]contractsv1.ContextFabricSubjectCandidate, 0, 50)
	for i := 0; i < 50; i++ {
		candidates = append(candidates, contractsv1.ContextFabricSubjectCandidate{
			ReceiptID: "receipt_max_" + strconv.Itoa(1000+i),
			Subject:   subject(contractsv1.ContextFabricSubjectProject, "project_max_"+strconv.Itoa(i), "Project "+strconv.Itoa(i)),
			State:     contractsv1.ContextFabricResolutionProposed,
			MatchReasons: filled(32, func(j int) string {
				return pad("match", i*100+j, 1000)
			}),
			Confidence:     1,
			EvidenceRefIDs: []string{},
		})
	}
	result.SubjectResolution = contractsv1.ContextFabricSubjectResolution{
		Candidates:          candidates,
		Committed:           []contractsv1.ContextFabricSubjectRef{project},
		ClarificationPrompt: strings.Repeat("p", 2000),
	}
	result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
	result.GeneratedAt = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	result.RemainingWork = []contractsv1.ContextFabricFinding{}
	result.ReadinessGaps = []contractsv1.ContextFabricFinding{}
	result.Conflicts = []contractsv1.ContextFabricFinding{}
	result.Paths = []contractsv1.ContextFabricRelationshipPath{}
	result.EvidenceRefIDs = []string{"evidence_max_1000"}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

func filled(count int, build func(int) string) []string {
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, build(i))
	}
	return out
}

// pad builds a unique string of exactly maxLength bytes.
func pad(prefix string, index, maxLength int) string {
	head := prefix + "-" + strconv.Itoa(index) + "-"
	if len(head) >= maxLength {
		return head[:maxLength]
	}
	return head + strings.Repeat("x", maxLength-len(head))
}

// TestProjectionIsClosedOverLegacyStoredResults is the codex round-4 F3
// regression.
//
// Reads of persisted data accept the HISTORICAL bounds, so a stored result
// can legitimately carry 50 inclusion reasons of 1024 characters, or
// narrative entries of 4000. Copying those through unchanged produced a
// projection that violated its own published schema -- from a stored row
// that was entirely valid. The API route emits the projection without
// revalidating, so that shipped as an invalid document rather than an
// error.
//
// The projection is a VIEW: it clamps, counts the drop, and leaves the
// canonical view to serve the untouched original.
func TestProjectionIsClosedOverLegacyStoredResults(t *testing.T) {
	result := legacyStoredResult(t)
	// The premise: this result is NOT valid under the current write
	// contract but IS valid as a stored row. If that stopped being true
	// the test would prove nothing.
	if err := result.Validate(); err == nil {
		t.Fatal("the legacy fixture passes the write validator, so it does not exercise the lenient-read path")
	}
	if err := result.ValidateStored(); err != nil {
		t.Fatalf("the legacy fixture is not readable as a stored row: %v", err)
	}

	for name, budget := range map[string]Budget{
		"default": {},
		"maximum": {
			MaxDrivers:       contractsv1.ContextFabricProjectedDriversMaxCount,
			MaxCohortMembers: contractsv1.ContextFabricProjectedCohortMaxCount,
			MaxCandidates:    contractsv1.ContextFabricProjectedCandidatesMaxCount,
			MaxFacts:         contractsv1.ContextFabricProjectedFactsMaxCount,
			MaxEvidenceRefs:  contractsv1.ContextFabricProjectedEvidenceMaxCount,
		},
	} {
		t.Run(name, func(t *testing.T) {
			projection := Project(result, budget)
			if err := projection.Validate(); err != nil {
				t.Fatalf("a legacy stored result produced an INVALID projection: %v", err)
			}
			assertNoNullArrays(t, projection)

			// Clamping is real, not accidental: the oversize values must
			// actually have been cut to the projection's bounds.
			if projection.Cohort != nil {
				for _, member := range projection.Cohort.Members {
					if len(member.InclusionReasons) > contractsv1.ContextFabricProjectedInclusionReasonsMaxCount {
						t.Errorf("cohort member kept %d inclusion reasons", len(member.InclusionReasons))
					}
					for _, reason := range member.InclusionReasons {
						if len([]rune(reason)) > contractsv1.ContextFabricProjectedInclusionReasonMaxLength {
							t.Errorf("cohort inclusion reason was not clamped: %d runes", len([]rune(reason)))
						}
					}
				}
			}
			for _, limitation := range projection.Limitations {
				if len([]rune(limitation)) > contractsv1.ContextFabricProjectedNarrativeMaxLength {
					t.Errorf("limitation was not clamped: %d runes", len([]rune(limitation)))
				}
			}
		})
	}
}

// legacyStoredResult builds a result at the HISTORICAL maxima: valid to
// read back, invalid to write today.
func legacyStoredResult(t *testing.T) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	result := richResult()
	result.Limitations = filled(120, func(i int) string { return pad("legacy-limitation", i, 4000) })
	result.Warnings = filled(120, func(i int) string { return pad("legacy-warning", i, 4000) })
	result.Cohort.Members[0].InclusionReasons = filled(50, func(i int) string { return pad("legacy-reason", i, 1024) })
	result.DirectJudgment = strings.Repeat("j", 8000)
	result.CurrentState = strings.Repeat("c", 8000)
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

// TestClampingIsDisclosed is the codex round-5 R5-3 regression. Shortening
// a value is a form of omission: a consumer reading a cut judgment with
// truncated=false has no way to know it is reading half an answer.
func TestClampingIsDisclosed(t *testing.T) {
	result := richResult()
	result.Drivers = result.Drivers[1:2] // no withheld driver, so nothing else truncates
	result.Cohort = nil
	result.DirectJudgment = strings.Repeat("j", 8000) // legal as a stored row, twice the projection bound

	projection := Project(result, Budget{})
	if len([]rune(projection.DirectJudgment)) != contractsv1.ContextFabricProjectedJudgmentMaxLength {
		t.Fatalf("judgment was not clamped: %d runes", len([]rune(projection.DirectJudgment)))
	}
	if projection.ProjectionBudget.ValuesClamped == 0 {
		t.Error("a clamped value was not counted")
	}
	if !projection.ProjectionBudget.Truncated {
		t.Error("clamping a value must set truncated; a silently shortened answer reads as a complete one")
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection invalid: %v", err)
	}
}

// TestSharedPrefixLegacyNarrativesDoNotCollide is the codex round-5 R5-4
// regression. Deduping BEFORE clamping let two distinct legacy entries
// sharing a long prefix survive as separate values and then collide once
// clamped, producing duplicate entries the projection's own validator
// rejects -- emitted unvalidated by the route.
func TestSharedPrefixLegacyNarrativesDoNotCollide(t *testing.T) {
	prefix := strings.Repeat("p", contractsv1.ContextFabricProjectedNarrativeMaxLength)
	result := richResult()
	result.Drivers = result.Drivers[1:2]
	result.Cohort = nil
	result.Limitations = []string{prefix + "-first-distinct-tail", prefix + "-second-distinct-tail"}

	// The premise: both are legal in a stored row and genuinely distinct.
	if err := result.ValidateStored(); err != nil {
		t.Fatalf("legacy fixture is not readable as a stored row: %v", err)
	}

	projection := Project(result, Budget{})
	if err := projection.Validate(); err != nil {
		t.Fatalf("shared-prefix legacy limitations produced an INVALID projection: %v", err)
	}
	if len(projection.Limitations) != 1 {
		t.Errorf("expected the collided entries to become one, got %d", len(projection.Limitations))
	}
	if projection.ProjectionBudget.LimitationsOmitted != 1 {
		t.Errorf("the collision was not counted as an omission: %d", projection.ProjectionBudget.LimitationsOmitted)
	}
}

// TestPaddedCoverageSourceNamesProject is the codex round-5 R5-5
// regression: the canonical validator trimmed only for LENGTH, so a padded
// source name was storable, while the projection required an exactly
// trimmed name and rejected it.
func TestPaddedCoverageSourceNamesProject(t *testing.T) {
	result := richResult()
	result.Coverage.Sources[0].Source = "  work_items  "

	if err := result.Validate(); err == nil {
		t.Error("the write path accepted an untrimmed source name")
	}
	if err := result.ValidateStored(); err != nil {
		t.Fatalf("a stored row with a padded source name is unreadable: %v", err)
	}
	projection := Project(result, Budget{})
	if err := projection.Validate(); err != nil {
		t.Fatalf("a padded stored source name produced an invalid projection: %v", err)
	}
	for _, entry := range projection.CoverageSummary {
		if strings.TrimSpace(entry.Source) != entry.Source {
			t.Errorf("projection did not normalize the source name: %q", entry.Source)
		}
	}
}

// TestLegacyCohortInclusionReasonsProjectAndAreCounted is the codex
// round-6 F2 regression.
//
// A stored cohort member can legitimately hold 50 inclusion reasons of 1024
// runes. Clamping them to 32 x 1000 without deduping afterwards let two
// distinct entries sharing a 1000-rune prefix become identical, which the
// projection validator rejects -- so a valid stored row produced an invalid
// projection. The count-based drop (50 to 32) was silent too.
func TestLegacyCohortInclusionReasonsProjectAndAreCounted(t *testing.T) {
	prefix := strings.Repeat("r", contractsv1.ContextFabricProjectedInclusionReasonMaxLength)
	result := richResult()
	result.Drivers = result.Drivers[1:2]
	// Two entries that are distinct at the legacy length and identical
	// once clamped, plus enough others to exceed the projection's count.
	reasons := []string{prefix + "-first-tail", prefix + "-second-tail"}
	for i := 0; i < 48; i++ {
		reasons = append(reasons, "distinct reason "+strconv.Itoa(i))
	}
	result.Cohort.Members[0].InclusionReasons = reasons

	if err := result.ValidateStored(); err != nil {
		t.Fatalf("legacy fixture is not readable as a stored row: %v", err)
	}

	projection := Project(result, Budget{})
	if err := projection.Validate(); err != nil {
		t.Fatalf("legacy inclusion reasons produced an INVALID projection: %v", err)
	}
	if projection.Cohort == nil || len(projection.Cohort.Members) == 0 {
		t.Fatal("cohort was dropped entirely")
	}
	kept := projection.Cohort.Members[0].InclusionReasons
	if len(kept) > contractsv1.ContextFabricProjectedInclusionReasonsMaxCount {
		t.Errorf("kept %d inclusion reasons", len(kept))
	}
	seen := map[string]bool{}
	for _, reason := range kept {
		if seen[reason] {
			t.Errorf("clamping produced a duplicate inclusion reason")
		}
		seen[reason] = true
	}
	// Reason drops belong in the REASONS counter, not the member counter:
	// no member was dropped, and claiming otherwise is a wrong statement on
	// the wire (codex round-7 F4).
	if projection.ProjectionBudget.ReasonsOmitted == 0 {
		t.Error("dropped inclusion reasons were not counted")
	}
	if projection.ProjectionBudget.CohortMembersOmitted != 0 {
		t.Errorf("reason drops were misattributed as %d dropped members", projection.ProjectionBudget.CohortMembersOmitted)
	}
	if !projection.ProjectionBudget.Truncated {
		t.Error("dropping inclusion reasons must set truncated")
	}
}

// TestCollapsedCoverageSourcesAreCounted is the codex round-6 F3
// regression. Canonical uniqueness is checked BEFORE trimming, so a stored
// row can hold both " work_items " and "work_items" with different states.
// Trimming then collapses them, dropping one source's state and reason.
func TestCollapsedCoverageSourcesAreCounted(t *testing.T) {
	result := richResult()
	result.Drivers = result.Drivers[1:2]
	result.Cohort = nil
	result.Coverage.Sources = []contractsv1.ContextFabricSourceObservation{
		{Source: "work_items", State: contractsv1.ContextFabricSourceAvailable},
		{Source: " work_items ", State: contractsv1.ContextFabricSourceUnavailable, Reason: "a state that would vanish"},
	}
	if err := result.ValidateStored(); err != nil {
		t.Fatalf("legacy padded coverage is not readable as a stored row: %v", err)
	}

	projection := Project(result, Budget{})
	if err := projection.Validate(); err != nil {
		t.Fatalf("collapsed coverage produced an invalid projection: %v", err)
	}
	if len(projection.CoverageSummary) != 1 {
		t.Fatalf("expected the padded duplicate to collapse, got %d entries", len(projection.CoverageSummary))
	}
	if projection.ProjectionBudget.CoverageOmitted == 0 {
		t.Error("the collapsed source's state was dropped without being counted")
	}
	if !projection.ProjectionBudget.Truncated {
		t.Error("collapsing a coverage entry must set truncated")
	}
}

// canonicalSourceStates reads the source-state vocabulary from the
// PUBLISHED canonical schema rather than restating it here.
//
// A second hand-written list in this file would be exactly the drift the
// test below exists to catch: it would keep passing over whatever set it
// happens to name, while a state added to the contract goes unexercised.
func canonicalSourceStates(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "context_fabric_common.v1.schema.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read canonical common schema: %v", err)
	}
	var document struct {
		Defs struct {
			SourceObservation struct {
				Properties struct {
					State struct {
						Enum []string `json:"enum"`
					} `json:"state"`
				} `json:"properties"`
			} `json:"SourceObservation"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode canonical common schema: %v", err)
	}
	states := document.Defs.SourceObservation.Properties.State.Enum
	if len(states) == 0 {
		t.Fatal("the canonical schema declares no source states, so this test proves nothing")
	}
	return states
}

// TestEveryCanonicalSourceStateSurvivesTheProjection is the behavioural
// half of the coverage-vocabulary pin (CHAOS-3783's `pruned`).
//
// TestAnswerProjectionVocabulariesMatchTheCanonicalOnes proves the two
// SCHEMAS declare the same set. That is a document-level fact and it caught
// the drift `pruned` introduced -- but a schema pair can agree while the
// code path that copies the value rejects or mangles it, so agreement alone
// is not evidence a consumer can actually receive the state.
//
// This drives each canonical state through a real result and a real
// Project call, and requires the projection to carry that exact state and
// still validate. The mechanism it claims is copying: replacing
// projectCoverage's `State: source.State` with any fixed state fails nine
// of the ten subtests. The two guards are deliberately independent -- one
// binds the two documents, this one binds the code to the documents.
func TestEveryCanonicalSourceStateSurvivesTheProjection(t *testing.T) {
	for _, state := range canonicalSourceStates(t) {
		t.Run(state, func(t *testing.T) {
			result := richResult()
			// Every state but available carries a mandatory reason; the
			// contract rejects a bare degraded observation.
			reason := ""
			if contractsv1.ContextFabricSourceState(state) != contractsv1.ContextFabricSourceAvailable {
				reason = "declared for the " + state + " state"
			}
			result.Coverage.Sources = []contractsv1.ContextFabricSourceObservation{
				{Source: "work_items", State: contractsv1.ContextFabricSourceState(state), Reason: reason},
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("a result carrying source state %q is not canonically valid: %v", state, err)
			}

			projection := Project(result, Budget{})
			if err := projection.Validate(); err != nil {
				t.Fatalf("source state %q produced an invalid projection: %v", state, err)
			}
			if len(projection.CoverageSummary) != 1 {
				t.Fatalf("expected the one coverage entry to survive, got %d", len(projection.CoverageSummary))
			}
			if got := string(projection.CoverageSummary[0].State); got != state {
				t.Errorf("projection reported state %q, want the canonical %q", got, state)
			}
		})
	}
}
