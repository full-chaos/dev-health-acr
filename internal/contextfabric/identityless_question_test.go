package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A QUESTION WITH NO IDENTITY, SWEPT ACROSS EVERY SEAM THAT KEYS ON ONE.
//
// CanonicalizeQuestion strips trailing terminal punctuation, so "?", "!!" and
// "..." all reduce to "" and share ONE hash. Any seam that keys or compares on
// that hash therefore treats unrelated questions as the same one unless it
// fails closed.
//
// THE SWEEP THAT PRODUCED THIS FILE was keyed by OPERATION -- "every site that
// compares or keys on a question hash or canonical question" -- rather than by
// any role noun, and it was run because the containment fix had just been
// found to miss exactly this class at ONE seam. It found a second live one.
//
//	answer reuse, lookup + save   already fails closed (its own earlier review)
//	carry containment, choke point now fails closed
//	structure priors, READ        now fails closed -- was serving priors curated
//	                              from "?" to a request asking "!!" (measured)
//	structure priors, WRITE x2    now fails closed, so the collision has no
//	                              material rather than merely being unreadable
//
// Each arm below drives the production seam, not the predicate.

// identitylessProbeConsultant records what hash the store was asked for, so an
// arm can prove the lookup was REFUSED rather than merely unproductive.
type identitylessProbeConsultant struct {
	consulted bool
	entries   []StructurePriorEntry
}

func (p *identitylessProbeConsultant) Consult(_ context.Context, _ string, questionHash string) ([]StructurePriorEntry, PriorDegradationState) {
	p.consulted = true
	var out []StructurePriorEntry
	for _, e := range p.entries {
		if e.QuestionHash == questionHash {
			out = append(out, e)
		}
	}
	return out, PriorDegradationNone
}

func TestIdentitylessQuestion_PriorsAreNeitherServedNorConsulted(t *testing.T) {
	t.Parallel()

	// PREMISE: the two questions genuinely collide, or the arm tests nothing.
	if QuestionHash("?") != QuestionHash("!!") {
		t.Fatalf("premise gone: %q and %q no longer share a hash", "?", "!!")
	}

	pc := &identitylessProbeConsultant{entries: []StructurePriorEntry{
		{QuestionHash: QuestionHash("?"), Member: contractsv1.ContextFabricStructureNeedExpectedKind, Value: "team"},
	}}
	engine := &Engine{priorConsultant: pc}

	got := engine.fetchPriorEntries(context.Background(), acceptancePrincipal(), QuestionHash("!!"))
	if len(got) > 0 {
		t.Errorf("served %d prior entr(ies) curated from %q to a request asking %q -- two unrelated questions that share a hash only because both canonicalize to the empty string", len(got), "?", "!!")
	}
	// REFUSED, not merely unproductive. Without this the arm would also pass
	// against a store that was consulted and happened to return nothing,
	// which is a different property from the one being claimed.
	if pc.consulted {
		t.Errorf("the priors store was consulted for the identityless hash: the lookup must be refused before it is issued, so a store holding such rows cannot serve them")
	}

	// CONTROL: a real question still consults and is still served, so the
	// guard cannot pass by disabling the mechanism.
	pc2 := &identitylessProbeConsultant{entries: []StructurePriorEntry{
		{QuestionHash: QuestionHash("which projects are at risk?"), Member: contractsv1.ContextFabricStructureNeedExpectedKind, Value: "project"},
	}}
	engine2 := &Engine{priorConsultant: pc2}
	if served := engine2.fetchPriorEntries(context.Background(), acceptancePrincipal(), QuestionHash("which projects are at risk?")); len(served) != 1 {
		t.Errorf("a REAL question was served %d prior entries, want 1: the guard has disabled the priors path rather than narrowing it", len(served))
	}
	if !pc2.consulted {
		t.Error("the priors store was never consulted for a real question")
	}
}

// recordingStructureSelectionSink captures what the production emit passed it.
type recordingStructureSelectionSink struct{ events []StructureSelectionEvent }

func (s *recordingStructureSelectionSink) RecordSelection(_ context.Context, event StructureSelectionEvent) {
	s.events = append(s.events, event)
}

func TestIdentitylessQuestion_SelectionsAreNeverCaptured(t *testing.T) {
	t.Parallel()

	sink := &recordingStructureSelectionSink{}
	engine := &Engine{structureSelectionSink: sink}

	canon := requestStructureCanonicalization{
		Confirmed: []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedExpectedKind}},
		PendingSelections: []StructureSelectionEvent{
			{QuestionHash: QuestionHash("?"), Member: string(contractsv1.ContextFabricStructureNeedExpectedKind)},
			{QuestionHash: QuestionHash("which projects are at risk?"), Member: string(contractsv1.ContextFabricStructureNeedExpectedKind)},
		},
	}
	engine.recordStructureConfirmationOutcome(context.Background(), acceptancePrincipal(), validInvestigationRequest(), canon)

	// The real-question event must survive; only the identityless one is
	// dropped. Asserting BOTH directions is what stops this passing against a
	// guard that simply stopped recording anything.
	if len(sink.events) != 1 {
		t.Fatalf("captured %d selection event(s), want exactly 1 -- the real question's; events: %#v", len(sink.events), sink.events)
	}
	if IdentitylessQuestionHash(sink.events[0].QuestionHash) {
		t.Error("captured a selection under the identityless hash: curation turns these rows into priors, so writing one is what lets an unrelated punctuation-only question inherit this selection")
	}
}

// TestIdentitylessQuestionHash_IsTheEmptyCanonicalHash pins the predicate
// itself against the family it must recognise, and against one it must not.
func TestIdentitylessQuestionHash_IsTheEmptyCanonicalHash(t *testing.T) {
	t.Parallel()

	for _, q := range []string{"?", "!!", "...", " ? ! ", "", ".,;:"} {
		if !IdentitylessQuestionHash(QuestionHash(q)) {
			t.Errorf("IdentitylessQuestionHash(QuestionHash(%q)) = false, want true -- it canonicalizes to %q", q, CanonicalizeQuestion(q))
		}
	}
	// A question that merely ENDS in punctuation keeps its identity, and a
	// guard that refused it would silently disable both mechanisms.
	for _, q := range []string{"which projects are at risk?", "C#", "done!"} {
		if IdentitylessQuestionHash(QuestionHash(q)) {
			t.Errorf("IdentitylessQuestionHash(QuestionHash(%q)) = true, want false -- it canonicalizes to %q, which is a real question", q, CanonicalizeQuestion(q))
		}
	}
}
