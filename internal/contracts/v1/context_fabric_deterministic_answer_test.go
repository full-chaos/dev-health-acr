package v1

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

// THE INPUT DOMAIN OF THE TERMINAL-FORM PREDICATE, enumerated and executed.
//
// Every field the predicate reads (the completeness block's claimed-fact
// count, the evidence refs) and every field its consequence depends on
// (Status, the completeness block's presence, DeterministicAnswer itself,
// the refusal basis, the clarification prompt) is crossed with the cells
// that can reach it, and every cell is executed through the three places the
// rule lives: the pure predicate, the Go validator (write and stored path),
// and the published JSON Schemas (v1 and v2), which must agree with Go on
// every cell they can see.

func TestDeterministicAnswerPredicateDomainIsEnumeratedAndExecuted(t *testing.T) {
	t.Parallel()
	type row struct{ surface, field, cell, got, want string }
	var table []row
	check := func(surface, field, cell, got, want string) {
		table = append(table, row{surface, field, cell, got, want})
		if got != want {
			t.Errorf("%s / %s / %s: got %s, want %s", surface, field, cell, got, want)
		}
	}
	degraded := ContextFabricInvestigationDegraded

	// ---------------------------------------------------------------- 1. the pure predicate
	supported := func(r ContextFabricInvestigationResult) string {
		return fmt.Sprintf("supported=%t", ContextFabricResultSupported(r))
	}
	{
		r := daFixture(degraded, 1, 1, "")
		r.Completeness = ContextFabricAnswerCompleteness{}
		check("ContextFabricResultSupported", "completeness", "block absent (zero), facts and evidence present", supported(r), "supported=false")
	}
	check("ContextFabricResultSupported", "claimed_facts_count", "zero", supported(daFixture(degraded, 0, 1, "")), "supported=false")
	check("ContextFabricResultSupported", "claimed_facts_count", "boundary: one", supported(daFixture(degraded, 1, 1, "")), "supported=true")
	check("ContextFabricResultSupported", "claimed_facts_count", "boundary+1: two", supported(daFixture(degraded, 2, 1, "")), "supported=true")
	{
		r := daFixture(degraded, 1, 1, "")
		r.Completeness.ClaimedFactsCount = -1
		check("ContextFabricResultSupported", "claimed_facts_count", "boundary-2: negative", supported(r), "supported=false")
	}
	{
		// K5: the COUNT is the authority. A document whose array and count
		// disagree is refused by validateCompleteness before the predicate
		// matters (asserted below); the predicate itself reads the count.
		r := daFixture(degraded, 1, 1, "")
		r.ClaimedFacts = []ContextFabricClaimedFact{}
		check("ContextFabricResultSupported", "claimed_facts_count", "count 1, array empty (reads the count)", supported(r), "supported=true")
		check("Validate (write)", "claimed_facts_count", "count 1, array empty", daVerdict(r.Validate()), "refused")
		r2 := daFixture(degraded, 1, 1, "")
		r2.Completeness.ClaimedFactsCount = 0
		check("ContextFabricResultSupported", "claimed_facts_count", "count 0, array of one (reads the count)", supported(r2), "supported=false")
		check("Validate (write)", "claimed_facts_count", "count 0, array of one", daVerdict(r2.Validate()), "refused")
	}
	{
		r := daFixture(degraded, 1, 0, "")
		r.EvidenceRefIDs = nil
		check("ContextFabricResultSupported", "evidence_ref_ids", "absent (nil)", supported(r), "supported=false")
	}
	check("ContextFabricResultSupported", "evidence_ref_ids", "empty container", supported(daFixture(degraded, 1, 0, "")), "supported=false")
	check("ContextFabricResultSupported", "evidence_ref_ids", "boundary: one", supported(daFixture(degraded, 1, 1, "")), "supported=true")
	check("ContextFabricResultSupported", "evidence_ref_ids", "boundary+1: two", supported(daFixture(degraded, 1, 2, "")), "supported=true")
	{
		r := daFixture(degraded, 1, 1, "")
		r.EvidenceRefIDs = []string{"evidence_00000000", "evidence_00000000"}
		check("ContextFabricResultSupported", "evidence_ref_ids", "duplicate ref", supported(r), "supported=true")
	}
	for _, status := range []ContextFabricInvestigationStatus{ContextFabricInvestigationComplete, ContextFabricInvestigationPartial, degraded, ContextFabricInvestigationClarificationRequired, ContextFabricInvestigationNoMatch} {
		check("ContextFabricResultSupported", "status", "does not read status: "+string(status)+" with no facts", supported(daFixture(status, 0, 1, daAnswer)), "supported=false")
	}

	// ---------------------------------------------- 2. the lower bound, every status x support x answer
	// got = write-path verdict / v1 schema / v2 schema. v2 differs from v1
	// only in schema_version, so the same document with the v2 constant
	// must reach the same verdict through ValidateV2 and the v2 schema.
	verdicts := func(r ContextFabricInvestigationResult) string {
		v2 := r
		v2.SchemaVersion = ContextFabricInvestigationResultSchemaV2
		v2.Versions.ContractVersion = ContextFabricInvestigationResultSchemaV2
		return fmt.Sprintf("go=%s v1=%s go2=%s v2=%s",
			daVerdict(r.Validate()), daSchema(t, r, "context_fabric_investigation_result.v1.schema.json"),
			daVerdict(v2.ValidateV2()), daSchema(t, v2, "context_fabric_investigation_result.v2.schema.json"))
	}
	all := func(v string) string { return fmt.Sprintf("go=%s v1=%s go2=%s v2=%s", v, v, v, v) }
	type statusCase struct {
		status ContextFabricInvestigationStatus
		// the empty answer on an UNSUPPORTED document: the widening.
		unsupportedEmpty string
	}
	for _, sc := range []statusCase{
		{ContextFabricInvestigationComplete, "refused"},
		{ContextFabricInvestigationPartial, "refused"},
		{degraded, "accepted"},
		{ContextFabricInvestigationClarificationRequired, "accepted"},
		{ContextFabricInvestigationNoMatch, "accepted"},
	} {
		s := string(sc.status)
		check("Validate + schemas", "deterministic_answer", s+", unsupported, answer empty", verdicts(daFixture(sc.status, 0, 0, "")), all(sc.unsupportedEmpty))
		check("Validate + schemas", "deterministic_answer", s+", unsupported, answer present", verdicts(daFixture(sc.status, 0, 0, daAnswer)), all("accepted"))
		check("Validate + schemas", "deterministic_answer", s+", supported, answer empty", verdicts(daFixture(sc.status, 1, 1, "")), all("refused"))
		check("Validate + schemas", "deterministic_answer", s+", supported, answer present", verdicts(daFixture(sc.status, 1, 1, daAnswer)), all("accepted"))
	}
	// The conjuncts, through the validator: each alone does not license an
	// empty answer's REQUIREMENT -- i.e. each alone leaves the document
	// unsupported, so the empty answer is allowed.
	check("Validate + schemas", "evidence_ref_ids", "degraded, one fact, NO evidence, answer empty (evidence conjunct)", verdicts(daFixture(degraded, 1, 0, "")), all("accepted"))
	check("Validate + schemas", "claimed_facts_count", "degraded, NO fact, evidence, answer empty (fact conjunct)", verdicts(daFixture(degraded, 0, 1, "")), all("accepted"))
	check("Validate + schemas", "claimed_facts_count", "degraded, two facts, two refs, answer empty", verdicts(daFixture(degraded, 2, 2, "")), all("refused"))
	// The answer sentence's own cells on an unsupported document.
	check("Validate + schemas", "deterministic_answer", "unsupported, whitespace-only (length 1, unchanged)", verdicts(daFixture(degraded, 0, 0, " ")), all("accepted"))
	check("Validate + schemas", "deterministic_answer", "unsupported, one rune", verdicts(daFixture(degraded, 0, 0, "x")), all("accepted"))
	check("Validate + schemas", "deterministic_answer", "unsupported, at the max length", verdicts(daFixture(degraded, 0, 0, strings.Repeat("a", ContextFabricDeterministicAnswerMaxLength))), all("accepted"))
	check("Validate + schemas", "deterministic_answer", "unsupported, max length + 1 (upper bound unchanged)", verdicts(daFixture(degraded, 0, 0, strings.Repeat("a", ContextFabricDeterministicAnswerMaxLength+1))), all("refused"))
	check("Validate + schemas", "deterministic_answer", "supported, max length + 1", verdicts(daFixture(degraded, 1, 1, strings.Repeat("a", ContextFabricDeterministicAnswerMaxLength+1))), all("refused"))
	{
		// A refusal reads no fact, so it is never supported: the empty answer
		// is allowed, and the basis still cannot accompany a fact.
		r := daFixture(degraded, 0, 0, "")
		r.RefusalBasis = ContextFabricRefusalBasisMemberKindUnservable
		r.Completeness.RefusalBasis = r.RefusalBasis
		daRestamp(&r)
		check("Validate + schemas", "refusal_basis", "refused, no fact, answer empty", verdicts(r), all("accepted"))
		r2 := daFixture(degraded, 1, 1, daAnswer)
		r2.RefusalBasis = ContextFabricRefusalBasisMemberKindUnservable
		r2.Completeness.RefusalBasis = r2.RefusalBasis
		daRestamp(&r2)
		check("Validate (write)", "refusal_basis", "refused WITH a fact (existing rule, unchanged)", daVerdict(r2.Validate()), "refused")
	}
	{
		// Disclosure survives the empty answer: every service-authored
		// channel populated at once.
		r := daFixture(degraded, 0, 0, "")
		r.Limitations = []string{"The readiness source was unavailable for this window."}
		r.Warnings = []string{"Coverage was partial."}
		r.Coverage.DegradedReasons = []string{"readiness source unavailable"}
		r.RefusalBasis = ContextFabricRefusalBasisUnspecified
		r.Completeness.RefusalBasis = r.RefusalBasis
		daRestamp(&r)
		check("Validate + schemas", "limitations/warnings/coverage/refusal_basis", "unsupported, every disclosure channel populated, answer empty", verdicts(r), all("accepted"))
	}
	{
		// A clarification that ALSO read facts is supported and keeps its answer.
		r := daFixture(ContextFabricInvestigationClarificationRequired, 1, 1, "")
		check("Validate + schemas", "status", "clarification_required that read a fact, answer empty", verdicts(r), all("refused"))
		r.DeterministicAnswer = daAnswer
		check("Validate + schemas", "status", "clarification_required that read a fact, answer present, prompt kept", verdicts(r), all("accepted"))
	}

	// ---------------------------------------------------------------- 3. the stored path
	{
		legacy := daFixture(degraded, 0, 0, "")
		legacy.Completeness = ContextFabricAnswerCompleteness{}
		check("ValidateStored", "completeness", "legacy row with no block, answer empty (keeps the rule it was written under)", daVerdict(legacy.ValidateStored()), "refused")
		legacy.DeterministicAnswer = daAnswer
		check("ValidateStored", "completeness", "legacy row with no block, answer present", daVerdict(legacy.ValidateStored()), "accepted")
		check("Validate (write)", "completeness", "no block on the write path (required, unchanged)", daVerdict(legacy.Validate()), "refused")
		fresh := daFixture(degraded, 0, 0, "")
		check("ValidateStored", "completeness", "stored row with a block, unsupported, answer empty", daVerdict(fresh.ValidateStored()), "accepted")
		sup := daFixture(degraded, 1, 1, "")
		check("ValidateStored", "completeness", "stored row with a block, supported, answer empty", daVerdict(sup.ValidateStored()), "refused")
		mixed := daFixture(degraded, 0, 0, "")
		mixed.SchemaVersion = ContextFabricInvestigationResultSchemaV2
		mixed.Versions.ContractVersion = ContextFabricInvestigationResultSchemaV2
		check("ValidateStoredResult (dispatch)", "schema_version", "v2 stored row, unsupported, answer empty", daVerdict(ValidateStoredResult(mixed)), "accepted")
	}

	// ------------------------------------------------------------ 4. the wire: what a decoder hands the validator
	{
		encoded, err := json.Marshal(daFixture(degraded, 0, 0, "x"))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(encoded, &doc); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		mutate := func(edit func(map[string]any)) []byte {
			var copyDoc map[string]any
			_ = json.Unmarshal(encoded, &copyDoc)
			edit(copyDoc)
			out, _ := json.Marshal(copyDoc)
			return out
		}
		wire := func(payload []byte) string {
			var decoded ContextFabricInvestigationResult
			if err := json.Unmarshal(payload, &decoded); err != nil {
				return "go=decode-refused v1=" + daVerdict(contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", payload))
			}
			return "go=" + daVerdict(decoded.Validate()) + " v1=" + daVerdict(contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", payload))
		}
		check("wire decode", "deterministic_answer", "key absent, unsupported", wire(mutate(func(m map[string]any) { delete(m, "deterministic_answer") })), "go=accepted v1=refused")
		check("wire decode", "deterministic_answer", "null, unsupported", wire(mutate(func(m map[string]any) { m["deterministic_answer"] = nil })), "go=accepted v1=refused")
		check("wire decode", "deterministic_answer", "wrong scalar type (number)", wire(mutate(func(m map[string]any) { m["deterministic_answer"] = 7 })), "go=decode-refused v1=refused")
		check("wire decode", "claimed_facts_count", "fractional (1.5)", wire(mutate(func(m map[string]any) { m["completeness"].(map[string]any)["claimed_facts_count"] = 1.5 })), "go=decode-refused v1=refused")
		check("wire decode", "claimed_facts_count", "wrong scalar type (string)", wire(mutate(func(m map[string]any) { m["completeness"].(map[string]any)["claimed_facts_count"] = "1" })), "go=decode-refused v1=refused")
		check("wire decode", "evidence_ref_ids", "wrong container type (string)", wire(mutate(func(m map[string]any) { m["evidence_ref_ids"] = "evidence_00000000" })), "go=decode-refused v1=refused")
		check("wire decode", "evidence_ref_ids", "null", wire(mutate(func(m map[string]any) { m["evidence_ref_ids"] = nil })), "go=refused v1=refused")
		check("wire decode", "status", "out of vocabulary", wire(mutate(func(m map[string]any) { m["status"] = "answered" })), "go=refused v1=refused")
	}

	t.Logf("%-32s %-44s %-78s %-40s %s", "SURFACE", "FIELD", "CELL", "GOT", "WANT")
	for _, r := range table {
		t.Logf("%-32s %-44s %-78s %-40s %s", r.surface, r.field, r.cell, r.got, r.want)
	}
	t.Logf("DOMAIN CELLS EXECUTED: %d", len(table))
}
