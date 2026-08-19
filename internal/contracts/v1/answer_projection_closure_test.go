package v1

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
		name          string
		root          string
		prefix        string
		untrusted     []string
		expectedPaths int
	}{
		{name: "answer_projection", root: "answer", prefix: "structured", untrusted: MCPInvestigateQuestionUntrustedFields, expectedPaths: 73},
		{name: "investigation_result", root: "result", prefix: "structured", untrusted: MCPInvestigationResultUntrustedFields, expectedPaths: 213},
	} {
		t.Run(surface.name, func(t *testing.T) {
			paths := stringPathsIn(t, documents, surface.root, surface.prefix)
			// The discovered path COUNT is pinned (codex round-3 P2-3).
			// A non-empty check is not enough: this walker has twice
			// under-reported while looking healthy, and a shrinking path
			// set is exactly what that failure looks like from outside.
			// Pinning makes shrinkage fail as loudly as growth, and both
			// force a deliberate update.
			if len(paths) != surface.expectedPaths {
				t.Fatalf("discovered %d string paths, pinned %d.\nA smaller number means the walker stopped reaching fields; a larger one means the contract grew.\nPaths:\n  %s",
					len(paths), surface.expectedPaths, strings.Join(paths, "\n  "))
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
		"provenance", "outcome", "source_state",
		// CHAOS-3900 W1: window_class/window_confidence/confidence are all
		// closed-vocabulary strings (ContextFabricWindowClass,
		// ContextFabricWindowConfidence) validated against their own
		// registries before a result is stored -- never model prose. Note
		// "confidence" here is a STRING leaf (ContextFabricEffectiveEvidenceWindow.Confidence);
		// every OTHER "confidence" field in this contract (SubjectCandidate,
		// DriverJudgment) is a float64 and never reaches this string walker.
		"window_class", "window_confidence", "confidence",
		// CHAOS-3781: "grain" is ContextFabricTemporalGrain (instant,
		// day, none) and "match_mechanisms" is the closed set of ways a
		// candidate was matched. Both are validated against their own
		// closed vocabulary before a result is stored, so neither can
		// carry model prose -- see validContextFabricTemporalGrain and
		// validMatchMechanisms.
		"grain", "match_mechanisms",
		// CHAOS-3900 P1: "member" (ContextFabricStructureNeedKind),
		// "offer_source" (ContextFabricStructureOfferSource), and
		// "disposition" (ContextFabricStructureDisposition) are all
		// closed-vocabulary strings, validated against their own
		// registries (ValidContextFabricStructureNeedKind/
		// ValidContextFabricStructureOfferSource/
		// ValidContextFabricStructureDisposition) before a result is
		// stored -- never model prose. "missing" is an ARRAY of the same
		// StructureNeedKind enum (StructureNeeds.Validate rejects any
		// non-member entry).
		"member", "offer_source", "disposition", "missing":
		return true
	// Opaque identifiers and digests: frozen handles, never prose.
	case "result_id", "request_id", "receipt_id", "driver_id", "claim_id",
		"finding_id", "path_id", "canonical_id", "turn_id", "schema_version",
		"evidence_ref_ids", "claimed_fact_ids", "path_ids", "content_digest",
		"snapshot_hash", "watermark",
		// CHAOS-3900 P1.E: matched_term_hash is a SHA-256 digest of a
		// normalized term (ContextFabricAnchorOption's own doc comment) --
		// a fixed-length, service-minted hash, never model or source prose.
		"matched_term_hash":
		return true
	// Service-issued identifier vocabularies: ACR chooses these, not a
	// model and not a retrieved document. "source" names a configured
	// Dev Health source; "field" names a canonical fact-provider field.
	case "source":
		return true
	// Service-issued version tokens.
	case "service_version", "contract_version", "backend", "backend_version",
		"projection_version", "query_version", "interpretation_version",
		"synthesis_version", "canonical_service_version", "source_version",
		"model_identity":
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
// It FAILS on any dead end rather than returning what it managed to reach.
// That rule exists because this walker has now silently under-reported
// twice: once when a cross-file $ref lost its document context, and once
// when a depth cap truncated deep paths. Both times it reported a clean
// result while skipping real fields, which is the worst possible behavior
// for a completeness check -- a guard that under-reports is indistinguishable
// from a guard that passes. Every node it cannot traverse is now a test
// failure demanding a deliberate decision.
func stringPathsIn(t *testing.T, documents map[string]map[string]any, root, prefix string) []string {
	t.Helper()
	seen := map[string]bool{}
	var paths []string

	var walk func(document string, node any, path string, depth int)
	walk = func(document string, node any, path string, depth int) {
		if depth > 64 {
			t.Fatalf("%s: walk exceeded the depth guard; the schema is cyclic or the walker is looping", path)
		}
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("%s: node is not a schema object (%T); classification would silently skip it", path, node)
		}
		if reference, ok := object["$ref"].(string); ok {
			nextDocument, resolved := resolveSchemaRef(t, documents, document, reference)
			if resolved == nil {
				t.Fatalf("%s: $ref %q did not resolve; classification would silently skip it", path, reference)
			}
			walk(nextDocument, resolved, path, depth+1)
			return
		}
		// Combinator branches are traversed, not skipped: a string field
		// reachable only through oneOf/anyOf/allOf is still a string field
		// a consumer receives.
		combinators := 0
		// if/then/else are conditional SHAPE, not just presence: a field
		// reachable only through a "then" branch is still a field a
		// consumer receives. The stricter walker found these untraversed
		// at the result root.
		for _, keyword := range []string{"then", "else"} {
			branch, ok := object[keyword].(map[string]any)
			if !ok || onlyPresenceConstraint(branch) {
				continue
			}
			combinators++
			walk(document, branch, path, depth+1)
		}
		for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
			branches, ok := object[keyword].([]any)
			if !ok {
				continue
			}
			for _, branch := range branches {
				// A bare {"required": [...]} branch constrains presence
				// rather than shape and carries no reachable field.
				if constraint, ok := branch.(map[string]any); ok && onlyPresenceConstraint(constraint) {
					continue
				}
				combinators++
				walk(document, branch, path, depth+1)
			}
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
				items, ok := object["items"]
				if !ok {
					t.Fatalf("%s: array has no items schema; classification would silently skip its members", path)
				}
				walk(document, items, path+"[]", depth+1)
				return
			case "object":
				// A map-shaped object carries its value schema in
				// additionalProperties. Its VALUES are as much a string
				// field as any named property, and the strict walker
				// caught them going unclassified.
				if valueSchema, ok := object["additionalProperties"].(map[string]any); ok {
					walk(document, valueSchema, path+"{}", depth+1)
					if _, named := object["properties"]; !named {
						return
					}
				}
				// falls through to properties below
			default:
				return // number, integer, boolean: no string leaf
			}
		}
		properties, ok := object["properties"].(map[string]any)
		if !ok {
			// A const/enum leaf, a pure presence constraint, or a branch
			// already covered by a combinator above carries no properties.
			if _, isConst := object["const"]; isConst {
				return
			}
			if _, isEnum := object["enum"]; isEnum {
				return
			}
			if combinators > 0 || onlyPresenceConstraint(object) {
				return
			}
			t.Fatalf("%s: schema node has no properties, type, const, enum, or combinator; classification would silently skip it (%v)", path, sortedKeysOf(object))
		}
		for name, child := range properties {
			walk(document, child, path+"."+name, depth+1)
		}
	}
	walk(root, documents[root], prefix, 0)
	sort.Strings(paths)
	return paths
}

// onlyPresenceConstraint reports whether a schema node constrains only
// which members must be present, carrying no shape of its own.
func onlyPresenceConstraint(node map[string]any) bool {
	if len(node) == 0 {
		return true
	}
	for key := range node {
		switch key {
		case "required", "not", "description", "title", "$comment", "additionalProperties", "minProperties", "maxProperties", "uniqueItems", "minItems", "maxItems", "if", "then", "else", "properties":
		default:
			return false
		}
	}
	return true
}

func sortedKeysOf(node map[string]any) []string {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// resolveSchemaRef follows a $ref, returning the document it landed in so a
// subsequent local ref resolves against the right file. A pointer that does
// not resolve returns nil, which the caller turns into a test failure.
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
		next, exists := object[key]
		if !exists {
			return target, nil
		}
		node = next
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

// legacyCohortMember builds a cohort member at the OLD Go-validator
// maximum: 50 inclusion reasons of 1024 characters. Rows of this shape were
// legitimately written by an earlier binary and are immutable, so they must
// stay readable forever.
func legacyCohortMember() ContextFabricCohortMember {
	reasons := make([]string, 0, contextFabricLegacyBounds.cohortInclusionReasons)
	for i := 0; i < contextFabricLegacyBounds.cohortInclusionReasons; i++ {
		head := "legacy-reason-" + strconv.Itoa(i) + "-"
		reasons = append(reasons, head+strings.Repeat("x", contextFabricLegacyBounds.cohortInclusionReasonLength-len(head)))
	}
	return ContextFabricCohortMember{
		Subject:          ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_legacy", Label: "Team Legacy"},
		Rank:             1,
		InclusionReasons: reasons,
		EvidenceRefIDs:   []string{},
	}
}

// TestLegacyCohortBoundsAreStrictOnWriteAndLenientOnRead is the codex
// round-3 P1-1 regression.
//
// Round 2 tightened the cohort inclusion-reason bound from 50x1024 to the
// published 32x1000. That was right for writes and wrong for reads:
// investigation results are IMMUTABLE, so a row written at the old maximum
// cannot be migrated, and a read path enforcing the new bound would turn it
// into a permanent API 500 and MCP retrieval failure after deploy.
//
// The rule is strict-write, lenient-read. This proves both halves on the
// SAME value: the write validator rejects it, the stored-read validator
// accepts it.
func TestLegacyCohortBoundsAreStrictOnWriteAndLenientOnRead(t *testing.T) {
	member := legacyCohortMember()

	if err := member.Validate(); err == nil {
		t.Error("the write path accepted a legacy-sized cohort member; new rows must not be creatable at the legacy size")
	}
	if err := member.validateStored(); err != nil {
		t.Errorf("the read path rejected a legacy-sized cohort member, making an immutable stored row unreadable: %v", err)
	}

	// And the same, end to end, through a whole result document.
	result := closureResult()
	result.Cohort = &ContextFabricCohort{
		Kind:      ContextFabricSubjectTeam,
		Members:   []ContextFabricCohortMember{member},
		Rationale: "Legacy cohort written by an earlier binary.",
		Complete:  true,
	}
	if err := result.Validate(); err == nil {
		t.Error("Validate accepted a legacy-sized result; writes must enforce the current contract")
	}
	if err := result.ValidateStored(); err != nil {
		t.Errorf("ValidateStored rejected a legacy stored result: %v", err)
	}

	// A value beyond even the legacy maximum stays invalid on both paths:
	// the allowance is bounded history, not an open door.
	beyond := member
	beyond.InclusionReasons = append(append([]string{}, member.InclusionReasons...), "one-too-many")
	if err := beyond.validateStored(); err == nil {
		t.Error("the read path accepted a member beyond the legacy maximum")
	}
}
