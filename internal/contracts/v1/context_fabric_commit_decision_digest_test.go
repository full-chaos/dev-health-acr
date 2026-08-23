package v1

import (
	"encoding/json"
	"testing"
)

// Round-trip and fail-closed pins for ContextFabricSubjectResolution.CommitDecisionDigests
// (CHAOS-4087), mirroring context_fabric_graph_not_projected_test.go's exact
// shape -- an additive-optional v1 addition to the same contract unit, and
// a contract field with no round-trip pin is exactly where an omitempty or
// replay regression hides.

func subjectRefForDigestTest(canonicalID string) ContextFabricSubjectRef {
	return ContextFabricSubjectRef{Kind: ContextFabricSubjectRepository, CanonicalID: canonicalID, Label: "l"}
}

func resolutionWithCommitDecisionDigests(digests []ContextFabricCommitDecisionDigest, committed []ContextFabricSubjectRef) ContextFabricSubjectResolution {
	return ContextFabricSubjectResolution{
		Candidates:            []ContextFabricSubjectCandidate{},
		Committed:             committed,
		CommitDecisionDigests: digests,
	}
}

func TestCHAOS4087_CommitDecisionDigestsRoundTripsBothWays(t *testing.T) {
	subject := subjectRefForDigestTest("repository:acme/widgets")
	digests := []ContextFabricCommitDecisionDigest{
		{Subject: subject, CommitGate: "lone_floor", IdentityProven: false, SearchTruncated: true, AliasLookupComplete: true},
	}
	resolution := resolutionWithCommitDecisionDigests(digests, []ContextFabricSubjectRef{subject})
	encoded, err := json.Marshal(resolution)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ContextFabricSubjectResolution
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.CommitDecisionDigests) != 1 {
		t.Fatalf("CommitDecisionDigests round-tripped %d entries, want 1", len(decoded.CommitDecisionDigests))
	}
	got := decoded.CommitDecisionDigests[0]
	if got.CommitGate != "lone_floor" || got.IdentityProven != false || got.SearchTruncated != true || got.AliasLookupComplete != true {
		t.Fatalf("digest round-tripped as %+v, want %+v", got, digests[0])
	}
	if got.Subject != subject {
		t.Fatalf("digest subject round-tripped as %+v, want %+v", got.Subject, subject)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("a resolution with a valid digest must validate: %v", err)
	}
}

// FAIL-CLOSED: a committed subject with NO digest recorded for it must
// still get an entry (CommitGate=="") -- never silently omitted -- and
// that entry must be distinguishable from a genuinely recorded-clean one.
// This is the exact regression team-lead's ruling asked to pin: an
// unrecorded digest reads as unrecorded, not as a clean pass.
func TestCHAOS4087_UnrecordedDigestIsDistinguishableFromRecordedClean(t *testing.T) {
	recordedSubject := subjectRefForDigestTest("repository:acme/widgets")
	unrecordedSubject := subjectRefForDigestTest("repository:acme/gadgets")
	resolution := resolutionWithCommitDecisionDigests(
		[]ContextFabricCommitDecisionDigest{
			{Subject: recordedSubject, CommitGate: "identity_fast_path", IdentityProven: true},
			{Subject: unrecordedSubject}, // zero value: CommitGate=="", the fail-closed "nothing recorded" reading.
		},
		[]ContextFabricSubjectRef{recordedSubject, unrecordedSubject},
	)
	if err := resolution.Validate(); err != nil {
		t.Fatalf("a resolution with one recorded and one unrecorded digest must validate: %v", err)
	}
	var recorded, unrecorded ContextFabricCommitDecisionDigest
	for _, d := range resolution.CommitDecisionDigests {
		switch d.Subject.CanonicalID {
		case recordedSubject.CanonicalID:
			recorded = d
		case unrecordedSubject.CanonicalID:
			unrecorded = d
		}
	}
	if recorded.CommitGate == "" {
		t.Fatal("the recorded subject's digest must NOT read as unrecorded")
	}
	if !recorded.IdentityProven {
		t.Fatal("the recorded subject's digest must carry its own IdentityProven verdict")
	}
	if unrecorded.CommitGate != "" {
		t.Fatalf("the unrecorded subject's digest must read as unrecorded (CommitGate==\"\"), got %q", unrecorded.CommitGate)
	}
	// The unrecorded entry's OTHER fields must also stay at their honest
	// zero value -- IdentityProven=true on an unrecorded digest would be
	// exactly the "recorded and clean" false reading this test exists to
	// rule out.
	if unrecorded.IdentityProven || unrecorded.SearchTruncated || unrecorded.AliasLookupComplete {
		t.Fatalf("an unrecorded digest must carry every field at its zero value, got %+v", unrecorded)
	}
}

// A resolution's digest count must match its committed count exactly (one
// entry per committed subject, self-identifying via Subject) -- a
// mismatch is a real defect (engine.go's own stamping invariant), not a
// forward-compat gap to silently tolerate.
func TestCHAOS4087_DigestCountMustMatchCommittedCount(t *testing.T) {
	subject := subjectRefForDigestTest("repository:acme/widgets")
	resolution := resolutionWithCommitDecisionDigests(
		[]ContextFabricCommitDecisionDigest{{Subject: subject, CommitGate: "exact_index"}},
		[]ContextFabricSubjectRef{subject, subjectRefForDigestTest("repository:acme/gadgets")},
	)
	if err := resolution.Validate(); err == nil {
		t.Fatal("a digest count that does not match the committed count must fail validation")
	}
}

// A digest naming a subject NOT in Committed at all is a phantom entry --
// must also fail validation, the same "no stale/orphaned proof" concern
// CommitBasisSet.ResetTo's own doc comment describes for the internal set.
func TestCHAOS4087_DigestForUncommittedSubjectIsRejected(t *testing.T) {
	committedSubject := subjectRefForDigestTest("repository:acme/widgets")
	phantomSubject := subjectRefForDigestTest("repository:acme/phantom")
	resolution := resolutionWithCommitDecisionDigests(
		[]ContextFabricCommitDecisionDigest{{Subject: phantomSubject, CommitGate: "exact_index"}},
		[]ContextFabricSubjectRef{committedSubject},
	)
	if err := resolution.Validate(); err == nil {
		t.Fatal("a digest naming a subject not in Committed must fail validation")
	}
}

// nil (absent) must round-trip as absent, not as an empty array -- the
// same omitempty-immutability concern GraphNotProjected's own test
// documents (CHAOS-3782 answer reuse keys on stored bytes).
func TestCHAOS4087_AbsentDigestsOmitTheFieldEntirely(t *testing.T) {
	resolution := resolutionWithCommitDecisionDigests(nil, []ContextFabricSubjectRef{})
	encoded, err := json.Marshal(resolution)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["commit_decision_digests"]; present {
		t.Fatalf("a resolution with nil digests must omit commit_decision_digests from the wire form, got %s", encoded)
	}
}

// A resolution persisted before CHAOS-4087 has no such key and must still
// decode and validate on replay.
func TestCHAOS4087_PreCHAOS4087SnapshotDecodesAndValidates(t *testing.T) {
	legacy := `{"candidates":[],"committed":[]}`
	var replayed ContextFabricSubjectResolution
	if err := json.Unmarshal([]byte(legacy), &replayed); err != nil {
		t.Fatalf("a pre-CHAOS-4087 resolution must decode: %v", err)
	}
	if replayed.CommitDecisionDigests != nil {
		t.Fatal("an absent commit_decision_digests must decode as nil")
	}
	if err := replayed.Validate(); err != nil {
		t.Fatalf("a pre-CHAOS-4087 resolution must still validate: %v", err)
	}
}

// CommitGate is a closed vocabulary (validCommitGate) -- an unrecognized
// value must be rejected, not silently accepted as a new, undocumented
// gate name.
func TestCHAOS4087_UnrecognizedCommitGateIsRejected(t *testing.T) {
	subject := subjectRefForDigestTest("repository:acme/widgets")
	resolution := resolutionWithCommitDecisionDigests(
		[]ContextFabricCommitDecisionDigest{{Subject: subject, CommitGate: "not_a_real_gate"}},
		[]ContextFabricSubjectRef{subject},
	)
	if err := resolution.Validate(); err == nil {
		t.Fatal("an unrecognized commit_gate value must fail validation")
	}
}

// codex R1 (CHAOS-4087) finding 1: two digests naming the SAME committed
// subject, with a second committed subject carrying none, still passed the
// old count-and-membership-only check (len matches, both digest subjects
// are IN Committed) -- silently reading the second subject as
// fail-closed-unrecorded instead of surfacing the real defect. A digest
// list must be a true bijection onto Committed: one entry each, never a
// duplicate standing in for a missing one.
func TestCHAOS4087_DuplicateDigestSubjectIsRejectedEvenWhenCountMatches(t *testing.T) {
	recorded := subjectRefForDigestTest("repository:acme/widgets")
	starved := subjectRefForDigestTest("repository:acme/gadgets")
	resolution := resolutionWithCommitDecisionDigests(
		[]ContextFabricCommitDecisionDigest{
			{Subject: recorded, CommitGate: "lone_floor"},
			{Subject: recorded, CommitGate: "lone_floor"}, // duplicate: stands in for `starved`'s missing entry
		},
		[]ContextFabricSubjectRef{recorded, starved},
	)
	if err := resolution.Validate(); err == nil {
		t.Fatal("two digests naming the same committed subject, with count matching Committed by coincidence, must fail validation")
	}
}

// codex R1 (CHAOS-4087) finding 2: CommitGate alone did not constrain
// IdentityProven, so a statistical gate like lone_floor (always
// CommitBasisStatistical -- see resolution.go:746-750) could carry
// IdentityProven=true, and a proof gate like identity_fast_path (always
// CommitBasisAuthoritativeIdentity) could carry IdentityProven=false -- a
// consumer trusting the boolean would misclassify a score comparison as a
// proven identity or vice versa.
func TestCHAOS4087_IdentityProvenContradictingItsCommitGateIsRejected(t *testing.T) {
	cases := []struct {
		name           string
		gate           string
		identityProven bool
	}{
		{"lone_floor claiming proven", "lone_floor", true},
		{"exact_index claiming proven", "exact_index", true},
		{"top_of_two claiming proven", "top_of_two", true},
		{"vector_margin_rescue claiming proven", "vector_margin_rescue", true},
		{"evidence_census claiming proven", "evidence_census", true},
		{"identity_fast_path claiming unproven", "identity_fast_path", false},
		{"pre_committed_exact_hint claiming unproven", "pre_committed_exact_hint", false},
		{"unrecorded claiming proven", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subject := subjectRefForDigestTest("repository:acme/widgets")
			resolution := resolutionWithCommitDecisionDigests(
				[]ContextFabricCommitDecisionDigest{{Subject: subject, CommitGate: tc.gate, IdentityProven: tc.identityProven}},
				[]ContextFabricSubjectRef{subject},
			)
			if err := resolution.Validate(); err == nil {
				t.Fatalf("gate %q with identity_proven=%v contradicts what that gate can ever produce and must fail validation", tc.gate, tc.identityProven)
			}
		})
	}
}

// caller_hint_short_circuit is the ONE gate that legitimately produces
// EITHER IdentityProven value at the same call site (a caller-explicit
// hint vs. a receipt-derived rider -- see
// TestChaos4085_ExactHintShortCircuitRecordsBasisPerClass in graphrank).
// Both must still validate.
func TestCHAOS4087_CallerHintShortCircuitAllowsBothIdentityProvenValues(t *testing.T) {
	for _, proven := range []bool{true, false} {
		subject := subjectRefForDigestTest("repository:acme/widgets")
		resolution := resolutionWithCommitDecisionDigests(
			[]ContextFabricCommitDecisionDigest{{Subject: subject, CommitGate: "caller_hint_short_circuit", IdentityProven: proven}},
			[]ContextFabricSubjectRef{subject},
		)
		if err := resolution.Validate(); err != nil {
			t.Fatalf("caller_hint_short_circuit with identity_proven=%v must validate: %v", proven, err)
		}
	}
}
