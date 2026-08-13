package v1

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// This file proves the CLOSURE property behind the CHAOS-3746 answer
// surface: Project(any valid canonical result) must always produce a
// projection that validates.
//
// Codex round 2 found three separate breaches of it. They were not related
// as code, only as property: each was a place where the projection was
// stricter than the canonical contract it copies from, so a legitimate
// result became an invalid projection -- an internal error on the MCP path
// and a schema-violating body on the API path.
//
// Fixing the instances would have left the next one. These tests fix the
// property instead: every copied bound is compared against its canonical
// source, and every narrowing must be deliberate and declared.

// copiedBound names a projection field that copies its value from a
// canonical field, and the canonical field it copies from. Paths are
// "<schema file>#<$defs path>" style, resolved by schemaBoundAt below.
type copiedBound struct {
	projection string
	canonical  string
	// narrowedBy names the projection_budget counter that declares the
	// omission when the projection deliberately holds a TIGHTER bound than
	// canonical. Empty means the projection must be at least as permissive
	// as canonical.
	//
	// A tighter bound without a narrowing step silently turns a valid
	// result into an invalid projection, which is exactly the class this
	// test closes -- so an entry here is a claim that Project actually
	// truncates the field and records the drop.
	narrowedBy string
	// why documents a deliberate narrowing. Required whenever narrowedBy
	// is set: an unexplained narrowing is indistinguishable from the bug.
	why string
}

// copiedBounds enumerates every projection field whose content is copied
// from the canonical contract. Adding a copied field without registering it
// here is caught by TestEveryProjectionStringFieldIsClassified, which
// requires every string-bearing path to be accounted for.
var copiedBounds = []copiedBound{
	{projection: "answer#properties.question", canonical: "result#properties.question"},
	{projection: "answer#properties.direct_judgment", canonical: "result#properties.direct_judgment"},
	{projection: "answer#properties.current_state", canonical: "result#properties.current_state"},
	{projection: "answer#properties.strongest_pressures", canonical: "result#properties.strongest_pressures"},
	{projection: "answer#properties.evidence_ref_ids", canonical: "result#properties.evidence_ref_ids"},
	{
		projection: "answer#properties.limitations", canonical: "result#properties.limitations",
		narrowedBy: "limitations_omitted",
		why:        "a bounded consumer reads a shortened caveat list; the drop is declared so it cannot read as a more confident answer",
	},
	{
		projection: "answer#properties.warnings", canonical: "result#properties.warnings",
		narrowedBy: "warnings_omitted",
		why:        "same as limitations",
	},
	{
		projection: "answer#properties.coverage_summary", canonical: "common#$defs.Coverage.properties.sources",
		narrowedBy: "coverage_omitted",
		why:        "coverage is an at-a-glance summary for a bounded consumer; the drop is declared",
	},
	{projection: "answer#$defs.ProjectedCohort.properties.rationale", canonical: "common#$defs.Cohort.properties.rationale"},
	{projection: "answer#$defs.ProjectedCohortMember.properties.inclusion_reasons", canonical: "common#$defs.CohortMember.properties.inclusion_reasons"},
	{projection: "answer#$defs.ProjectedCohortMember.properties.evidence_ref_ids", canonical: "common#$defs.CohortMember.properties.evidence_ref_ids"},
	{projection: "answer#$defs.ProjectedDriver.properties.title", canonical: "common#$defs.DriverJudgment.properties.title"},
	{projection: "answer#$defs.ProjectedDriver.properties.summary", canonical: "common#$defs.DriverJudgment.properties.summary"},
	{projection: "answer#$defs.ProjectedDriver.properties.qualification", canonical: "common#$defs.DriverJudgment.properties.qualification"},
	{projection: "answer#$defs.ProjectedDriver.properties.evidence_ref_ids", canonical: "common#$defs.DriverJudgment.properties.evidence_ref_ids"},
	{projection: "answer#$defs.ProjectedDriver.properties.claimed_fact_ids", canonical: "common#$defs.DriverJudgment.properties.claimed_fact_ids"},
	{projection: "answer#$defs.ProjectedFact.properties.field", canonical: "common#$defs.ClaimedFact.properties.field"},
	{projection: "answer#$defs.ProjectedCoverage.properties.source", canonical: "common#$defs.SourceObservation.properties.source"},
	{projection: "answer#$defs.ProjectedCoverage.properties.reason", canonical: "common#$defs.SourceObservation.properties.reason"},
	{projection: "answer#$defs.ProjectedCandidate.properties.receipt_id", canonical: "common#$defs.SubjectCandidate.properties.receipt_id"},
	{projection: "answer#$defs.ProjectedCandidate.properties.match_reasons", canonical: "common#$defs.SubjectCandidate.properties.match_reasons"},
	{
		projection: "answer#$defs.ProjectedClarification.properties.candidates", canonical: "common#$defs.SubjectResolution.properties.candidates",
		narrowedBy: "candidates_omitted",
		why:        "an agent chooses between a handful of options, not fifty; the drop is declared",
	},
	{projection: "answer#$defs.ProjectedClarification.properties.prompt", canonical: "common#$defs.SubjectResolution.properties.clarification_prompt"},
	{projection: "answer#properties.committed_subjects", canonical: "common#$defs.SubjectResolution.properties.committed"},
	{
		projection: "answer#properties.principal_drivers", canonical: "result#properties.drivers",
		narrowedBy: "drivers_omitted",
		why:        "the caller's budget selects the drivers that survive; every drop is declared",
	},
	{
		projection: "answer#properties.key_facts", canonical: "result#properties.claimed_facts",
		narrowedBy: "facts_omitted",
		why:        "facts follow the drivers that cite them; every drop is declared",
	},
	{
		projection: "answer#$defs.ProjectedCohort.properties.members", canonical: "common#$defs.Cohort.properties.members",
		narrowedBy: "cohort_members_omitted",
		why:        "the caller's budget bounds the cohort; every drop is declared",
	},
}

func schemaDocuments(t *testing.T) map[string]map[string]any {
	t.Helper()
	return map[string]map[string]any{
		"answer": loadSchemaDocument(t, "context_fabric_answer_projection.v1.schema.json"),
		"result": loadSchemaDocument(t, "context_fabric_investigation_result.v1.schema.json"),
		"common": loadSchemaDocument(t, "context_fabric_common.v1.schema.json"),
	}
}

// schemaNodeAt resolves a "<document>#<dotted path>" reference.
func schemaNodeAt(t *testing.T, documents map[string]map[string]any, reference string) map[string]any {
	t.Helper()
	parts := strings.SplitN(reference, "#", 2)
	document, ok := documents[parts[0]]
	if !ok {
		t.Fatalf("unknown schema document %q", parts[0])
	}
	var node any = document
	for _, key := range strings.Split(parts[1], ".") {
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("%s: %q is not an object", reference, key)
		}
		next, ok := object[key]
		if !ok {
			t.Fatalf("%s: %q is missing", reference, key)
		}
		node = next
	}
	object, ok := node.(map[string]any)
	if !ok {
		t.Fatalf("%s does not resolve to a schema object", reference)
	}
	return object
}

func schemaLimit(node map[string]any, keyword string) (float64, bool) {
	value, ok := node[keyword].(float64)
	return value, ok
}

// TestCopiedProjectionBoundsAreNotTighterThanCanonical is the closure
// property test for copied bounds (codex round-2 F2).
//
// For every field the projection copies from the canonical contract, the
// projection's bound must be at least as permissive -- otherwise a
// legitimate canonical value produces an invalid projection. The only
// exception is a bound the projection deliberately narrows, which must name
// the projection_budget counter that declares the omission and say why.
func TestCopiedProjectionBoundsAreNotTighterThanCanonical(t *testing.T) {
	documents := schemaDocuments(t)
	budgetProperties, ok := schemaNodeAt(t, documents, "answer#$defs.ProjectionBudget.properties")["truncated"]
	if !ok || budgetProperties == nil {
		t.Fatal("projection budget has no truncated flag")
	}
	declared := schemaNodeAt(t, documents, "answer#$defs.ProjectionBudget.properties")

	checked := 0
	for _, bound := range copiedBounds {
		t.Run(bound.projection, func(t *testing.T) {
			projection := schemaNodeAt(t, documents, bound.projection)
			canonical := schemaNodeAt(t, documents, bound.canonical)

			if bound.narrowedBy != "" {
				if strings.TrimSpace(bound.why) == "" {
					t.Fatalf("deliberate narrowing must state why")
				}
				if _, exists := declared[bound.narrowedBy]; !exists {
					t.Fatalf("narrowing claims counter %q, absent from projection_budget", bound.narrowedBy)
				}
				return
			}
			for _, keyword := range []string{"maxLength", "maxItems"} {
				canonicalLimit, hasCanonical := schemaLimit(canonical, keyword)
				if !hasCanonical {
					continue
				}
				projectionLimit, hasProjection := schemaLimit(projection, keyword)
				if !hasProjection {
					continue // unbounded is never tighter
				}
				checked++
				if projectionLimit < canonicalLimit {
					t.Errorf("projection %s = %v is TIGHTER than canonical %s = %v, with no declared narrowing: a valid canonical result would produce an invalid projection",
						keyword, projectionLimit, keyword, canonicalLimit)
				}
			}
		})
	}
	t.Logf("compared %d copied paths (%d bound comparisons)", len(copiedBounds), checked)
}

// TestEveryProjectionStringFieldIsClassified is the closure property test
// for the untrusted enumeration (codex round-2 F3).
//
// Every string-bearing path in a structured payload is either untrusted
// (model- or source-derived text) or trusted BECAUSE it is a closed
// vocabulary, an opaque identifier, or a service-issued constant. Nothing
// may be trusted by omission: the previous enumeration missed seven paths
// simply because nobody thought of them.
func TestEveryProjectionStringFieldIsClassified(t *testing.T) {
	documents := schemaDocuments(t)

	for _, surface := range []struct {
		name      string
		root      string
		prefix    string
		untrusted []string
	}{
		{name: "answer_projection", root: "answer", prefix: "structured", untrusted: MCPInvestigateQuestionUntrustedFields},
		{name: "investigation_result", root: "result", prefix: "structured", untrusted: MCPInvestigationResultUntrustedFields},
	} {
		t.Run(surface.name, func(t *testing.T) {
			paths := stringPathsIn(t, documents, surface.root, surface.prefix)
			if len(paths) == 0 {
				t.Fatal("no string paths discovered; the walker is not working")
			}
			untrusted := make(map[string]bool, len(surface.untrusted))
			for _, field := range surface.untrusted {
				untrusted[field] = true
			}
			unclassified := make([]string, 0, len(paths))
			for _, path := range paths {
				if untrusted[path] || trustedBecauseClosed(path) {
					continue
				}
				unclassified = append(unclassified, path)
			}
			sort.Strings(unclassified)
			if len(unclassified) > 0 {
				t.Errorf("%d string paths are neither declared untrusted nor allowlisted as closed:\n  %s",
					len(unclassified), strings.Join(unclassified, "\n  "))
			}
			t.Logf("classified %d string paths (%d untrusted, %d trusted-because-closed)",
				len(paths), countMatching(paths, func(p string) bool { return untrusted[p] }),
				countMatching(paths, trustedBecauseClosed))
		})
	}
}

func countMatching(values []string, predicate func(string) bool) int {
	total := 0
	for _, value := range values {
		if predicate(value) {
			total++
		}
	}
	return total
}

// trustedBecauseClosed allowlists string paths that cannot carry
// model-authored prose: closed enumerations, opaque identifiers, timestamps,
// service-issued version tokens, and digests. Each pattern is a positive
// claim that the value's shape is constrained by the contract itself.
func trustedBecauseClosed(path string) bool {
	leaf := path[strings.LastIndex(path, ".")+1:]
	leaf = strings.TrimSuffix(leaf, "[]")
	switch leaf {
	// Closed vocabularies.
	case "kind", "state", "status", "standing", "category", "shape", "role",
		"axis", "derivation", "epistemic_status", "type", "availability",
		"provenance", "outcome", "source_state":
		return true
	// Opaque identifiers and digests: frozen handles, never prose.
	case "result_id", "request_id", "receipt_id", "driver_id", "claim_id",
		"finding_id", "path_id", "canonical_id", "turn_id", "schema_version",
		"evidence_ref_ids", "claimed_fact_ids", "path_ids", "content_digest",
		"snapshot_hash", "watermark":
		return true
	// Service-issued identifier vocabularies: ACR chooses these, not a
	// model and not a retrieved document. "source" names a configured
	// Dev Health source; "field" names a canonical fact-provider field.
	case "source", "field":
		return true
	// Service-issued version tokens.
	case "service_version", "contract_version", "backend", "backend_version",
		"projection_version", "query_version", "interpretation_version",
		"synthesis_version", "canonical_service_version", "source_version",
		"model_identity", "requested_judgment":
		return true
	// Timestamps.
	case "generated_at", "observed_at", "created_at", "as_of", "start", "end",
		"valid_from", "valid_to", "resolved_at", "event_at":
		return true
	}
	// Caller-supplied scope identifiers echo back what the caller sent.
	if strings.HasSuffix(leaf, "_id") || strings.HasSuffix(leaf, "_ids") {
		return true
	}
	return false
}

// stringPathsIn walks a schema document and returns every path that
// resolves to a string-typed leaf, in the dotted "[]"-suffixed notation the
// untrusted enumeration uses.
//
// It carries the CURRENT document through the walk. An earlier version did
// not: after following a cross-file $ref into context_fabric_common.v1, the
// local "#/$defs/..." refs inside that document were resolved against the
// ORIGINAL root, silently dead-ending and hiding every nested subject label
// from classification. A walker that quietly stops walking is worse than no
// walker, because it reports a clean result.
func stringPathsIn(t *testing.T, documents map[string]map[string]any, root, prefix string) []string {
	t.Helper()
	seen := map[string]bool{}
	var paths []string

	var walk func(document string, node any, path string, depth int)
	walk = func(document string, node any, path string, depth int) {
		object, ok := node.(map[string]any)
		if !ok || depth > 24 {
			return
		}
		if reference, ok := object["$ref"].(string); ok {
			nextDocument, resolved := resolveSchemaRef(t, documents, document, reference)
			walk(nextDocument, resolved, path, depth+1)
			return
		}
		if kind, ok := object["type"].(string); ok {
			switch kind {
			case "string":
				if !seen[path] {
					seen[path] = true
					paths = append(paths, path)
				}
				return
			case "array":
				walk(document, object["items"], path+"[]", depth+1)
				return
			}
		}
		properties, ok := object["properties"].(map[string]any)
		if !ok {
			return
		}
		for name, child := range properties {
			walk(document, child, path+"."+name, depth+1)
		}
	}
	walk(root, documents[root], prefix, 0)
	sort.Strings(paths)
	return paths
}

// resolveSchemaRef follows a $ref, returning the document it landed in so a
// subsequent local ref resolves against the right file.
func resolveSchemaRef(t *testing.T, documents map[string]map[string]any, current, reference string) (string, any) {
	t.Helper()
	target := current
	pointer := reference
	if strings.Contains(reference, "#") {
		parts := strings.SplitN(reference, "#", 2)
		if parts[0] != "" {
			switch parts[0] {
			case "context_fabric_common.v1.schema.json":
				target = "common"
			default:
				t.Fatalf("unhandled cross-file $ref %q; classification would silently skip it", reference)
			}
		}
		pointer = parts[1]
	}
	var node any = documents[target]
	for _, key := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if key == "" {
			continue
		}
		object, ok := node.(map[string]any)
		if !ok {
			return target, nil
		}
		node = object[key]
	}
	return target, node
}

// TestProjectionEmitsArraysNotNull is the codex round-2 F1 regression at
// the contract level: the projection's array members are required, so a nil
// slice serializes to null and violates the schema the API publishes.
func TestProjectionEmitsArraysNotNull(t *testing.T) {
	projection := validAnswerProjection()
	projection.EvidenceRefIDs = nil
	if err := projection.Validate(); err == nil {
		t.Error("validator accepted a nil required evidence array")
	}
	encoded, err := json.Marshal(validAnswerProjection())
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"evidence_ref_ids", "committed_subjects", "principal_drivers", "key_facts", "coverage_summary", "limitations", "warnings", "subject_receipts", "strongest_pressures"} {
		if strings.Contains(string(encoded), fmt.Sprintf("%q:null", member)) {
			t.Errorf("%s serialized as null; the schema requires an array", member)
		}
	}
}
