package graphrank

// THE OFFER IS THE OTHER HALF OF THE COMMIT.
//
// AC-3778-3 -- "a vector hit alone never commits a subject" -- was enforced at
// the commit gates and only there, and the existing pins for it
// (TestAC_3778_3_..., TestVectorOnlyGuardBlocksLoneCommitRegardlessOfConfidence)
// are all still true. They were true on the rig too, on the exact run where a
// vector-only candidate ended up committed with identity_proven, because the
// commit happened on the NEXT TURN through the caller:
//
//	t2  the vector-only candidate is OFFERED, with a receipt id
//	t3  the client answers with that receipt, the receipt's own label joins
//	    SubjectTerms, the engine exact-matches the label it had itself
//	    offered, and pre_committed_exact_hint commits it identity_proven
//
// So a guard that stops at the commit is a guard the offer walks around. These
// pins are the same invariant asserted one step earlier, at the point the
// candidate becomes answerable.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// vectorOfferCandidate builds a candidate in the shape the rig produced: a
// receipt id (so it would be answerable if offered), a real subject, and an
// explicit mechanism set.
func vectorOfferCandidate(id string, confidence float64, state contextfabric.ResolutionState, mechanisms ...contextfabric.MatchMechanism) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: "receipt_" + id + "_padding",
		Subject: contextfabric.SubjectRef{
			Kind: contextfabric.SubjectTeam, CanonicalID: id, Label: id,
		},
		State:           state,
		MatchReasons:    []string{"probe"},
		Confidence:      confidence,
		MatchMechanisms: mechanisms,
	}
}

func resolveOfferPool(candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	bySubject := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, candidate := range candidates {
		bySubject[SubjectKey(candidate.Subject)] = candidate
	}
	return ResolveFromMergedCandidates(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true)
}

func offeredIDs(resolution contextfabric.SubjectResolution) []string {
	ids := make([]string, 0, len(resolution.Candidates))
	for _, candidate := range resolution.Candidates {
		ids = append(ids, candidate.Subject.CanonicalID)
	}
	return ids
}

// THE HARM: the rig's t2. A question naming a subject that does not exist
// produced ONE candidate -- a real team, reached by embedding proximity alone
// -- and the engine offered it. Offering it is what let the next turn commit
// it, so an offer pool that admits a vector-only candidate is the substitution
// with one turn of delay.
func TestAVectorOnlyCandidateIsNeverOffered(t *testing.T) {
	t.Parallel()
	resolution := resolveOfferPool(
		vectorOfferCandidate("team_vector_only", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector),
	)
	if len(resolution.Candidates) != 0 {
		t.Fatalf("offered %v; a vector-only candidate must never be answerable -- this is the rig's t2, and answering it is what committed a substituted subject on t3", offeredIDs(resolution))
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("committed %d subject(s) on a vector-only pool, want 0", len(resolution.Committed))
	}
	// The prompt this leaves behind is the EMPTIED-POOL one, not a built
	// one. The concern the original assertion here encoded -- a prompt must
	// never name a choice absent from the machine-readable result -- is
	// preserved and is now structural rather than incidental: the constant
	// interpolates nothing, so it cannot name a withheld candidate even by
	// accident, which a built prompt could. The turn clarifies instead of
	// collapsing to no_match (team-lead ruling 04:21Z); see
	// TestAnOfferPoolEmptiedByTheExclusionStillClarifies for that claim and
	// TestAWithheldOfferPoolClarifiesInsteadOfNoMatch for the terminal.
	if resolution.ClarificationPrompt != contextfabric.OfferPoolEmptiedClarificationPrompt {
		t.Fatalf("clarification prompt = %q, want the emptied-pool constant", resolution.ClarificationPrompt)
	}
	for _, candidate := range []string{"team_vector_only"} {
		if strings.Contains(resolution.ClarificationPrompt, candidate) {
			t.Fatalf("the prompt names %q, a candidate the caller cannot see in the result", candidate)
		}
	}
}

// THE OTHER HALF, and it is the one an offer-only fix would miss. A candidate
// that ARRIVES already committed came from the caller -- in production, from a
// prior receipt -- and dropping it silently would answer a different question
// than the one asked. It is DEMOTED instead: it may still clarify, and it can
// no longer reach pre_committed_exact_hint, which is the gate that stamped
// identity_proven on a vector guess.
func TestAPreCommittedVectorOnlyCandidateCannotCommit(t *testing.T) {
	t.Parallel()
	resolution := resolveOfferPool(
		vectorOfferCandidate("team_receipt_named", 1, contextfabric.ResolutionCommitted, contextfabric.MatchVector),
	)
	if len(resolution.Committed) != 0 {
		t.Fatalf("committed %v through the pre-committed arrival path on vector-only evidence; this is precisely the pre_committed_exact_hint laundering", resolution.Committed)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.State == contextfabric.ResolutionCommitted {
			t.Fatalf("candidate %q is still Committed after demotion", candidate.Subject.CanonicalID)
		}
	}
	// And it is not offered back either: re-offering the receipt the caller
	// just answered hands the same guess round again under a new id.
	if len(resolution.Candidates) != 0 {
		t.Fatalf("offered %v after demotion; the caller already answered this receipt and the answer was refused", offeredIDs(resolution))
	}
}

// A DEMOTED candidate must still COMPETE, even though it is never offered.
// This is the distinction an existing pin caught the hard way: the first
// version of this change removed vector-only candidates before the commit
// decision, which left a corroborated rival UNOPPOSED and committed it at
// lone_floor -- a fixture that had committed nothing for the life of
// CHAOS-3829 started committing. Removing a candidate removes what the
// others were competing against, and this seam exists to make the engine
// commit LESS on a guess, never more.
func TestAVectorOnlyCandidateStillOpposesTheOthers(t *testing.T) {
	t.Parallel()
	// A corroborated candidate that would clear LoneFloor unopposed, and a
	// vector-only rival at a confidence that keeps the pair ambiguous.
	resolution := resolveOfferPool(
		vectorOfferCandidate("team_corroborated", 0.62, contextfabric.ResolutionProposed, contextfabric.MatchVector, contextfabric.MatchLexical),
		vectorOfferCandidate("team_vector_only", 0.62, contextfabric.ResolutionProposed, contextfabric.MatchVector),
	)
	if len(resolution.Committed) != 0 {
		t.Fatalf("committed %v; excluding the vector-only rival from the OFFER must not remove it from the CONTEST -- that turns an ambiguous pool into a lone-candidate commit", resolution.Committed)
	}
	if offered := offeredIDs(resolution); len(offered) != 1 || offered[0] != "team_corroborated" {
		t.Fatalf("offered %v, want exactly [team_corroborated]", offered)
	}
}

// A vector hit that CORROBORATES is untouched. This is AC-3778-2's whole
// lift, and a fix that killed it would trade one regression for another; the
// pin exists so "exclude vector" can never quietly become "exclude the vector
// mechanism wherever it appears".
func TestACorroboratedVectorCandidateIsStillOffered(t *testing.T) {
	t.Parallel()
	resolution := resolveOfferPool(
		vectorOfferCandidate("team_corroborated", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector, contextfabric.MatchLexical),
		vectorOfferCandidate("team_rival", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchLexical),
	)
	offered := offeredIDs(resolution)
	if len(offered) != 2 {
		t.Fatalf("offered %v, want both -- a subject found by vector AND lexical is corroborated, not a guess", offered)
	}
}

// A pool holding BOTH kinds keeps the real candidate and drops the guess. The
// two-candidate shape is what discriminates a working exclusion from a fix
// that simply empties the pool whenever any vector mechanism is present.
func TestAVectorOnlyCandidateIsExcludedWhileARealOneSurvives(t *testing.T) {
	t.Parallel()
	resolution := resolveOfferPool(
		vectorOfferCandidate("team_vector_only", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector),
		vectorOfferCandidate("team_exact", 1, contextfabric.ResolutionProposed, contextfabric.MatchExact),
	)
	offered := offeredIDs(resolution)
	if len(offered) != 1 || offered[0] != "team_exact" {
		t.Fatalf("offered %v, want exactly [team_exact]", offered)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "team_exact" {
		t.Fatalf("committed %v, want the exact-matched subject -- the exclusion must not disturb a real commit", resolution.Committed)
	}
}

// THE POSITIVE CONTROL. Without it every assertion above could be satisfied by
// a build that offers nothing at all, and "the pool was empty" would read as
// "the exclusion worked".
func TestAnOrdinaryCandidatePoolIsStillOffered(t *testing.T) {
	t.Parallel()
	resolution := resolveOfferPool(
		vectorOfferCandidate("team_lexical_one", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchLexical),
		vectorOfferCandidate("team_lexical_two", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchLexical),
	)
	if len(resolution.Candidates) != 2 {
		t.Fatalf("offered %v, want both lexical candidates -- if this is empty, every exclusion pin in this file is vacuous", offeredIDs(resolution))
	}
}

// offerPoolTracer records every offer_pool event so the summary can be checked
// against the per-candidate lines it claims to fold.
type offerPoolTracer struct {
	perCandidate []string
	summaries    []ResolutionTraceEvent
}

func (r *offerPoolTracer) Trace(event ResolutionTraceEvent) {
	if event.Stage != "offer_pool" {
		return
	}
	if event.OfferPoolSummary {
		r.summaries = append(r.summaries, event)
		return
	}
	r.perCandidate = append(r.perCandidate, event.OfferPoolDisposition)
}

func resolveOfferPoolTraced(tracer ResolutionTracer, candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	bySubject := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, candidate := range candidates {
		bySubject[SubjectKey(candidate.Subject)] = candidate
	}
	resolution, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(
		bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true,
		DefaultCommitGatePolicy(), nil, nil, false, tracer, "req_offer_pool_identity_0000000000", "", false, false, nil,
	)
	return resolution
}

// THE IDENTITY, asserted on EVERY arm rather than the counts alone. A per-arm
// assertion can pass on a half-done fix -- a build that excluded correctly but
// stopped counting demotions would satisfy every "excluded == N" check in this
// file. Only `excluded + demoted == the number of candidates the seam acted
// on` goes red for that, and only the summary-versus-per-candidate comparison
// catches a summary that drifts from what it claims to fold.
func TestTheOfferPoolSummaryEqualsWhatItFolded(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name         string
		candidates   []contextfabric.SubjectCandidate
		wantExcluded int
		wantDemoted  int
	}{
		{
			name:       "nothing to act on",
			candidates: []contextfabric.SubjectCandidate{vectorOfferCandidate("team_exact", 1, contextfabric.ResolutionProposed, contextfabric.MatchExact)},
		},
		{
			name:         "one guess excluded",
			candidates:   []contextfabric.SubjectCandidate{vectorOfferCandidate("team_vector_only", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector)},
			wantExcluded: 1,
		},
		{
			name:        "one caller-named guess demoted",
			candidates:  []contextfabric.SubjectCandidate{vectorOfferCandidate("team_receipt_named", 1, contextfabric.ResolutionCommitted, contextfabric.MatchVector)},
			wantDemoted: 1,
		},
		{
			name: "both dispositions in one call",
			candidates: []contextfabric.SubjectCandidate{
				vectorOfferCandidate("team_vector_only", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector),
				vectorOfferCandidate("team_receipt_named", 1, contextfabric.ResolutionCommitted, contextfabric.MatchVector),
				vectorOfferCandidate("team_exact", 1, contextfabric.ResolutionProposed, contextfabric.MatchExact),
			},
			wantExcluded: 1, wantDemoted: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			tracer := &offerPoolTracer{}
			resolveOfferPoolTraced(tracer, testCase.candidates...)

			if len(tracer.summaries) != 1 {
				t.Fatalf("emitted %d offer_pool summaries, want exactly 1 -- the summary fires on every call including the ones that acted on nothing", len(tracer.summaries))
			}
			summary := tracer.summaries[0]
			if summary.OfferPoolVectorOnlyExcluded != testCase.wantExcluded || summary.OfferPoolVectorOnlyDemoted != testCase.wantDemoted {
				t.Fatalf("summary excluded/demoted = %d/%d, want %d/%d", summary.OfferPoolVectorOnlyExcluded, summary.OfferPoolVectorOnlyDemoted, testCase.wantExcluded, testCase.wantDemoted)
			}
			// The identity: the folded total must equal the population it
			// folded, per disposition, not merely in aggregate -- a swap
			// between the two counters sums correctly and is still wrong.
			var excluded, demoted int
			for _, disposition := range tracer.perCandidate {
				switch disposition {
				case "vector_only_excluded":
					excluded++
				case "vector_only_demoted":
					demoted++
				default:
					t.Fatalf("per-candidate offer_pool event carries disposition %q, outside the closed vocabulary", disposition)
				}
			}
			if excluded != summary.OfferPoolVectorOnlyExcluded || demoted != summary.OfferPoolVectorOnlyDemoted {
				t.Fatalf("summary %d/%d disagrees with the %d/%d per-candidate events it folded", summary.OfferPoolVectorOnlyExcluded, summary.OfferPoolVectorOnlyDemoted, excluded, demoted)
			}
			if excluded+demoted != len(tracer.perCandidate) {
				t.Fatalf("the two dispositions sum to %d over %d events; a candidate the seam acted on is unaccounted for", excluded+demoted, len(tracer.perCandidate))
			}
		})
	}
}

// AN OFFER POOL EMPTIED BY THE EXCLUSION IS NOT AN EMPTY GRAPH.
//
// This is the one regression the rig arm found, and the shape is exactly the
// row that lost it: resolution AMBIGUOUS, every offerable candidate withheld
// because it was vector-only, and the caller handed an empty candidate list
// that the layer above then read as `no_match` -- the same terminal a
// genuinely empty graph produces. The conversation ended one turn before the
// engine would have offered the real exact-matched candidates.
//
// The resolution's job here is to make the two empties DISTINGUISHABLE. It
// does that by carrying a prompt beside zero candidates, a pairing nothing
// else in this package produces.
func TestAnOfferPoolEmptiedByTheExclusionStillClarifies(t *testing.T) {
	t.Parallel()
	// Two vector-only candidates: ambiguous by construction, and both
	// withheld. Two rather than one so the pool is genuinely ambiguous
	// rather than a lone candidate that merely missed its gate.
	resolution := resolveOfferPool(
		vectorOfferCandidate("team_guess_one", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector),
		vectorOfferCandidate("team_guess_two", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector),
	)
	if len(resolution.Candidates) != 0 {
		t.Fatalf("offered %v, want none -- the exclusion must still withhold them", offeredIDs(resolution))
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("committed %v, want none", resolution.Committed)
	}
	if resolution.ClarificationPrompt != contextfabric.OfferPoolEmptiedClarificationPrompt {
		t.Fatalf("clarification prompt = %q, want the emptied-pool prompt -- without it the layer above cannot tell a withheld pool from an absent one, and the turn collapses to no_match", resolution.ClarificationPrompt)
	}
}

// THE CONTROL, and the whole change turns on it: a graph that genuinely
// found nothing must keep its `no_match`. If this ever carries a prompt the
// fix has stopped discriminating and has simply turned every empty
// resolution into a clarification.
func TestAGenuinelyEmptyPoolCarriesNoPrompt(t *testing.T) {
	t.Parallel()
	resolution := resolveOfferPool()
	if len(resolution.Candidates) != 0 {
		t.Fatalf("offered %v on an empty input, want none", offeredIDs(resolution))
	}
	if resolution.ClarificationPrompt != "" {
		t.Fatalf("clarification prompt = %q on a pool that never had a candidate; the emptied-by-exclusion signal must not fire when nothing was excluded", resolution.ClarificationPrompt)
	}
}

// THE FLAG REQUIRES SOMETHING TO HAVE BEEN WITHHELD. This is what replaced
// the `ambiguous` conjunct after a mutation deleting that conjunct turned
// nothing red: the conjunct was unprovable (a committed candidate is never
// vector-only, so it is never withheld, so it is always offered, so an empty
// offered list already implies nothing committed), and an unprovable clause
// is not a guard. This holds the condition at what a fixture CAN distinguish.
func TestTheFlagNeedsSomethingWithheld(t *testing.T) {
	t.Parallel()
	tracer := &offerPoolTracer{}
	// An empty input: nothing offered, and nothing withheld either.
	resolveOfferPoolTraced(tracer)
	if len(tracer.summaries) != 1 {
		t.Fatalf("emitted %d offer_pool summaries on an empty pool, want 1", len(tracer.summaries))
	}
	if tracer.summaries[0].OfferPoolEmptiedByExclusion {
		t.Fatal("the flag fired on a pool that never held a candidate; it would make every empty graph a clarification")
	}
}

// A prompt built from an EMPTY candidate list is prose no caller can act on.
// This is the pinned half of the guard at resolve.go's rebuild site, whose
// own reachability no fixture in this repo constructs (see that site's
// reported limit): even if the rebuild ever ran on an empty list, it degrades
// to no prompt -- and so to the ordinary no_match terminal -- rather than
// asking the user "Which subject did you mean: ?".
func TestAPromptIsNeverBuiltFromAnEmptyCandidateList(t *testing.T) {
	t.Parallel()
	if got := ClarificationPrompt(nil); got != "" {
		t.Fatalf("ClarificationPrompt(nil) = %q, want empty", got)
	}
	if got := ClarificationPrompt([]contextfabric.SubjectCandidate{}); got != "" {
		t.Fatalf("ClarificationPrompt(empty) = %q, want empty", got)
	}
	// The positive control: with candidates it still asks the question.
	got := ClarificationPrompt([]contextfabric.SubjectCandidate{
		vectorOfferCandidate("team_one", 1, contextfabric.ResolutionProposed, contextfabric.MatchExact),
	})
	if got == "" {
		t.Fatal("ClarificationPrompt built nothing from a real candidate; the empty-list guard has swallowed the ordinary case")
	}
}

// A pool emptied by TRUNCATION rather than by the exclusion is also not this
// case. Without this arm the flag could be "the offered list is empty and
// something happened", which would fire on states the rig never produced.
func TestAnUnambiguousEmptyPoolCarriesNoPrompt(t *testing.T) {
	t.Parallel()
	// One exact candidate: it COMMITS, so the resolution is not ambiguous,
	// and the emptied-by-exclusion condition must not fire even though the
	// sibling vector-only candidate was excluded.
	resolution := resolveOfferPool(
		vectorOfferCandidate("team_exact", 1, contextfabric.ResolutionProposed, contextfabric.MatchExact),
		vectorOfferCandidate("team_guess", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector),
	)
	if len(resolution.Committed) != 1 {
		t.Fatalf("committed %v, want the exact-matched subject", resolution.Committed)
	}
	if resolution.ClarificationPrompt == contextfabric.OfferPoolEmptiedClarificationPrompt {
		t.Fatal("a resolution that COMMITTED carries the emptied-pool prompt; the flag must require ambiguity")
	}
}

// The flag reaches the folded Info line, on both of its values, and agrees
// with the pool counters it sits beside. A flag that only ever appears true
// cannot be told apart from a build that hardcodes it.
func TestTheEmptiedByExclusionFlagIsExplicitOnEveryPass(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		candidates []contextfabric.SubjectCandidate
		want       bool
	}{
		{
			name: "emptied by the exclusion",
			candidates: []contextfabric.SubjectCandidate{
				vectorOfferCandidate("team_guess_one", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector),
				vectorOfferCandidate("team_guess_two", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector),
			},
			want: true,
		},
		{
			name:       "genuinely empty",
			candidates: nil,
			want:       false,
		},
		{
			name: "ambiguous but still offerable",
			candidates: []contextfabric.SubjectCandidate{
				vectorOfferCandidate("team_lexical_one", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchLexical),
				vectorOfferCandidate("team_lexical_two", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchLexical),
			},
			want: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			tracer := &offerPoolTracer{}
			resolveOfferPoolTraced(tracer, testCase.candidates...)
			if len(tracer.summaries) != 1 {
				t.Fatalf("emitted %d offer_pool summaries, want exactly 1 on every pass", len(tracer.summaries))
			}
			got := tracer.summaries[0].OfferPoolEmptiedByExclusion
			if got != testCase.want {
				t.Fatalf("offer_pool_emptied_by_exclusion = %t, want %t", got, testCase.want)
			}
			// The identity that keeps the flag honest: it may only be true
			// when the counters say something was actually withheld.
			s := tracer.summaries[0]
			withheld := s.OfferPoolVectorOnlyExcluded + s.OfferPoolVectorOnlyDemoted
			if got && withheld == 0 {
				t.Fatalf("the flag is true while the counters report %d withheld candidates; it would fire on an empty graph", withheld)
			}
		})
	}
}

// decisionSummaryCapture keeps the FOLDED decision_summary event -- the line an
// operator actually reads -- as opposed to the per-stage events the fold
// consumes on its way there.
type decisionSummaryCapture struct {
	summaries []ResolutionTraceEvent
	// offerPool keeps the per-resolution offer_pool summaries the fold
	// consumes, so the folded line can be checked against WHAT IT FOLDED
	// rather than against a constant this test also wrote. A pin that
	// asserts two independent literals cannot catch a fold that drifts from
	// its own source; an identity can.
	offerPool []ResolutionTraceEvent
}

func (c *decisionSummaryCapture) Trace(event ResolutionTraceEvent) {
	switch {
	case event.Stage == "decision_summary":
		c.summaries = append(c.summaries, event)
	case event.Stage == "offer_pool" && event.OfferPoolSummary:
		c.offerPool = append(c.offerPool, event)
	}
}

// THE FOLDED LINE, NOT THE EVENTS THAT FEED IT.
//
// A hosted mutation battery found five survivors on this seam and FOUR were
// one class: every pin in this file read the offer_pool SUMMARY EVENT, and
// nothing drove a real resolution and asserted what the decision_summary ends
// up CARRYING. So the fold could drop the emptied flag, invert its own
// offer_pool branch, or let the frame-gate tripwire report `passed`
// unconditionally, and the whole suite stayed green -- on the observable this
// change exists to add.
//
// Every local mutant that killed before that run was one I had already written
// a pin for. The battery picked the ones I had not thought of. This test is
// the answer to that, and it goes through ResolveSubjectsWithCommitBasis --
// the production entry point that INSTALLS the fold -- rather than through
// the buffer, because a test that constructs the buffer itself proves
// formatting and would have survived all four.
func TestTheFoldedDecisionSummaryCarriesTheSeamsOwnValues(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name            string
		frame           *contextfabric.QuestionFrame
		mechanism       contextfabric.MatchMechanism
		labels          []string
		wantGate        string
		wantRefuseBasis string
		wantExcluded    int
		wantEmptied     bool
		wantCommitted   int
	}{
		{
			name:      "a passing frame whose pool was emptied by the exclusion",
			frame:     passingCohortFrame(),
			mechanism: contextfabric.MatchVector, labels: []string{"guess one", "guess two"},
			wantGate: "passed", wantRefuseBasis: "none", wantExcluded: 2, wantEmptied: true,
		},
		{
			name:      "a passing frame with nothing withheld",
			frame:     passingCohortFrame(),
			mechanism: contextfabric.MatchLexical, labels: []string{"probe"},
			wantGate: "passed", wantRefuseBasis: "none", wantExcluded: 0, wantEmptied: false,
		},
		{
			// THE TRIPWIRE ARM. The engine refuses this frame above
			// retrieval, so in a correct build resolution never runs on
			// one -- which is exactly why the value must be asserted: if
			// the enforcement is ever weakened, THIS is the line that
			// says so, and an unconditional `passed` would hide it.
			name:      "a frame the gate refuses reaches the line as refused",
			frame:     unservableCohortFrame(),
			mechanism: contextfabric.MatchExact, labels: []string{"probe"},
			wantGate: "refused:member_kind_unservable", wantRefuseBasis: "member_kind_unservable",
			wantExcluded: 0, wantEmptied: false, wantCommitted: 1,
		},
		{
			// THE TRIPWIRE'S OTHER BRANCH. A hosted battery arm blanked
			// the rejected-invariant path and SURVIVED: the arm above
			// exercises `refused:<basis>` and nothing exercised
			// `rejected:<invariant>`, so the tripwire could report a
			// phase-A1-invalid frame as `passed` with the suite green.
			// Reaching it means calling graphrank with a frame the engine
			// would have refused -- which is the bypass the tripwire is
			// for, so simulating it here is the only way to assert it.
			name:      "a frame that fails an invariant reaches the line as rejected",
			frame:     invalidCohortFrame(),
			mechanism: contextfabric.MatchExact, labels: []string{"probe"},
			wantGate: "rejected:i6", wantRefuseBasis: "none",
			wantExcluded: 0, wantEmptied: false, wantCommitted: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			capture := &decisionSummaryCapture{}
			resolveThroughTheProductionEntryPoint(t, capture, testCase.frame, testCase.mechanism, testCase.labels...)

			if len(capture.summaries) != 1 {
				t.Fatalf("captured %d decision_summary events, want exactly 1 -- the fold flushes once per call, including when it counted nothing", len(capture.summaries))
			}
			got := capture.summaries[0]
			if got.DecisionFrameGate != testCase.wantGate {
				t.Errorf("frame_gate on the FOLDED line = %q, want %q", got.DecisionFrameGate, testCase.wantGate)
			}
			if got.DecisionRefuseBasis != testCase.wantRefuseBasis {
				t.Errorf("refuse_basis on the FOLDED line = %q, want %q", got.DecisionRefuseBasis, testCase.wantRefuseBasis)
			}
			if got.OfferPoolVectorOnlyExcluded != testCase.wantExcluded {
				t.Errorf("offer_pool_vector_only_excluded on the FOLDED line = %d, want %d -- the fold must carry what the offer_pool summary reported", got.OfferPoolVectorOnlyExcluded, testCase.wantExcluded)
			}
			if got.OfferPoolEmptiedByExclusion != testCase.wantEmptied {
				t.Errorf("offer_pool_emptied_by_exclusion on the FOLDED line = %t, want %t", got.OfferPoolEmptiedByExclusion, testCase.wantEmptied)
			}
			// THE IDENTITY: the folded line must equal the SUM of the
			// per-resolution offer_pool summaries it folded, per counter and
			// for the flag. Expected values above pin the behaviour; this
			// pins the FOLD, and only this goes red when the fold drifts
			// from its own source while both happen to agree with a literal.
			var sumExcluded, sumDemoted int
			var anyEmptied bool
			for _, e := range capture.offerPool {
				sumExcluded += e.OfferPoolVectorOnlyExcluded
				sumDemoted += e.OfferPoolVectorOnlyDemoted
				anyEmptied = anyEmptied || e.OfferPoolEmptiedByExclusion
			}
			if len(capture.offerPool) == 0 {
				t.Fatal("no offer_pool summary was emitted, so the identity below would compare the folded line against nothing")
			}
			if got.OfferPoolVectorOnlyExcluded != sumExcluded || got.OfferPoolVectorOnlyDemoted != sumDemoted {
				t.Errorf("folded counters %d/%d disagree with the %d/%d the offer_pool summaries reported", got.OfferPoolVectorOnlyExcluded, got.OfferPoolVectorOnlyDemoted, sumExcluded, sumDemoted)
			}
			if got.OfferPoolEmptiedByExclusion != anyEmptied {
				t.Errorf("folded emptied flag %t disagrees with the OR of what it folded (%t)", got.OfferPoolEmptiedByExclusion, anyEmptied)
			}
			if testCase.wantCommitted > 0 && got.DecisionCommittedCount != testCase.wantCommitted {
				t.Fatalf("committed_count on the FOLDED line = %d, want %d -- the tripwire arm is only meaningful BESIDE a real commit: a refusing verdict standing next to a non-zero committed_count is the bypass it exists to expose", got.DecisionCommittedCount, testCase.wantCommitted)
			}
		})
	}
}

// passingCohortFrame is a valid, servable cohort frame -- the gate passes it.
func passingCohortFrame() *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:       contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
		},
		Temporal:    contextfabric.TemporalIntentCurrent,
		Obligations: []contextfabric.AnswerObligation{contextfabric.ObligationState},
		Version:     contextfabric.QuestionFrameVersion,
	}
}

// invalidCohortFrame FAILS phase-A1 validation: grouped_members whose grouping
// axis equals its member kind. The engine refuses such a frame above
// retrieval, so in a correct build graphrank never sees one -- which is
// exactly why the tripwire's REJECTED branch needs its own fixture: it can
// only be exercised by calling graphrank directly, i.e. by simulating the
// bypass the tripwire exists to detect.
func invalidCohortFrame() *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:    contextfabric.SubjectExpressionGroupedMembers,
			Grouped: &contextfabric.GroupedSetExpression{GroupKind: contextfabric.SubjectTeam, MemberKind: contextfabric.SubjectTeam},
		},
		Temporal:    contextfabric.TemporalIntentCurrent,
		Obligations: []contextfabric.AnswerObligation{contextfabric.ObligationState},
		Version:     contextfabric.QuestionFrameVersion,
	}
}

// unservableCohortFrame declares a member kind no discovery arm serves. Valid,
// but the gate refuses it on the basis.
func unservableCohortFrame() *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:       contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectPullRequest},
		},
		Temporal:    contextfabric.TemporalIntentCurrent,
		Obligations: []contextfabric.AnswerObligation{contextfabric.ObligationState},
		Version:     contextfabric.QuestionFrameVersion,
	}
}

// resolveThroughTheProductionEntryPoint drives ResolveSubjectsWithCommitBasis,
// which is what INSTALLS the decision-summary fold. Going through the buffer
// directly would prove formatting and nothing about the wiring -- the exact
// gap the battery exposed.
//
// It reuses this package's own fakeGraphBackend rather than a hand-rolled
// ResolveDeps: a double that returns no nodes produces no candidates, and the
// test would then assert zeros for a reason that has nothing to do with the
// seam. The vector mechanism is stamped on the node so the candidates arrive
// vector-only and the exclusion has something real to withhold.
func resolveThroughTheProductionEntryPoint(t *testing.T, tracer ResolutionTracer, frame *contextfabric.QuestionFrame, mechanism contextfabric.MatchMechanism, labels ...string) {
	t.Helper()
	results := map[string][]CandidateNode{}
	for i, label := range labels {
		node := candidateNode(contextfabric.SubjectTeam, fmt.Sprintf("team_probe_%d", i), label, 0.5, "*")
		node.Mechanism = mechanism
		results["probe"] = append(results["probe"], node)
	}
	backend := &fakeGraphBackend{searchResults: results}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	request := contextfabric.InvestigationRequest{
		RequestID: "req_folded_summary_pin_000000000",
		Options:   contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, AllowClarification: true},
	}
	interpreted := contextfabric.InterpretedQuestion{SubjectTerms: []string{"probe"}}
	_, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"},
		request, interpreted, deps, nil, nil, frame, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
}

// THE PROMPT MUST NOT NAME A CANDIDATE THE CALLER CANNOT SEE.
//
// A battery arm rebuilt the clarification prompt from the UNFILTERED list and
// survived: every pin here asserted the prompt's TEXT when the pool was
// emptied, and none asserted its CONTENTS when the pool was mixed. So the
// prompt could go on naming withheld candidates -- handing back by name the
// very guesses the exclusion exists to withhold -- with the suite green.
func TestTheClarificationPromptNeverNamesAWithheldCandidate(t *testing.T) {
	t.Parallel()
	// A mixed pool: two lexical candidates that ARE offered, and one
	// vector-only guess that is not. Two offered candidates keep the
	// resolution ambiguous, so a prompt is genuinely built rather than
	// skipped.
	// The withheld candidate's DISPLAY LABEL differs from its canonical id.
	// A confirmation round found that the shared fixture set them equal, so
	// a regression leaking a candidate whose label differs from its id would
	// slip past a check written against the id -- and the prompt is built
	// from LABELS. The assertion below therefore checks the label.
	withheld := vectorOfferCandidate("team_withheld_guess", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector)
	withheld.Subject.Label = "Withheld Guess"
	resolution := resolveOfferPool(
		vectorOfferCandidate("team_lexical_one", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchLexical),
		vectorOfferCandidate("team_lexical_two", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchLexical),
		withheld,
	)
	if resolution.ClarificationPrompt == "" {
		t.Fatal("no clarification prompt was built on an ambiguous mixed pool; this test cannot see what it exists to check")
	}
	if offered := offeredIDs(resolution); len(offered) != 2 {
		t.Fatalf("offered %v, want the two lexical candidates -- the fixture must be mixed for this assertion to mean anything", offered)
	}
	for _, leaked := range []string{"Withheld Guess", "team_withheld_guess"} {
		if strings.Contains(resolution.ClarificationPrompt, leaked) {
			t.Fatalf("the prompt names a WITHHELD candidate (%q): %q -- it hands back by name the guess the exclusion withheld from the machine-readable result", leaked, resolution.ClarificationPrompt)
		}
	}
	// The positive half: it does name what it offered, so "names nothing"
	// cannot satisfy this test.
	if !strings.Contains(resolution.ClarificationPrompt, "team_lexical_one") {
		t.Fatalf("the prompt names none of the OFFERED candidates: %q", resolution.ClarificationPrompt)
	}
}

// THE FOLD ACCUMULATES ACROSS PASSES, which the four arms above cannot see.
//
// A confirmation round found this: the entry-point fixture reaches only ONE
// resolution, so the identity there compares the folded value with a single
// source summary -- the same thing the literal assertion beside it already
// checks. A fold changed from `+=` to overwrite, or one that dropped a later
// summary, passes every one of those arms. Production DOES emit a second
// offer_pool summary when the confirmed-kind or evidence-census re-decision
// paths run, so the accumulating behaviour is real and was unpinned.
//
// Driving those re-decision paths from the entry point needs a census
// dependency and a truncated backend; this instead feeds the fold the event
// sequence they produce, which is what the accumulation contract is ABOUT.
// The arms above keep the wiring honest; this one keeps the arithmetic honest.
func TestTheFoldAccumulatesEveryOfferPoolSummaryItSees(t *testing.T) {
	t.Parallel()
	sink := &decisionSummaryCapture{}
	fold := &decisionSummaryBuffer{real: sink, requestID: "req_fold_accumulation_pin_00000"}

	// Two passes, as a re-decision produces: 2+3 excluded, 1+0 demoted, and
	// the emptied flag true on only the SECOND -- so an OR that reads only
	// the first, or a counter that overwrites, both go red.
	fold.Trace(ResolutionTraceEvent{Stage: "offer_pool", OfferPoolSummary: true,
		OfferPoolVectorOnlyExcluded: 2, OfferPoolVectorOnlyDemoted: 1, OfferPoolEmptiedByExclusion: false})
	fold.Trace(ResolutionTraceEvent{Stage: "offer_pool", OfferPoolSummary: true,
		OfferPoolVectorOnlyExcluded: 3, OfferPoolVectorOnlyDemoted: 0, OfferPoolEmptiedByExclusion: true})
	fold.flush()

	if len(sink.summaries) != 1 {
		t.Fatalf("flush produced %d decision summaries, want 1", len(sink.summaries))
	}
	got := sink.summaries[0]
	if got.OfferPoolVectorOnlyExcluded != 5 {
		t.Errorf("excluded = %d, want 5 (2+3) -- an overwrite would report 3 and a dropped second summary 2", got.OfferPoolVectorOnlyExcluded)
	}
	if got.OfferPoolVectorOnlyDemoted != 1 {
		t.Errorf("demoted = %d, want 1 (1+0) -- an overwrite would report 0", got.OfferPoolVectorOnlyDemoted)
	}
	if !got.OfferPoolEmptiedByExclusion {
		t.Error("emptied flag is false; it is an OR across passes and the SECOND summary set it, so reading only the first loses it")
	}
}

// THE NIL-FRAME BRANCH, value-pinned. A confirmation round found that a
// nil-only regression -- the `frame == nil` branch returning `passed` --
// passes every arm above, because all of them supply a frame. A no-frame
// turn's summary would then claim a frame passed the gate, which is the same
// "a key that can lie" failure the tripwire exists to prevent, on the one
// branch that is legitimately reached in production.
func TestTheTripwireReportsNotProposedForANilFrame(t *testing.T) {
	t.Parallel()
	gate, basis := frameGateObservable(nil)
	if gate != "not_proposed" {
		t.Fatalf("frameGateObservable(nil) = %q, want %q -- a turn that proposed no frame must never read as one that passed the gate", gate, "not_proposed")
	}
	if basis != "none" {
		t.Errorf("refuse basis for a nil frame = %q, want the explicit token %q", basis, "none")
	}
	// The discriminating half: a passing frame must NOT render the same,
	// so a build hardcoding either value fails one of these two.
	if passing, _ := frameGateObservable(passingCohortFrame()); passing == gate {
		t.Fatalf("a passing frame and a nil frame both render %q; the two states are indistinguishable on the line", passing)
	}
}
