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
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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
