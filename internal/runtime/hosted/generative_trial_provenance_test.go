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
	// instance's values): every JSON tag trialCandidateMatchProvenance
	// exposes must be one of the four allowed names. A field added later
	// (e.g. "label" or "matched_terms") fails here even before any test
	// data happens to populate it.
	allowedTags := map[string]bool{"kind": true, "canonical_id": true, "mechanisms": true, "confidence": true}
	var asMap map[string]json.RawMessage
	full := trialCandidateMatchProvenanceAllFields()
	fullBlob, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal full-field provenance: %v", err)
	}
	if err := json.Unmarshal(fullBlob, &asMap); err != nil {
		t.Fatalf("unmarshal full-field provenance: %v", err)
	}
	for tag := range asMap {
		if !allowedTags[tag] {
			t.Fatalf("trialCandidateMatchProvenance emitted unexpected JSON field %q -- only kind/canonical_id/mechanisms/confidence are allowed (privacy posture)", tag)
		}
	}
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
