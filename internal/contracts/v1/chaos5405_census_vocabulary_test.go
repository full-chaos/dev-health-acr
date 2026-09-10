package v1

import (
	"slices"
	"strings"
	"testing"
)

// chaos5405ValidCensusRecord is a census row every field of which is a real
// member of its own vocabulary, used as the BASE every arm below mutates one
// field of. Built once here so an arm can never fail for a reason other than
// the field it changed.
func chaos5405ValidCensusRecord() ContextFabricFactScopeCensusRecord {
	population := 3
	return ContextFabricFactScopeCensusRecord{
		RequirementKind:           string(ContextFabricFactStatus),
		OriginKind:                string(ContextFabricSubjectProject),
		Policy:                    "project_work_item_status_v1",
		Basis:                     "direct",
		Axis:                      string(ContextFabricTemporalCurrent),
		Outcome:                   "expanded",
		TargetLimit:               200,
		PopulationMeasured:        true,
		AuthorizedPopulationCount: &population,
		AdmittedCount:             3,
		Truncated:                 false,
	}
}

// chaos5405CensusBase returns the irreducible VALID result the bound table
// builds, so anything Validate rejects in these tests is rejected because of
// the census row and not because the surrounding document was already bad.
func chaos5405CensusBase(t *testing.T) ContextFabricInvestigationResult {
	t.Helper()
	base := buildFromTable(t, func(b answerBound) func(*ContextFabricInvestigationResult) { return b.Min })
	if err := base.Validate(); err != nil {
		t.Fatalf("the base fixture is not valid, so these assertions would prove nothing: %v", err)
	}
	return base
}

// TestChaos5405_EveryCensusTokenIsClosedAtTheWire pins codex r2 F3.
//
// The census validator checked only the LENGTH of its six string fields, so
// any value at all was served as long as it was short enough -- although the
// body, the contract and this package's own sibling record
// (ContextFabricCoverageDetail, which already validates its policy and
// outcome against these very vocabularies) all describe them as closed.
//
// Executed before the fix:
//
//	Validate accepted a census row whose six closed tokens are all unknown
//
// Both directions per field. Rejecting the unknown value alone is satisfied by
// a validator that rejects everything; accepting the real member alone is
// satisfied by the defect.
func TestChaos5405_EveryCensusTokenIsClosedAtTheWire(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		field   string
		unknown string
		set     func(*ContextFabricFactScopeCensusRecord, string)
	}{
		{"requirement_kind", "not_a_fact_kind", func(r *ContextFabricFactScopeCensusRecord, v string) { r.RequirementKind = v }},
		{"origin_kind", "not_a_subject_kind", func(r *ContextFabricFactScopeCensusRecord, v string) { r.OriginKind = v }},
		{"policy", "not_a_policy", func(r *ContextFabricFactScopeCensusRecord, v string) { r.Policy = v }},
		{"basis", "not_a_basis", func(r *ContextFabricFactScopeCensusRecord, v string) { r.Basis = v }},
		{"axis", "not_an_axis", func(r *ContextFabricFactScopeCensusRecord, v string) { r.Axis = v }},
		{"outcome", "not_an_outcome", func(r *ContextFabricFactScopeCensusRecord, v string) { r.Outcome = v }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			base := chaos5405CensusBase(t)

			// THE CONTROL FIRST: the untouched record is accepted, so the
			// rejection below is about this field and nothing else.
			valid := base
			valid.FactScopeCensus = []ContextFabricFactScopeCensusRecord{chaos5405ValidCensusRecord()}
			if err := valid.Validate(); err != nil {
				t.Fatalf("a census row of real vocabulary members was rejected: %v", err)
			}

			record := chaos5405ValidCensusRecord()
			tc.set(&record, tc.unknown)
			mutated := base
			mutated.FactScopeCensus = []ContextFabricFactScopeCensusRecord{record}
			err := mutated.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s = %q, which is not a member of its closed vocabulary -- the service can serve a document that violates its own contract", tc.field, tc.unknown)
			}
			// The message must NAME the field, or an operator reading a 500
			// cannot tell which of six tokens was wrong.
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("the rejection does not name the offending field: %v", err)
			}
		})
	}

	// EMPTY STAYS LEGAL, for the reason the validator's own comment gives:
	// basis and axis are genuinely empty on a rung that refused before either
	// was resolved, and rejecting them would reject exactly the rows D-e
	// added so a refusal is legible rather than silent.
	t.Run("an empty token is not an unknown token", func(t *testing.T) {
		t.Parallel()
		base := chaos5405CensusBase(t)
		record := chaos5405ValidCensusRecord()
		record.Basis = ""
		record.Axis = ""
		refused := base
		refused.FactScopeCensus = []ContextFabricFactScopeCensusRecord{record}
		if err := refused.Validate(); err != nil {
			t.Fatalf("a refused-early census row with an empty basis and axis was rejected: %v", err)
		}
	})

	// EVERY MEMBER of each vocabulary must validate, not just the one the
	// base fixture happens to use. A validator wired to the wrong vocabulary
	// would still pass the arms above.
	t.Run("every declared member of every vocabulary validates", func(t *testing.T) {
		t.Parallel()
		base := chaos5405CensusBase(t)
		assign := func(field string, value string) ContextFabricFactScopeCensusRecord {
			record := chaos5405ValidCensusRecord()
			switch field {
			case "requirement_kind":
				record.RequirementKind = value
			case "origin_kind":
				record.OriginKind = value
			case "policy":
				record.Policy = value
			case "basis":
				record.Basis = value
			case "axis":
				record.Axis = value
			case "outcome":
				record.Outcome = value
			default:
				t.Fatalf("unknown field %q", field)
			}
			return record
		}
		vocabularies := map[string][]string{
			"requirement_kind": factKindStrings(),
			"origin_kind":      subjectKindStrings(),
			"policy":           contextFabricFactScopePolicies[:],
			"basis":            contextFabricFactScopeBases[:],
			"axis":             temporalAxisStrings(),
			"outcome":          contextFabricFactScopeOutcomes[:],
		}
		for field, members := range vocabularies {
			if len(members) == 0 {
				t.Fatalf("%s has an EMPTY vocabulary -- this loop would assert nothing", field)
			}
			for _, member := range members {
				candidate := base
				candidate.FactScopeCensus = []ContextFabricFactScopeCensusRecord{assign(field, member)}
				if err := candidate.Validate(); err != nil {
					t.Errorf("%s = %q is a declared member and was rejected: %v", field, member, err)
				}
			}
		}
	})
}

// TestChaos5405_TheCensusCannotContradictItselfAboutMeasurement pins the
// second half of codex r2 F3, which the reviewer found and which is the
// sharper defect of the two.
//
// PopulationMeasured and AuthorizedPopulationCount exist to carry ONE
// distinction: a measured zero versus an unmeasured population. A document
// that says `population_measured: false` beside a count, or `true` beside
// null, asserts and denies that distinction in the same record -- and both
// were accepted.
func TestChaos5405_TheCensusCannotContradictItselfAboutMeasurement(t *testing.T) {
	t.Parallel()

	base := chaos5405CensusBase(t)
	zero := 0

	t.Run("measured=false with a count is refused", func(t *testing.T) {
		t.Parallel()
		record := chaos5405ValidCensusRecord()
		record.PopulationMeasured = false
		record.AuthorizedPopulationCount = &zero
		candidate := base
		candidate.FactScopeCensus = []ContextFabricFactScopeCensusRecord{record}
		if err := candidate.Validate(); err == nil {
			t.Fatal("Validate accepted population_measured=false beside authorized_population_count=0 -- an unmeasured population that reports a measurement")
		}
	})

	t.Run("measured=true with null is refused", func(t *testing.T) {
		t.Parallel()
		record := chaos5405ValidCensusRecord()
		record.PopulationMeasured = true
		record.AuthorizedPopulationCount = nil
		candidate := base
		candidate.FactScopeCensus = []ContextFabricFactScopeCensusRecord{record}
		if err := candidate.Validate(); err == nil {
			t.Fatal("Validate accepted population_measured=true beside a null count -- a completed census that reports no measurement")
		}
	})

	// BOTH LEGAL SHAPES, so the rule above is an agreement rule and not a
	// blanket ban on either value.
	t.Run("the two agreeing shapes are accepted", func(t *testing.T) {
		t.Parallel()
		measured := chaos5405ValidCensusRecord()
		measured.PopulationMeasured = true
		measured.AuthorizedPopulationCount = &zero
		unmeasured := chaos5405ValidCensusRecord()
		unmeasured.PopulationMeasured = false
		unmeasured.AuthorizedPopulationCount = nil
		for name, record := range map[string]ContextFabricFactScopeCensusRecord{
			"a measured zero":          measured,
			"an unmeasured population": unmeasured,
		} {
			candidate := base
			candidate.FactScopeCensus = []ContextFabricFactScopeCensusRecord{record}
			if err := candidate.Validate(); err != nil {
				t.Errorf("%s was rejected: %v", name, err)
			}
		}
	})
}

// TestChaos5405_ThePublishedAxisEnumIsTheGoVocabulary binds the new closed
// axis vocabulary to the schemas that publish it.
//
// contextFabricTemporalAxes is a MIRROR of what TimeContext.axis enumerates,
// and a mirror nobody checks is a second authority. Both directions: a member
// added to Go without the schema, or to the schema without Go, fails here.
func TestChaos5405_ThePublishedAxisEnumIsTheGoVocabulary(t *testing.T) {
	t.Parallel()

	documents := schemaDocuments(t)
	node := schemaNodeAt(t, documents, "common#$defs.TimeContext.properties.axis")
	raw, ok := node["enum"].([]any)
	if !ok {
		t.Fatal("common#$defs.TimeContext.properties.axis declares no enum")
	}
	published := make([]ContextFabricTemporalAxis, 0, len(raw))
	for _, value := range raw {
		text, isString := value.(string)
		if !isString {
			t.Fatalf("the axis enum holds a non-string member %v", value)
		}
		published = append(published, ContextFabricTemporalAxis(text))
	}
	vocabulary := ContextFabricTemporalAxisVocabulary()
	if !slices.Equal(published, vocabulary[:]) {
		t.Errorf("the published axis enum and the Go vocabulary disagree:\n  schema: %v\n  go:     %v", published, vocabulary)
	}
	for _, axis := range published {
		if !ValidContextFabricTemporalAxis(axis) {
			t.Errorf("the schema publishes axis %q, which the Go vocabulary rejects", axis)
		}
	}
	if ValidContextFabricTemporalAxis("") {
		t.Error("the empty string is a member of the axis vocabulary -- callers for which unset is legal must say so themselves")
	}
}

// TestChaos5405_TheProjectionBoundaryRunsTheSameCensusRule pins codex r3 P2.
//
// ContextFabricAnswerProjection.Validate never looked at the census it now
// carries, so the boundary the API and MCP paths actually call accepted a
// document the canonical validator refuses. Reproduced before the fix:
//
//	projection.Validate on a census with unknown tokens, negative bounds and a
//	contradictory measurement -> err=<nil>
//
// EVERY rule is exercised through the projection, one at a time, rather than
// asserting that one bad census is refused: a call wired to a partial copy of
// the rule would satisfy a single-case test and leave the rest open. The
// control below proves the boundary still accepts a well-formed census, so
// none of this is satisfied by a validator that refuses everything.
func TestChaos5405_TheProjectionBoundaryRunsTheSameCensusRule(t *testing.T) {
	t.Parallel()

	base := validAnswerProjection()
	if err := base.Validate(); err != nil {
		t.Fatalf("the base projection is not valid, so these assertions would prove nothing: %v", err)
	}
	negative := -1
	zero := 0

	t.Run("control: a well-formed census is accepted", func(t *testing.T) {
		t.Parallel()
		good := base
		good.FactScopeCensus = []ContextFabricFactScopeCensusRecord{chaos5405ValidCensusRecord()}
		if err := good.Validate(); err != nil {
			t.Fatalf("the projection boundary rejected a valid census: %v", err)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*ContextFabricFactScopeCensusRecord)
	}{
		{"unknown requirement_kind", func(r *ContextFabricFactScopeCensusRecord) { r.RequirementKind = "not_a_fact_kind" }},
		{"unknown origin_kind", func(r *ContextFabricFactScopeCensusRecord) { r.OriginKind = "not_a_subject_kind" }},
		{"unknown policy", func(r *ContextFabricFactScopeCensusRecord) { r.Policy = "not_a_policy" }},
		{"unknown basis", func(r *ContextFabricFactScopeCensusRecord) { r.Basis = "not_a_basis" }},
		{"unknown axis", func(r *ContextFabricFactScopeCensusRecord) { r.Axis = "not_an_axis" }},
		{"unknown outcome", func(r *ContextFabricFactScopeCensusRecord) { r.Outcome = "not_an_outcome" }},
		{"negative target_limit", func(r *ContextFabricFactScopeCensusRecord) { r.TargetLimit = -1 }},
		{"negative admitted_count", func(r *ContextFabricFactScopeCensusRecord) { r.AdmittedCount = -1 }},
		{"negative population count", func(r *ContextFabricFactScopeCensusRecord) { r.AuthorizedPopulationCount = &negative }},
		{"measured=false with a count", func(r *ContextFabricFactScopeCensusRecord) {
			r.PopulationMeasured = false
			r.AuthorizedPopulationCount = &zero
		}},
		{"measured=true with null", func(r *ContextFabricFactScopeCensusRecord) {
			r.PopulationMeasured = true
			r.AuthorizedPopulationCount = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := chaos5405ValidCensusRecord()
			tc.mutate(&record)
			candidate := base
			candidate.FactScopeCensus = []ContextFabricFactScopeCensusRecord{record}
			if err := candidate.Validate(); err == nil {
				t.Fatalf("the projection boundary accepted %s -- the API and MCP paths call THIS validator, so a document the canonical one refuses reached a consumer", tc.name)
			}
		})
	}

	// The COUNT bound too, which is a property of the array rather than of a
	// record and would be missed by a helper wired to iterate only.
	t.Run("the array bound is enforced here as well", func(t *testing.T) {
		t.Parallel()
		oversized := base
		records := make([]ContextFabricFactScopeCensusRecord, 0, ContextFabricFactScopeCensusMaxCount+1)
		for i := 0; i <= ContextFabricFactScopeCensusMaxCount; i++ {
			records = append(records, chaos5405ValidCensusRecord())
		}
		oversized.FactScopeCensus = records
		if err := oversized.Validate(); err == nil {
			t.Fatalf("the projection boundary accepted %d census rows, past the %d bound", len(records), ContextFabricFactScopeCensusMaxCount)
		}
	})
}
