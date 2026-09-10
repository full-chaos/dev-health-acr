package contextfabric

// CHAOS-5405 D-d -- the served census, and the re-baseline shape it makes
// possible.
//
// WHY THIS IS A SEPARATE PIN FROM THE ACTIVATION. A coverage detail names a
// policy ONLY when a GAP was recorded (fact_registry.go:1329-1334); a
// SUCCESSFUL expansion records no gap and therefore names no policy anywhere
// on the served document. That is fine while every work-item pair is `none`,
// and becomes the whole problem the moment they are activated:
//
//   - D-d's own requirement -- "the SERVED DOCUMENT (not just the log) must
//     distinguish a measured zero from an unmeasured population" -- has no
//     field to live in; `admitted_count = 0` alone never proves emptiness.
//   - D-f gate 8's re-baseline cannot be honoured. Comparing two yardstick
//     arms requires proving both ran on the SAME activated policy set; with
//     no policy on a successful serve, an old-policy arm and a new-policy arm
//     are indistinguishable from their own artefacts, and the ruling forbids
//     comparing across that boundary. The census record is what carries the
//     discriminator.
//
// Written through reflection and a JSON round trip so it COMPILES at the
// parent. The round trip is not incidental: pginvestigation stores the whole
// result as one JSONB payload (store.go:138,265) and reads it back through
// json.Unmarshal (store.go:506), so a field that does not survive it is a
// field no re-baseline can ever read.
//
// RED-FIRST at 0945a53dfdad0e84ba8244c59e8e9d85a7d195f2.

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// chaos5405CensusFields is D-d's record shape: the six identity fields and
// the five measurement fields, by json tag.
func chaos5405CensusFields() []string {
	return []string{
		"requirement_kind", "origin_kind", "policy", "basis", "axis", "outcome",
		"target_limit", "population_measured", "authorized_population_count",
		"admitted_count", "truncated",
	}
}

func chaos5405CensusFieldType(t *testing.T) reflect.Type {
	t.Helper()
	resultType := reflect.TypeOf(contractsv1.ContextFabricInvestigationResult{})
	field, ok := resultType.FieldByName("FactScopeCensus")
	if !ok {
		t.Fatalf("ContextFabricInvestigationResult has no FactScopeCensus field -- D-d's census has nowhere to be served")
	}
	if got := field.Tag.Get("json"); got != "fact_scope_census,omitempty" {
		t.Fatalf("FactScopeCensus json tag = %q, want %q -- optional-first, absent on every result written before it existed", got, "fact_scope_census,omitempty")
	}
	if field.Type.Kind() != reflect.Slice {
		t.Fatalf("FactScopeCensus kind = %s, want a slice -- one record per attempted requirement/origin decision", field.Type.Kind())
	}
	return field.Type.Elem()
}

// TestChaos5405_TheCensusRecordCarriesEveryRuledField pins the record shape.
func TestChaos5405_TheCensusRecordCarriesEveryRuledField(t *testing.T) {
	t.Parallel()
	recordType := chaos5405CensusFieldType(t)
	tags := map[string]reflect.StructField{}
	for i := 0; i < recordType.NumField(); i++ {
		field := recordType.Field(i)
		name := field.Tag.Get("json")
		for j := 0; j < len(name); j++ {
			if name[j] == ',' {
				name = name[:j]
				break
			}
		}
		tags[name] = field
	}
	for _, want := range chaos5405CensusFields() {
		if _, ok := tags[want]; !ok {
			t.Fatalf("census record has no %q field; it carries %v", want, tags)
		}
	}
	if recordType.NumField() != len(chaos5405CensusFields()) {
		t.Fatalf("census record has %d fields, %d are ruled -- an unruled field on a served record is one nobody agreed to disclose", recordType.NumField(), len(chaos5405CensusFields()))
	}
}

// TestChaos5405_AMeasuredZeroIsNotAnUnmeasuredPopulation is D-d's core
// distinction, and the reason authorized_population_count must be NULLABLE
// rather than a plain int: a Go zero and "never counted" would otherwise
// serialize identically.
func TestChaos5405_AMeasuredZeroIsNotAnUnmeasuredPopulation(t *testing.T) {
	t.Parallel()
	recordType := chaos5405CensusFieldType(t)
	count, ok := recordType.FieldByName("AuthorizedPopulationCount")
	if !ok {
		t.Fatalf("census record has no AuthorizedPopulationCount field")
	}
	if count.Type.Kind() != reflect.Pointer {
		t.Fatalf("AuthorizedPopulationCount kind = %s, want a pointer -- a measured 0 and an unmeasured population must not serialize alike", count.Type.Kind())
	}
	measured, ok := recordType.FieldByName("PopulationMeasured")
	if !ok {
		t.Fatalf("census record has no PopulationMeasured field")
	}
	if measured.Type.Kind() != reflect.Bool {
		t.Fatalf("PopulationMeasured kind = %s, want bool", measured.Type.Kind())
	}
	if got := measured.Tag.Get("json"); got != "population_measured" {
		t.Fatalf("PopulationMeasured json tag = %q, want %q -- never omitempty: a false that vanishes is the ambiguity this field exists to remove", got, "population_measured")
	}
}

// TestChaos5405_TheCensusSurvivesThePersistedPayloadRoundTrip is the
// re-baseline shape itself: a census written today must be readable from the
// stored payload tomorrow, naming the policy each arm ran on, or the two arms
// of the D-f yardstick cannot be proven to share an activated policy set.
func TestChaos5405_TheCensusSurvivesThePersistedPayloadRoundTrip(t *testing.T) {
	t.Parallel()
	recordType := chaos5405CensusFieldType(t)
	record := reflect.New(recordType).Elem()
	setString := func(field, value string) {
		target := record.FieldByName(field)
		if !target.IsValid() {
			t.Fatalf("census record has no %s field", field)
		}
		target.SetString(value)
	}
	setString("Policy", "project_work_item_status_v1")
	setString("Basis", "direct")
	setString("Outcome", "expanded_partial")

	result := contractsv1.ContextFabricInvestigationResult{}
	census := reflect.ValueOf(&result).Elem().FieldByName("FactScopeCensus")
	census.Set(reflect.Append(census, record))

	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var restored contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	back := reflect.ValueOf(&restored).Elem().FieldByName("FactScopeCensus")
	if back.Len() != 1 {
		t.Fatalf("census records after round trip = %d, want 1 -- a census that does not survive the payload is one no re-baseline can read", back.Len())
	}
	if got := back.Index(0).FieldByName("Policy").String(); got != "project_work_item_status_v1" {
		t.Fatalf("policy after round trip = %q, want the activated policy -- this is the discriminator that keeps an old-policy arm from being compared with a new-policy one", got)
	}
}

// TestChaos5405_TheProjectionPreservesTheCensus pins D-d's "Preserve these
// records through answerprojection; consumers must not reconstruct them from
// prose."
func TestChaos5405_TheProjectionPreservesTheCensus(t *testing.T) {
	t.Parallel()
	recordType := chaos5405CensusFieldType(t)
	record := reflect.New(recordType).Elem()
	record.FieldByName("Policy").SetString("team_primary_attribution_work_item_membership_v1")

	result := scopeServableResult()
	census := reflect.ValueOf(&result).Elem().FieldByName("FactScopeCensus")
	census.Set(reflect.Append(census, record))

	projection := answerprojection.Project(result, answerprojection.Budget{})
	projected := reflect.ValueOf(&projection).Elem().FieldByName("FactScopeCensus")
	if !projected.IsValid() {
		t.Fatalf("ContextFabricAnswerProjection has no FactScopeCensus field -- the census stops at the stored result and never reaches a consumer")
	}
	if projected.Len() != 1 {
		t.Fatalf("projected census records = %d, want 1", projected.Len())
	}
}

// TestChaos5405_ControlAnswerPlanIsStillOptionalFirst is the NEGATIVE
// CONTROL: the same optional-first round-trip discipline on a field that
// ALREADY exists, so it passes at the parent and proves the three reds above
// are about the missing census, not about a broken harness.
func TestChaos5405_ControlAnswerPlanIsStillOptionalFirst(t *testing.T) {
	t.Parallel()
	resultType := reflect.TypeOf(contractsv1.ContextFabricInvestigationResult{})
	field, ok := resultType.FieldByName("AnswerPlan")
	if !ok {
		t.Fatalf("ContextFabricInvestigationResult has no AnswerPlan field")
	}
	if got := field.Tag.Get("json"); got != "answer_plan,omitempty" {
		t.Fatalf("AnswerPlan json tag = %q, want %q", got, "answer_plan,omitempty")
	}
	payload, err := json.Marshal(contractsv1.ContextFabricInvestigationResult{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := decoded["answer_plan"]; present {
		t.Fatalf("answer_plan present on a zero result -- omitempty is what makes a field optional-first")
	}
}
