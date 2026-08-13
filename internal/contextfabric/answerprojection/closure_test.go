package answerprojection

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

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
	result.Limitations = filled(250, func(i int) string { return pad("limitation", i, 2000) })
	result.Warnings = filled(250, func(i int) string { return pad("warning", i, 2000) })

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
	sources := make([]contractsv1.ContextFabricSourceObservation, 0, 250)
	for i := 0; i < 250; i++ {
		sources = append(sources, contractsv1.ContextFabricSourceObservation{
			Source: "source_" + strconv.Itoa(1000+i),
			State:  contractsv1.ContextFabricSourceUnavailable,
			Reason: strings.Repeat("e", 2000),
		})
	}
	result.Coverage = contractsv1.ContextFabricCoverage{
		Sources: sources, Partial: true,
		DegradedReasons: filled(250, func(i int) string { return pad("degraded", i, 2000) }),
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
