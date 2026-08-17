package hosted_test

import (
	"encoding/json"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-3880: the trial-result artifact could not attribute an observed
// commit to its retrieval population (vector-only vs lexical-only vs
// corroborated) -- the CHAOS-3742/CHAOS-3857 threshold-sweep verification
// had to infer that from where TopCandidateConfidence happened to land
// inside a known band, "which only worked by luck" (team-lead's framing).
// These tests pin the fix: committedMatchProvenance/topNonCommittedMatchProvenance
// record MatchMechanisms+Confidence directly, and never leak anything beyond
// IDs/kinds/mechanisms/confidences into the artifact.

func subjectCandidate(kind contractsv1.ContextFabricSubjectKind, id string, confidence float64, mechanisms ...contractsv1.ContextFabricSubjectMatchMechanism) contractsv1.ContextFabricSubjectCandidate {
	return contractsv1.ContextFabricSubjectCandidate{
		ReceiptID:       "receipt_" + id,
		Subject:         contractsv1.ContextFabricSubjectRef{Kind: kind, CanonicalID: id, Label: "SECRET LABEL " + id},
		State:           contractsv1.ContextFabricResolutionCommitted,
		MatchedTerms:    []string{"secret search term " + id},
		MatchReasons:    []string{"secret match reason " + id},
		Confidence:      confidence,
		MatchMechanisms: mechanisms,
	}
}

func TestCommittedMatchProvenance_recordsMechanismsAndConfidenceForTheCommittedSubject(t *testing.T) {
	committed := []contractsv1.ContextFabricSubjectRef{{Kind: "project", CanonicalID: "project_x"}}
	candidates := []contractsv1.ContextFabricSubjectCandidate{
		subjectCandidate("project", "project_x", 0.79, contractsv1.ContextFabricMatchVector, contractsv1.ContextFabricMatchLexical),
		subjectCandidate("project", "project_y", 0.55, contractsv1.ContextFabricMatchVector),
	}

	matches := committedMatchProvenance(committed, candidates)

	if len(matches) != 1 {
		t.Fatalf("want exactly one committed match, got %d: %+v", len(matches), matches)
	}
	got := matches[0]
	if got.Kind != "project" || got.CanonicalID != "project_x" {
		t.Fatalf("committed match identity = %+v, want project/project_x", got)
	}
	if got.Confidence != 0.79 {
		t.Fatalf("committed match confidence = %v, want 0.79", got.Confidence)
	}
	wantMechanisms := []string{"vector", "lexical"}
	if len(got.Mechanisms) != len(wantMechanisms) {
		t.Fatalf("committed match mechanisms = %v, want %v", got.Mechanisms, wantMechanisms)
	}
	for i, m := range wantMechanisms {
		if got.Mechanisms[i] != m {
			t.Fatalf("committed match mechanisms = %v, want %v", got.Mechanisms, wantMechanisms)
		}
	}
}

func TestCommittedMatchProvenance_emptyWhenNothingCommitted(t *testing.T) {
	candidates := []contractsv1.ContextFabricSubjectCandidate{
		subjectCandidate("project", "project_x", 0.6, contractsv1.ContextFabricMatchVector),
	}
	if matches := committedMatchProvenance(nil, candidates); matches != nil {
		t.Fatalf("committedMatchProvenance(nil committed) = %+v, want nil", matches)
	}
}

func TestTopNonCommittedMatchProvenance_picksHighestConfidenceCandidateExcludingCommitted(t *testing.T) {
	committed := []contractsv1.ContextFabricSubjectRef{{Kind: "project", CanonicalID: "project_x"}}
	candidates := []contractsv1.ContextFabricSubjectCandidate{
		subjectCandidate("project", "project_x", 0.79, contractsv1.ContextFabricMatchVector, contractsv1.ContextFabricMatchLexical),
		subjectCandidate("project", "project_y", 0.6, contractsv1.ContextFabricMatchLexical),
		subjectCandidate("work_item", "wi_z", 0.55, contextfabricMatchVectorOnly()...),
	}

	got := topNonCommittedMatchProvenance(committed, candidates)

	if got == nil {
		t.Fatal("topNonCommittedMatchProvenance returned nil, want the project_y runner-up")
	}
	if got.Kind != "project" || got.CanonicalID != "project_y" {
		t.Fatalf("top non-committed = %+v, want project/project_y (0.6 beats 0.55, and project_x is excluded as committed)", got)
	}
	if got.Confidence != 0.6 {
		t.Fatalf("top non-committed confidence = %v, want 0.6", got.Confidence)
	}
	if len(got.Mechanisms) != 1 || got.Mechanisms[0] != "lexical" {
		t.Fatalf("top non-committed mechanisms = %v, want [lexical]", got.Mechanisms)
	}
}

func contextfabricMatchVectorOnly() []contractsv1.ContextFabricSubjectMatchMechanism {
	return []contractsv1.ContextFabricSubjectMatchMechanism{contractsv1.ContextFabricMatchVector}
}

func TestTopNonCommittedMatchProvenance_nilWhenEveryCandidateIsCommitted(t *testing.T) {
	committed := []contractsv1.ContextFabricSubjectRef{{Kind: "project", CanonicalID: "project_x"}}
	candidates := []contractsv1.ContextFabricSubjectCandidate{
		subjectCandidate("project", "project_x", 0.9, contractsv1.ContextFabricMatchExact),
	}
	if got := topNonCommittedMatchProvenance(committed, candidates); got != nil {
		t.Fatalf("topNonCommittedMatchProvenance = %+v, want nil (only candidate is committed)", got)
	}
}

func TestTopNonCommittedMatchProvenance_nilWhenNoCandidatesAtAll(t *testing.T) {
	if got := topNonCommittedMatchProvenance(nil, nil); got != nil {
		t.Fatalf("topNonCommittedMatchProvenance(nil, nil) = %+v, want nil", got)
	}
}

// TestTopNonCommittedMatchProvenance_tieBreaksDeterministicallyByKeyRegardlessOfInputOrder
// is the sol-review-requested determinism pin: graphrank's own output order
// happens to be stable today, but this function must not silently depend on
// that as an invisible cross-package invariant. Two non-committed candidates
// share the SAME confidence (0.6) -- project_b (key "project|project_b")
// must always win over work_item_a (key "work_item|work_item_a") because
// "project" < "work_item" lexically, and that must hold NO MATTER which
// order the caller's candidates slice lists them in. A result-file reader
// diffing two runs over an identical underlying candidate SET must see the
// same runner-up every time.
func TestTopNonCommittedMatchProvenance_tieBreaksDeterministicallyByKeyRegardlessOfInputOrder(t *testing.T) {
	a := subjectCandidate("project", "project_b", 0.6, contractsv1.ContextFabricMatchLexical)
	b := subjectCandidate("work_item", "work_item_a", 0.6, contractsv1.ContextFabricMatchVector)

	forward := topNonCommittedMatchProvenance(nil, []contractsv1.ContextFabricSubjectCandidate{a, b})
	backward := topNonCommittedMatchProvenance(nil, []contractsv1.ContextFabricSubjectCandidate{b, a})

	if forward == nil || backward == nil {
		t.Fatalf("forward=%+v backward=%+v, want both non-nil", forward, backward)
	}
	if forward.Kind != "project" || forward.CanonicalID != "project_b" {
		t.Fatalf("forward-order result = %+v, want project/project_b (lexically smallest key wins a confidence tie)", forward)
	}
	if backward.Kind != forward.Kind || backward.CanonicalID != forward.CanonicalID {
		t.Fatalf("input-order dependence detected: forward=%+v backward=%+v, want identical results for the same candidate set in either order", forward, backward)
	}
}

// TestCandidateMatchProvenanceNeverCarriesLabelsOrSearchText is the CHAOS-3880
// privacy canary: trialCandidateMatchProvenance's own field set is IDs/kinds/
// mechanisms/confidences only (see its doc comment) -- this proves that at
// the JSON-wire level, not just by reading the struct definition, so a future
// field added to trialCandidateMatchProvenance or a change to
// candidateMatchProvenance that starts forwarding Subject.Label/MatchedTerms/
// MatchReasons fails this test rather than silently shipping a leak.
func TestCandidateMatchProvenanceNeverCarriesLabelsOrSearchText(t *testing.T) {
	cand := subjectCandidate("project", "project_secret_id", 0.83, contractsv1.ContextFabricMatchLexical, contractsv1.ContextFabricMatchVector)

	got := candidateMatchProvenance(cand)

	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal candidateMatchProvenance: %v", err)
	}
	text := string(blob)

	// The struct's OWN allowed vocabulary: kind, canonical_id, mechanisms,
	// confidence. Fail if it ever contains the forbidden source fields.
	forbidden := []string{"SECRET LABEL", "secret search term", "secret match reason", "matched_terms", "match_reasons", "label"}
	for _, f := range forbidden {
		if strings.Contains(strings.ToLower(text), strings.ToLower(f)) {
			t.Fatalf("candidateMatchProvenance JSON leaked forbidden content %q: %s", f, text)
		}
	}

	// Positive assertion: the allowed fields ARE present, so this test is
	// exercising something real, not merely absence-of-everything.
	for _, want := range []string{`"kind":"project"`, `"canonical_id":"project_secret_id"`, `"confidence":0.83`, `"mechanisms":["lexical","vector"]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("candidateMatchProvenance JSON = %s, want it to contain %q", text, want)
		}
	}

	// Reflection-level canary on the STRUCT SHAPE itself (not only this one
	// instance's values): every JSON KEY trialCandidateMatchProvenance
	// exposes, AT ANY NESTING DEPTH, must be one of the four allowed names.
	// A field added later (e.g. "label" or "matched_terms") fails here even
	// before any test data happens to populate it -- and walkJSONObjectKeys
	// recurses into nested objects/arrays, so a FUTURE field that is itself
	// a struct (not just a new top-level scalar) is caught too, not only a
	// flat top-level key.
	allowedTags := map[string]bool{"kind": true, "canonical_id": true, "mechanisms": true, "confidence": true}
	full := trialCandidateMatchProvenanceAllFields()
	fullBlob, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal full-field provenance: %v", err)
	}
	seen := map[string]bool{}
	walkJSONObjectKeys(t, fullBlob, seen)
	if len(seen) == 0 {
		t.Fatal("walkJSONObjectKeys found no object keys at all -- the canary is not exercising anything")
	}
	for tag := range seen {
		if !allowedTags[tag] {
			t.Fatalf("trialCandidateMatchProvenance emitted unexpected JSON field %q -- only kind/canonical_id/mechanisms/confidence are allowed (privacy posture)", tag)
		}
	}
}

// walkJSONObjectKeys recursively collects every JSON OBJECT key found in raw,
// at any nesting depth (into nested objects and array elements alike), into
// seen. Used by the privacy canary so a future field that is itself a
// nested struct -- not just a new top-level scalar -- cannot silently add an
// unreviewed key that a flat, single-level unmarshal-to-map check would
// miss entirely.
func walkJSONObjectKeys(t *testing.T, raw json.RawMessage, seen map[string]bool) {
	t.Helper()
	var asObject map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asObject); err == nil {
		for key, value := range asObject {
			seen[key] = true
			walkJSONObjectKeys(t, value, seen)
		}
		return
	}
	var asArray []json.RawMessage
	if err := json.Unmarshal(raw, &asArray); err == nil {
		for _, elem := range asArray {
			walkJSONObjectKeys(t, elem, seen)
		}
		return
	}
	// A scalar (string/number/bool/null) or malformed input carries no
	// object keys of its own -- nothing further to walk.
}

// trialCandidateMatchProvenanceAllFields returns an instance with every
// field set to a non-zero value, so the reflection canary above sees every
// JSON tag the type can ever emit (a zero-valued field would be dropped by
// omitempty and silently escape the check).
func trialCandidateMatchProvenanceAllFields() trialCandidateMatchProvenance {
	return trialCandidateMatchProvenance{
		Kind:        "project",
		CanonicalID: "project_x",
		Mechanisms:  []string{"vector"},
		Confidence:  0.5,
	}
}

// TestCaseOutcome_newProvenanceFieldsAreAdditiveAndOptional pins the
// backward-compatibility requirement CHAOS-3880 scoped this change to: a
// pre-CHAOS-3880 report JSON (no committed_matches/top_non_committed_match
// keys at all) must still decode cleanly, and a caseOutcome with the new
// fields left unset must marshal WITHOUT emitting those keys, so an existing
// reader that does not know about them sees exactly the shape it always saw.
func TestCaseOutcome_newProvenanceFieldsAreAdditiveAndOptional(t *testing.T) {
	legacy := `{"index":0,"is_control":false,"outcome":"correct","stage":"usable_answer","committed_count":1,"latency_ms":120}`
	var decoded caseOutcome
	if err := json.Unmarshal([]byte(legacy), &decoded); err != nil {
		t.Fatalf("decode pre-CHAOS-3880 report shape: %v", err)
	}
	if decoded.CommittedMatches != nil {
		t.Fatalf("decoded CommittedMatches = %+v, want nil for a legacy payload with no such key", decoded.CommittedMatches)
	}
	if decoded.TopNonCommittedMatch != nil {
		t.Fatalf("decoded TopNonCommittedMatch = %+v, want nil for a legacy payload with no such key", decoded.TopNonCommittedMatch)
	}

	blob, err := json.Marshal(caseOutcome{Index: 0, Outcome: "correct", Stage: "usable_answer", CommittedCount: 1, LatencyMS: 120})
	if err != nil {
		t.Fatalf("marshal zero-valued new fields: %v", err)
	}
	text := string(blob)
	for _, key := range []string{"committed_matches", "top_non_committed_match"} {
		if strings.Contains(text, key) {
			t.Fatalf("caseOutcome JSON = %s, want it to OMIT %q when unset (omitempty)", text, key)
		}
	}
}

// legacyCaseOutcome is a stand-in for "a reader written before CHAOS-3880":
// only the fields caseOutcome carried before this change. encoding/json
// silently ignores JSON object keys a target struct does not declare (no
// DisallowUnknownFields anywhere in this harness or in scripts/trial/'s
// shell readers, which never parse specific fields at all -- grepped), so
// this is a real guarantee, not an assumption, but CHAOS-3880's
// backward-compatibility requirement ("old result files must remain
// parseable by anything that reads them") cuts both ways: it is just as
// important that something reading OLD code against a NEW-shaped file does
// not break as the reverse (already covered above).
type legacyCaseOutcome struct {
	Index          int    `json:"index"`
	Outcome        string `json:"outcome"`
	Stage          string `json:"stage"`
	CommittedCount int    `json:"committed_count"`
	LatencyMS      int64  `json:"latency_ms"`
}

// TestLegacyReader_ignoresTheNewProvenanceFieldsInANewReportFile is the
// OTHER direction of the backward-compatibility requirement: a NEW report
// JSON (committed_matches/top_non_committed_match present) decoded by an OLD
// reader shape must succeed and simply not see the new data, not error or
// corrupt the fields it does know about.
func TestLegacyReader_ignoresTheNewProvenanceFieldsInANewReportFile(t *testing.T) {
	newShaped := caseOutcome{
		Index: 3, Outcome: "correct", Stage: "usable_answer", CommittedCount: 1, LatencyMS: 250,
		CommittedMatches:     []trialCandidateMatchProvenance{{Kind: "project", CanonicalID: "project_x", Mechanisms: []string{"vector", "lexical"}, Confidence: 0.79}},
		TopNonCommittedMatch: &trialCandidateMatchProvenance{Kind: "project", CanonicalID: "project_y", Mechanisms: []string{"lexical"}, Confidence: 0.6},
	}
	blob, err := json.Marshal(newShaped)
	if err != nil {
		t.Fatalf("marshal new-shaped caseOutcome: %v", err)
	}

	var legacy legacyCaseOutcome
	if err := json.Unmarshal(blob, &legacy); err != nil {
		t.Fatalf("a pre-CHAOS-3880 reader shape failed to decode a NEW report file: %v", err)
	}
	if legacy.Index != 3 || legacy.Outcome != "correct" || legacy.Stage != "usable_answer" || legacy.CommittedCount != 1 || legacy.LatencyMS != 250 {
		t.Fatalf("legacy reader decoded = %+v, want the known fields carried through unchanged", legacy)
	}
}
