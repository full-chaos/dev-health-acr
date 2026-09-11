package graphrank

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A MENTION IS NOT A SCOPE.
//
// Corpus row neg-mentions-teams-but-not-grouped, measured on the acr main
// yardstick: a question whose ANCHOR is one kind and whose MEMBERS are another
// ended with a member-kind subject committed as the answer's subject. The chain
// was three turns and no single turn looks wrong on its own -- the engine offers
// the expected_kind axis, the caller redeems the MEMBER kind truthfully because
// that is the kind it asked about, the confirmed member kind then narrows the
// SUBJECT pool to itself and drops the anchor the terms actually named, the one
// weak lexical survivor is offered with a receipt id, and the next turn commits
// it on caller_canonical_id: the engine crediting the caller with an identifier
// the engine itself minted.
//
// These pins live at the CANDIDATE-SET BOUNDARY because that is where the fix
// lives, and the two arms below are why. A per-gate rule was tried first and
// four review rounds found the same class four times.

func contestFrame(term string) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{term},
				MemberKind:  contextfabric.SubjectTeam,
			},
		},
	}
}

func confirmedTeamKind() *contextfabric.ConfirmedExpectedKind {
	return &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectTeam}
}

// contestCapture keeps the folded decision_summary line AND the per-candidate
// offer_pool dispositions it summarises, so a reported count can be checked
// against its own source rather than against a second literal this test also
// wrote.
type contestCapture struct {
	summaries    []ResolutionTraceEvent
	dispositions []ResolutionTraceEvent
	offerPool    []ResolutionTraceEvent
}

func (c *contestCapture) Trace(event ResolutionTraceEvent) {
	switch {
	case event.Stage == "decision_summary":
		c.summaries = append(c.summaries, event)
	// ONLY the summary this seam emits. resolution.go emits an offer_pool
	// summary of its own once per resolver pass for the vector counters; those
	// are a different quantity and counting them here would measure the number
	// of passes, not the number of disclosures.
	case event.Stage == "offer_pool" && event.OfferPoolSummary && event.OfferPoolAnchorKindWithheldScope != "":
		c.offerPool = append(c.offerPool, event)
	case event.Stage == "offer_pool" && event.OfferPoolDisposition != "":
		c.dispositions = append(c.dispositions, event)
	}
}

// THE ROW'S OWN SHAPE: the term names a REPOSITORY, and a team of the org shares
// enough of the term to be reached lexically. Retrieval finds both; the
// confirmed member kind is what used to drop the repository and leave the team.
func contestBackend(term string) *fakeGraphBackend {
	repo := candidateNode(contextfabric.SubjectRepository,
		"repository.v2:github:"+term, term, 0.95, "*")
	team := candidateNode(contextfabric.SubjectTeam,
		"team.v2:github:"+term+"-owners", term+" owners", 0.4, "*")
	return &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{term: {repo, team}},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {
				contextfabric.SubjectTeam:       {team},
				contextfabric.SubjectRepository: {repo},
			},
		},
	}
}

// THE HARM, at the production entry point.
func TestAMemberKindCandidateNeverEntersTheContest(t *testing.T) {
	t.Parallel()
	res := resolveContest(t, contestBackend("platform"), contestFrame("platform"), confirmedTeamKind(), nil, 20, contextfabric.SubjectRepository)
	for _, candidate := range res.Candidates {
		if candidate.Subject.Kind == contextfabric.SubjectTeam {
			t.Fatalf("candidate %q of the declared MEMBER kind reached the contest set; on a children_of_scope "+
				"frame the subject is the ANCHOR, whose kind I11 says is never the member's",
				candidate.Subject.CanonicalID)
		}
	}
	for _, subject := range res.Committed {
		if subject.Kind == contextfabric.SubjectTeam {
			t.Fatalf("committed %v of the member kind", res.Committed)
		}
	}
}

// resolveContest drives the production entry point. scopeAnchorKind is supplied
// because production does: ScopeAnchorRetrievalKind hands the resolution the
// anchor kind a redeemed receipt named, and a fixture that omits it is resolving
// a DIFFERENT question from the one the row asks.
func resolveContest(t *testing.T, backend *fakeGraphBackend, frame *contextfabric.QuestionFrame,
	confirmedKind *contextfabric.ConfirmedExpectedKind, tracer ResolutionTracer, max int,
	scopeAnchorKind contextfabric.SubjectKind) contextfabric.SubjectResolution {
	t.Helper()
	req := testRequest()
	req.Options.MaxSubjectCandidates = max
	deps := backend.deps()
	if tracer != nil {
		deps.ResolutionTracer = tracer
	}
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, req, testInterpreted("platform"),
		deps, confirmedKind, nil, frame, scopeAnchorKind)
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return res
}

// r4 FINDING 1, PERMANENT PIN. A candidate refused the contest must not consume
// an OFFER SLOT on its way out.
//
// The first implementation removed members from the commit index, which happens
// AFTER ranking, reservation and truncation. With one slot, a higher-ranked team
// took it and then disappeared, and the retrieved repository anchor -- the
// subject the question was actually about -- was never offered at all. The
// clarification prompt then told the caller that retrieval had matched only
// subjects of the member kind, which was false: the anchor had been retrieved
// and thrown away. Measured, not argued:
//
//	max=2 candidates=map[repository:1]
//	max=1 candidates=map[]  prompt="Retrieval matched only subjects of the kind..."
//
// Two arms, and the max=2 arm is what makes the max=1 arm mean anything: without
// it a fixture that simply never retrieved the anchor would pass.
func TestARefusedMemberNeverConsumesTheAnchorsOfferSlot(t *testing.T) {
	t.Parallel()
	for _, max := range []int{2, 1} {
		t.Run(strings.Join([]string{"max", string(rune('0' + max))}, "="), func(t *testing.T) {
			t.Parallel()
			backend := contestBackend("platform")
			// THE SHAPE THAT ACTUALLY REPRODUCES, and my first attempt did not.
			// Ordinary search must return ONLY the member, with the anchor
			// reachable exclusively through the kind-scoped arm: that is where
			// the slot competition happens. A fixture with both in ordinary
			// search passes even on the implementation that had this defect,
			// which I verified before trusting this pin.
			team := candidateNode(contextfabric.SubjectTeam, "team.v2:github:platform-owners", "platform owners", 0.6, "*")
			repo := candidateNode(contextfabric.SubjectRepository, "repository.v2:github:platform", "platform repository", 0.3, "*")
			backend.searchResults["platform"] = []CandidateNode{team}
			backend.searchKindResults["platform"][contextfabric.SubjectTeam] = []CandidateNode{team}
			backend.searchKindResults["platform"][contextfabric.SubjectRepository] = []CandidateNode{repo}
			res := resolveContest(t, backend, contestFrame("platform"), confirmedTeamKind(), nil, max, contextfabric.SubjectRepository)
			kinds := map[contextfabric.SubjectKind]int{}
			for _, candidate := range res.Candidates {
				kinds[candidate.Subject.Kind]++
			}
			if kinds[contextfabric.SubjectRepository] != 1 {
				t.Fatalf("max=%d: the retrieved anchor is not in the candidate set (kinds=%v, prompt=%q) -- a "+
					"candidate this question refuses must never displace one it can actually resolve over",
					max, kinds, res.ClarificationPrompt)
			}
		})
	}
}

// r4 FINDING 2, PERMANENT PIN, and the reason the filter is at the boundary
// rather than at the gates.
//
// Removing a candidate from the commit index left its claims in
// identityClaimants/identityMatchTerms, where identityCrossClassRivalClaimant
// still read them -- so a refused member went on VETOING an exact anchor through
// a side channel while the code that had removed it read as correct. A two-arm
// control: the only difference is whether the refused member is present at all.
func TestARefusedMemberCannotVetoTheAnchorThroughIdentity(t *testing.T) {
	t.Parallel()
	for _, includeMember := range []bool{false, true} {
		name := "member absent"
		if includeMember {
			name = "member present"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := aliasCandidateNode(contextfabric.SubjectRepository, "repository.v2:github:platform", "platform", -1, nil, nil, false)
			member := aliasCandidateNode(contextfabric.SubjectTeam, "team.v2:github:platform-owners", "platform owners", -1, []string{"platform"}, nil, true)
			repo.Attributes["authorization_repositories"] = "*"
			member.Attributes["authorization_repositories"] = "*"
			nodes := []CandidateNode{repo}
			if includeMember {
				nodes = append(nodes, member)
			}
			backend := &fakeGraphBackend{
				enableAliasLookup:    true,
				aliasLookupComplete:  true,
				aliasLookupClaimants: map[string][]CandidateNode{"platform": nodes},
			}
			res := resolveContest(t, backend, contestFrame("platform"), confirmedTeamKind(), nil, 20, contextfabric.SubjectRepository)
			if len(res.Committed) != 1 {
				t.Fatalf("committed %v with the refused member %s -- a candidate outside the contest set must "+
					"not decide the outcome through the identity claimant map either", res.Committed, name)
			}
		})
	}
}

// r4 FINDING 3, PERMANENT PIN. The disclosed count is DISTINCT SUBJECTS ACROSS
// THE CALL, not a per-pass quantity.
//
// The confirmed-kind re-decision resolves over a freshly built pool, so two
// passes can refuse overlapping-but-different populations. Summing double-counts
// a subject refused twice; taking the last pass's value drops one that only the
// first pass saw. Both were shipped and both were wrong, in opposite directions;
// a set keyed by subject is the only shape that cannot be either.
func TestTheRefusedCountIsDistinctSubjectsAcrossTheCall(t *testing.T) {
	t.Parallel()
	backend := contestBackend("platform")
	// searchTruncated is what sends this call into the confirmed-kind
	// re-decision -- without it there is only one pass and the defect this pins
	// cannot appear at all.
	backend.searchTruncated = true
	// DIVERGENT POPULATIONS ACROSS THE PASSES, which is the whole point and
	// which an earlier version of this fixture did not have: both of its
	// searches returned the SAME subject, so it caught summing a duplicate but
	// could not catch LOSING a subject that only one pass saw. This member
	// appears only in the kind-scoped results.
	lateMember := candidateNode(contextfabric.SubjectTeam, "team.v2:github:platform-late", "platform late", 0.7, "*")
	backend.searchKindResults["platform"][contextfabric.SubjectTeam] = append(
		backend.searchKindResults["platform"][contextfabric.SubjectTeam], lateMember)
	capture := &contestCapture{}
	resolveContest(t, backend, contestFrame("platform"), confirmedTeamKind(), capture, 20, contextfabric.SubjectRepository)

	if len(capture.offerPool) != 1 {
		t.Fatalf("captured %d offer_pool SUMMARY events, want exactly 1 -- the disclosure is per CALL, and more "+
			"than one summary means it went back to describing a pass", len(capture.offerPool))
	}
	distinct := map[string]bool{}
	for _, event := range capture.dispositions {
		if event.OfferPoolDisposition == contestSetDisposition {
			distinct[SubjectKey(event.Subject)] = true
		}
	}
	if len(distinct) == 0 {
		t.Fatal("no subject was refused in this fixture -- the fixture is wrong, not the counter")
	}
	if got := capture.offerPool[0].OfferPoolAnchorKindWithheld; got != len(distinct) {
		t.Fatalf("offer_pool_anchor_kind_withheld = %d but %d DISTINCT subjects were refused; neither summing "+
			"per pass nor keeping the last pass's value can express this", got, len(distinct))
	}
	if len(capture.summaries) != 1 {
		t.Fatalf("captured %d decision_summary events, want 1", len(capture.summaries))
	}
	if got := capture.summaries[0].DecisionSummaryFields.OfferPoolAnchorKindWithheld; got != len(distinct) {
		t.Fatalf("the FOLDED decision line reports %d withheld, want %d", got, len(distinct))
	}
}

// THE OBSERVABLE, ASSERTED ON THE LINE THAT IS ACTUALLY LOGGED.
//
// This is deliberately NOT a recording-tracer test. An earlier version of this
// seam was pinned entirely by handing a capture struct to the resolution and
// asserting on the struct -- and a mutant that deleted every one of these fields
// from the real emitter passed all of it, because nothing in the suite ever went
// through the handler that writes the line an operator reads. A recording tracer
// proves the field was SET; only the handler proves it is EMITTED.
//
// Both arms are required. Without the withheld arm the fields could be hardcoded
// to zero and `none`; without the untouched arm they could be hardcoded the
// other way and every ordinary question would report a refusal that never
// happened.
func TestTheRefusalIsOnTheEmittedInfoLine(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name         string
		frame        *contextfabric.QuestionFrame
		wantWithheld float64
		wantScope    string
		wantReason   string
	}{
		{
			name:  "a scope-anchored question reports what it refused",
			frame: contestFrame("platform"), wantWithheld: 1,
			wantScope: "team", wantReason: "frame_member_kind",
		},
		{
			// EXPLICIT ZEROS, and this is the arm that gives the other one its
			// meaning: if the keys appeared only when something was refused,
			// their absence on an ordinary line could not be told apart from a
			// build that stopped emitting them.
			name:  "an ordinary question reports explicit zeros",
			frame: nil, wantWithheld: 0,
			wantScope: "none", wantReason: "none",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			// The REAL handler, at the production level. Info, because that is
			// what the deployed configuration runs at -- a field that only
			// appears at Debug is not an observable an operator has.
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			tracer := NewSlogResolutionTracer(logger)
			resolveContest(t, contestBackend("platform"), testCase.frame, confirmedTeamKind(), tracer, 20, contextfabric.SubjectRepository)

			var line map[string]any
			for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
				if raw == "" {
					continue
				}
				var entry map[string]any
				if err := json.Unmarshal([]byte(raw), &entry); err != nil {
					t.Fatalf("emitted a line that is not JSON: %v", err)
				}
				if entry["stage"] == "decision_summary" {
					line = entry
				}
			}
			if line == nil {
				t.Fatalf("no decision_summary line was EMITTED at Info; the fields cannot be an observable if "+
					"the line carrying them never reaches the handler. captured:\n%s", buf.String())
			}
			for key, want := range map[string]any{
				"offer_pool_anchor_kind_withheld":        testCase.wantWithheld,
				"offer_pool_anchor_kind_withheld_scope":  testCase.wantScope,
				"offer_pool_anchor_kind_withheld_reason": testCase.wantReason,
				// The exemption count belongs on the SAME line for the same
				// reason: a refusal count of zero beside an exemption count of
				// one is a different fact from two zeros, and only one of them
				// means "this question refused nothing". A mutation arm that
				// deleted this field from the emitter survived the capture-based
				// test, because a recording tracer proves a field was SET and
				// only the handler proves it is EMITTED.
				"offer_pool_anchor_kind_exempted": float64(0),
			} {
				got, present := line[key]
				if !present {
					t.Errorf("%q is absent from the EMITTED line -- an explicit zero that is missing is not an "+
						"explicit zero", key)
					continue
				}
				if got != want {
					t.Errorf("emitted %q = %v, want %v", key, got, want)
				}
			}
		})
	}
}

// ONE ARM PER COMMIT GATE, because "removed from the contest" is a claim about
// every gate and a per-gate guard is only ever as complete as someone's memory
// of the list. The full-scope review named six: the exact index, the identity
// fast path, the lone floor, top-of-two, the vector margin rescue and the
// evidence-census rescue.
//
// EACH ARM IS A TWO-ARM CONTROL. The fixture is built so the ANCHOR decides
// through that gate; then the refused member is added and nothing about the
// outcome may change. Without the member-absent arm a fixture that never
// committed anything would pass; without the member-present arm the filter could
// be absent and nothing would notice.
//
// Where a gate's own preconditions cannot be constructed here it is NAMED and
// SKIPPED with the reason, never quietly dropped — an unexercised gate must not
// read like an exercised one.
func TestNoRefusedCandidateCanInfluenceAnyCommitGate(t *testing.T) {
	t.Parallel()
	anchor := candidateNode(contextfabric.SubjectRepository, "repository.v2:github:platform", "platform", 0.95, "*")
	member := candidateNode(contextfabric.SubjectTeam, "team.v2:github:platform-owners", "platform owners", 0.9, "*")

	for _, testCase := range []struct {
		gate  string
		build func(withMember bool) *fakeGraphBackend
		skip  string
	}{
		{
			gate: "exact index / lone floor / top-of-two (ordinary ranked pool)",
			build: func(withMember bool) *fakeGraphBackend {
				b := contestBackend("platform")
				results := []CandidateNode{anchor}
				kinds := map[contextfabric.SubjectKind][]CandidateNode{contextfabric.SubjectRepository: {anchor}}
				if withMember {
					results = append(results, member)
					kinds[contextfabric.SubjectTeam] = []CandidateNode{member}
				}
				b.searchResults["platform"] = results
				b.searchKindResults["platform"] = kinds
				return b
			},
		},
		{
			gate: "identity fast path (alias claimants)",
			build: func(withMember bool) *fakeGraphBackend {
				repo := aliasCandidateNode(contextfabric.SubjectRepository, "repository.v2:github:platform", "platform", -1, nil, nil, false)
				repo.Attributes["authorization_repositories"] = "*"
				nodes := []CandidateNode{repo}
				if withMember {
					rival := aliasCandidateNode(contextfabric.SubjectTeam, "team.v2:github:platform-owners", "platform owners", -1, []string{"platform"}, nil, true)
					rival.Attributes["authorization_repositories"] = "*"
					nodes = append(nodes, rival)
				}
				return &fakeGraphBackend{
					enableAliasLookup:    true,
					aliasLookupComplete:  true,
					aliasLookupClaimants: map[string][]CandidateNode{"platform": nodes},
				}
			},
		},
		{
			gate: "confirmed-kind re-decision (second resolver pass over a fresh pool)",
			build: func(withMember bool) *fakeGraphBackend {
				b := contestBackend("platform")
				b.searchTruncated = true
				results := []CandidateNode{anchor}
				kinds := map[contextfabric.SubjectKind][]CandidateNode{contextfabric.SubjectRepository: {anchor}}
				if withMember {
					results = append(results, member)
					kinds[contextfabric.SubjectTeam] = []CandidateNode{member}
				}
				b.searchResults["platform"] = results
				b.searchKindResults["platform"] = kinds
				return b
			},
		},
		{
			gate: "vector margin rescue",
			skip: "the rescue needs a live vector arm (vectorArmSimilarity populated by a " +
				"vector-capable backend); this package's fake produces lexical provenance only, so a " +
				"fixture here would report green while never reaching the gate",
		},
		{
			gate: "evidence-census rescue",
			skip: "the rescue needs deps.CensusFunc plus a stalled resolution, which this fixture " +
				"cannot produce; it merges through the SAME boundary as every other arm, and that is " +
				"pinned directly by TestTheBoundaryRefusesBeforeAnyIdentityClaimIsRecorded",
		},
	} {
		t.Run(testCase.gate, func(t *testing.T) {
			t.Parallel()
			if testCase.skip != "" {
				t.Skip(testCase.skip)
			}
			without := resolveContest(t, testCase.build(false), contestFrame("platform"), confirmedTeamKind(), nil, 20, contextfabric.SubjectRepository)
			if len(without.Committed) != 1 || without.Committed[0].Kind != contextfabric.SubjectRepository {
				t.Fatalf("control arm committed %v, want exactly the anchor -- without a committing control this "+
					"arm cannot tell a refusal from a fixture that never reached the gate", without.Committed)
			}
			with := resolveContest(t, testCase.build(true), contestFrame("platform"), confirmedTeamKind(), nil, 20, contextfabric.SubjectRepository)
			if len(with.Committed) != len(without.Committed) {
				t.Fatalf("adding the refused member changed the committed set from %v to %v -- a candidate outside "+
					"the contest must not influence this gate", without.Committed, with.Committed)
			}
			for i := range with.Committed {
				if with.Committed[i] != without.Committed[i] {
					t.Fatalf("adding the refused member changed the committed set from %v to %v",
						without.Committed, with.Committed)
				}
			}
			for _, candidate := range with.Candidates {
				if candidate.Subject.Kind == contextfabric.SubjectTeam {
					t.Fatalf("the refused member %q is in the candidate set this gate ranked over",
						candidate.Subject.CanonicalID)
				}
			}
		})
	}
}

// THE BOUNDARY ITSELF, pinned directly, because it is what makes the per-gate
// arms above true for gates those arms cannot construct.
//
// Every retrieval arm in this package -- ordinary search, alias claimants, the
// kind-hinted pool, the exact-name arm, the coverage floor, the confirmed-kind
// rescue, the confirmed-kind scoped snapshot and the evidence-census satisfier
// -- merges through mergeSearchResults. So a refusal here is a refusal for all
// of them, including the ones no end-to-end fixture in this file can reach.
//
// The assertion is TWO-SIDED and that is the point: the refused candidate must
// be absent from the pool AND from the identity claimant map, because it was the
// second of those that a per-gate rule missed and that let a refused member veto
// an anchor through a side channel.
func TestTheBoundaryRefusesBeforeAnyIdentityClaimIsRecorded(t *testing.T) {
	t.Parallel()
	anchor := candidateNode(contextfabric.SubjectRepository, "repository.v2:github:platform", "platform", 0.9, "*")
	// An ALIAS candidate, deliberately: recordIdentityClaim only records a
	// claimant that actually claims an alias, so a plain node would leave the
	// identity half of this test vacuous -- it would report "no claim recorded"
	// whether the boundary refused it or not.
	member := aliasCandidateNode(contextfabric.SubjectTeam, "team.v2:github:platform-owners", "platform owners", 0.8, []string{"platform"}, nil, true)
	member.Attributes["authorization_repositories"] = "*"

	for _, testCase := range []struct {
		name        string
		admission   *contestAdmission
		wantPool    int
		wantRefused int
	}{
		{
			// A nil admission admits everything -- this package's own unit
			// callers and the arms that only ever run with no confirmed kind.
			name:      "a nil admission admits both and records nothing",
			admission: nil, wantPool: 2, wantRefused: 0,
		},
		{
			name:      "a scope that refuses the member admits only the anchor",
			admission: newContestAdmission(contestScope{MemberKind: contextfabric.SubjectTeam, Source: contestScopeFrameMemberKind}),
			wantPool:  1, wantRefused: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			pool := map[string]contextfabric.SubjectCandidate{}
			identity := identityClaimants{}
			identityTerms := identityMatchTerms{}
			mergeSearchResults(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}},
				testRequest(), (&fakeGraphBackend{}).deps(), "platform", []CandidateNode{anchor, member},
				pool, map[string]string{}, map[string]bool{}, true, nil, identity, identityTerms, testCase.admission)

			if len(pool) != testCase.wantPool {
				t.Fatalf("pool holds %d candidate(s), want %d", len(pool), testCase.wantPool)
			}
			memberInPool := false
			for key := range pool {
				if strings.Contains(key, "team.v2:github:platform-owners") {
					memberInPool = true
				}
			}
			if memberInPool != (testCase.wantPool == 2) {
				t.Errorf("the refused member is in the pool = %v", memberInPool)
			}
			// THE HALF A PER-GATE RULE MISSED: the identity claimant map. This
			// assertion is ONE-SIDED here and that is stated rather than
			// papered over -- recordIdentityClaim needs claim conditions this
			// direct fixture does not reproduce, so the nil-admission arm
			// records no claim either and cannot serve as the positive control.
			// The two-sided proof is the END-TO-END arm
			// TestARefusedMemberCannotVetoTheAnchorThroughIdentity, which was
			// measured RED against this PR's own base with the member present
			// and GREEN with it absent. What this arm adds is that the refusal
			// happens at the boundary, before the claim could be recorded at
			// all, for every arm that merges through it.
			// THREE levels: identityClaimants is
			// map[identityKeyClass]map[term]map[subjectKey]bool. An earlier
			// version of this loop stopped at the TERM level and compared a term
			// against a subject key, so it could never match and never failed —
			// a review round found it vacuous. It now reaches the subject keys.
			for _, byTerm := range identity {
				for _, subjects := range byTerm {
					for subjectKey := range subjects {
						if strings.Contains(subjectKey, "team.v2:github:platform-owners") && testCase.wantRefused > 0 {
							t.Errorf("the refused member recorded an identity claim (%s) -- a candidate outside "+
								"the contest that still claims an identity goes on vetoing anchors through a "+
								"side channel", subjectKey)
						}
					}
				}
			}
			if got := testCase.admission.withheldCount(); got != testCase.wantRefused {
				t.Errorf("withheldCount() = %d, want %d", got, testCase.wantRefused)
			}
		})
	}
}

// SURVIVOR-DRIVEN PIN 1. A candidate whose KIND FAILED TO RESOLVE carries the
// zero value, and so does a scope that decided nothing — so a bare
// `kind == s.MemberKind` would refuse that whole class for every question in the
// product, through a comparison nobody wrote deliberately.
//
// The code refuses the empty kind explicitly and says why; nothing pinned it,
// and a mutant that deleted the explicit refusal survived the suite. This is
// that mutant's pin.
func TestTheZeroScopeRefusesNothingIncludingTheZeroKind(t *testing.T) {
	t.Parallel()
	var zero contestScope
	if zero.refuses("") {
		t.Error("the zero scope refuses the zero kind: a candidate whose kind failed to resolve would " +
			"vanish from every question in the product")
	}
	if zero.refuses(contextfabric.SubjectTeam) {
		t.Error("the zero scope refuses a real kind")
	}
	// And a DECIDED scope still must not refuse the zero kind, which is the
	// half the deleted guard actually protected.
	decided := contestScope{MemberKind: contextfabric.SubjectTeam, Source: contestScopeFrameMemberKind}
	if decided.refuses("") {
		t.Error("a decided scope refuses the zero kind -- an unresolved candidate is not a member-kind candidate")
	}
	if !decided.refuses(contextfabric.SubjectTeam) {
		t.Error("a decided scope does not refuse its own member kind")
	}
	if decided.refuses(contextfabric.SubjectRepository) {
		t.Error("a decided scope refuses a kind that is not its member kind")
	}
}

// THE SECOND DOORWAY, with the input shape that makes it a doorway at all.
//
// The confirmed-kind re-decision resolves over a pool it builds FRESH, so it is
// a second way into the contest and must carry the same admission. Three
// fixtures failed to test that, and an instrumented run said why: a shared
// SearchKind table serves BOTH the first pass's kind-hinted arm and the
// re-decision, so anything the second pass can see the first pass already
// refused, and the second pass is never what the assertion measures.
//
// THE MISSING CELL IS THAT THE TWO PASSES SEE DIFFERENT POPULATIONS — the same
// fact that made a per-pass count wrong. This SearchKind returns the member only
// from the re-decision's own query onward, which is exactly a graph that gained
// a row between the two reads, and is a shape production can produce. Now the
// second doorway is the only way in.
func TestTheConfirmedKindRedecisionCarriesTheSameAdmission(t *testing.T) {
	t.Parallel()
	// An ambiguous anchor pair, so the FIRST pass commits nothing and the
	// re-decision actually runs.
	// Neither label EQUALS the term, so the exact-label override cannot fire and
	// the equal relevances stay genuinely ambiguous — otherwise the first pass
	// commits and the re-decision, which only runs when it did not, never
	// happens at all.
	anchor := candidateNode(contextfabric.SubjectRepository, "repository.v2:github:plat-one", "plat one", 0.5, "*")
	rival := candidateNode(contextfabric.SubjectRepository, "repository.v2:github:plat-two", "plat two", 0.5, "*")
	late := candidateNode(contextfabric.SubjectTeam, "team.v2:github:platform-late", "platform late", 0.95, "*")

	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"platform": {anchor, rival}},
		searchTruncated:  true,
	}
	deps := backend.deps()
	var teamQueries int
	deps.SearchKind = func(_ context.Context, term string, kind contextfabric.SubjectKind, _ int) ([]CandidateNode, bool, bool, error) {
		if kind == contextfabric.SubjectRepository {
			return []CandidateNode{anchor, rival}, false, false, nil
		}
		if kind != contextfabric.SubjectTeam {
			return nil, false, false, nil
		}
		teamQueries++
		// The FIRST team query belongs to the first pass's kind-hinted arm and
		// must come back empty; every later one is the re-decision reading a
		// graph that has since gained the row.
		if teamQueries == 1 {
			return nil, false, false, nil
		}
		return []CandidateNode{late}, false, false, nil
	}

	capture := &contestCapture{}
	deps.ResolutionTracer = capture
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, req, testInterpreted("platform"),
		deps, confirmedTeamKind(), nil, contestFrame("platform"), contextfabric.SubjectRepository)
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if teamQueries < 2 {
		t.Fatalf("the member-kind query ran %d time(s); the re-decision never read the graph a second time, so "+
			"this fixture is not exercising the second doorway", teamQueries)
	}
	for _, candidate := range res.Candidates {
		if candidate.Subject.CanonicalID == "team.v2:github:platform-late" {
			t.Fatalf("the refused member entered the contest through the re-decision's own pool; that pass "+
				"builds a FRESH pool over a population the first pass never saw, so it must carry the same "+
				"admission. candidates=%v", res.Candidates)
		}
	}
	for _, subject := range res.Committed {
		if subject.Kind == contextfabric.SubjectTeam {
			t.Fatalf("committed %v of the refused member kind through the re-decision", res.Committed)
		}
	}
	refused := false
	for _, event := range capture.dispositions {
		if event.OfferPoolDisposition == contestSetDisposition &&
			event.Subject.CanonicalID == "team.v2:github:platform-late" {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("the member visible ONLY to the re-decision was never refused; that pass admitted it")
	}
}

// ONE ARM PER DOOR, on committed IDENTITY rather than committed COUNT.
//
// A full-scope review found three separate paths by which a candidate reached
// the contest without passing the admission function, after this seam had
// claimed that function was the only way in. Each is pinned here, and each
// asserts WHICH subject committed — an earlier arm of mine asserted only the
// COUNT, which is exactly why a member-kind commit could hide inside a "1".
func TestNoDoorLetsARefusedCandidateIntoTheContest(t *testing.T) {
	t.Parallel()
	anchorRef := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository.v2:github:platform", Label: "platform"}

	t.Run("door: the traversal insert", func(t *testing.T) {
		t.Parallel()
		// The traversal fires only for an OBSERVATION kind (document or
		// episode) — a work-item observation never reaches it, which is how a
		// first attempt at this fixture reported green while measuring nothing.
		observation := candidateNode(contextfabric.SubjectDocument, "doc_child", "child doc", 0.9, "*")
		anchor := candidateNode(anchorRef.Kind, anchorRef.CanonicalID, anchorRef.Label, 0.95, "*")
		backend := &fakeGraphBackend{
			searchResults:    map[string][]CandidateNode{"platform": {anchor, observation}},
			enableSearchKind: true,
			searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
				"platform": {contextfabric.SubjectRepository: {anchor}},
			},
			traverse: func(_ context.Context, term string, _ CandidateNode, _ bool) (contextfabric.SubjectCandidate, ObservationTraversal) {
				return contextfabric.SubjectCandidate{
						Subject:         contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_parent", Label: "Platform Team"},
						Confidence:      0.85,
						MatchedTerms:    []string{term},
						MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchTraversalParent},
					},
					ObservationParentFound
			},
		}
		res := resolveContest(t, backend, contestFrame("platform"), confirmedTeamKind(), nil, 20, contextfabric.SubjectRepository)
		assertNoMemberKindAnywhere(t, res, "the traversal insert proposes a parent this function never saw as a result node")
	})

	t.Run("door: the vector side map", func(t *testing.T) {
		t.Parallel()
		// DRIVEN AT THE BOUNDARY, not end to end, and that is deliberate: an
		// end-to-end version of this arm PASSED on the tree it was meant to
		// fail against, because the anchor committed at a stronger tier before
		// the margin rescue ever ran. What actually has to be true is narrower
		// and checkable directly — a refused candidate's similarity is never
		// RECORDED. vectorMarginCommit takes its competitor from the FULL side
		// map (its own comment says "not restricted to commitIndex"), so a
		// recorded similarity is arithmetic in the rescue whether or not the
		// candidate is eligible; filtering the pool cannot undo that.
		member := candidateNode(contextfabric.SubjectTeam, "team_vec", "platform team", 0.78, "*")
		member.Mechanism = contextfabric.MatchVector
		similarity := 0.89
		member.VectorSimilarity = &similarity
		anchor := candidateNode(anchorRef.Kind, anchorRef.CanonicalID, anchorRef.Label, 0.9, "*")

		pool := map[string]contextfabric.SubjectCandidate{}
		vectorArmSimilarity := map[string]float64{}
		admission := newContestAdmission(contestScope{
			MemberKind: contextfabric.SubjectTeam, Source: contestScopeFrameMemberKind,
		})
		mergeSearchResults(context.Background(),
			storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, testRequest(),
			(&fakeGraphBackend{}).deps(), "platform", []CandidateNode{anchor, member},
			pool, map[string]string{}, map[string]bool{}, true, vectorArmSimilarity,
			identityClaimants{}, identityMatchTerms{}, admission)

		for key := range vectorArmSimilarity {
			if strings.Contains(key, "team_vec") {
				t.Fatalf("the refused candidate's vector similarity was recorded (%s=%v); the margin rescue "+
					"reads the FULL side map, so a recorded similarity still changes the outcome of a contest "+
					"this candidate can never win", key, vectorArmSimilarity[key])
			}
		}
		if admission.withheldCount() != 1 {
			t.Fatalf("withheldCount() = %d, want 1 -- the fixture did not reach the refusal", admission.withheldCount())
		}
	})
}

// assertNoMemberKindAnywhere checks IDENTITY, in both the committed set and the
// candidate set. Asserting a count would let a member-kind commit hide inside a
// number that happened to match.
func assertNoMemberKindAnywhere(t *testing.T, res contextfabric.SubjectResolution, why string) {
	t.Helper()
	for _, subject := range res.Committed {
		if subject.Kind == contextfabric.SubjectTeam {
			t.Fatalf("committed %q of the refused member kind — %s. committed=%v",
				subject.CanonicalID, why, res.Committed)
		}
	}
	for _, candidate := range res.Candidates {
		if candidate.Subject.Kind == contextfabric.SubjectTeam {
			t.Fatalf("candidate %q of the refused member kind reached the contest — %s",
				candidate.Subject.CanonicalID, why)
		}
	}
}

// THE EXEMPTION DOOR, which is a decision rather than a bypass — and the pin
// asserts BOTH halves of that.
//
// A caller-explicit hint of the member kind is ADMITTED: a caller naming a
// subject by canonical id has not been offered anything, so it is not a
// substitution, and refusing it would break "name the subject you mean". What
// changed is that the hint now goes THROUGH the admission function, which
// records the exemption — because an exemption that is silent is
// indistinguishable from a boundary that was never applied.
func TestACallerHintOfTheRefusedKindIsAdmittedAndTheExemptionIsRecorded(t *testing.T) {
	t.Parallel()
	member := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1", Label: "Platform Team"}
	backend := contestBackend("platform")
	backend.exactHints = map[string]CandidateNode{
		SubjectKey(member): candidateNode(member.Kind, member.CanonicalID, member.Label, 1, "*"),
	}
	// THE REAL HANDLER, at the production level — not a recording tracer. A
	// capture proves the field was SET; only the handler proves it is EMITTED,
	// and a mutation arm that deleted this field from the emitter survived a
	// capture-based version of this very test.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: member.Kind, ID: member.CanonicalID, Label: member.Label, Source: "workbench"},
	}
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(logger)
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, req, testInterpreted("platform"),
		deps, confirmedTeamKind(), nil, contestFrame("platform"), contextfabric.SubjectRepository)
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	committed := false
	for _, subject := range res.Committed {
		if subject.CanonicalID == member.CanonicalID {
			committed = true
		}
	}
	if !committed {
		t.Fatalf("committed %v — a caller who names a subject by canonical id must still get it; refusing "+
			"this would break naming a subject, which is decided truth", res.Committed)
	}
	var line map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			t.Fatalf("emitted a line that is not JSON: %v", err)
		}
		if entry["stage"] == "decision_summary" {
			line = entry
		}
	}
	if line == nil {
		t.Fatalf("no decision_summary line was EMITTED at Info:\n%s", buf.String())
	}
	if got, present := line["offer_pool_anchor_kind_exempted"]; !present || got != float64(1) {
		t.Fatalf("emitted offer_pool_anchor_kind_exempted = %v (present=%v), want 1 — the exemption fired and "+
			"said nothing on the line an operator reads, which is indistinguishable from a boundary that was "+
			"never applied", got, present)
	}
	if got := line["offer_pool_anchor_kind_withheld"]; got != float64(0) {
		t.Fatalf("emitted offer_pool_anchor_kind_withheld = %v, want 0 — nothing was REFUSED on this turn, and "+
			"conflating the two counts is what makes the exemption unreadable", got)
	}
}

// r2 FINDING 1, PERMANENT PIN. THE EXEMPTION IS DECIDED BY WHO AUTHORED THE
// HINT, not by the fact that a hint exists.
//
// The first version of the exemption classified EVERY SubjectHint as
// caller-sourced. That is wrong, and resolve.go's own pre-existing test two
// lines above the hint insert already said so: a hint whose source is
// prior_subject_receipt is one THIS ENGINE minted on an earlier turn and read
// back, and it is deliberately excluded from callerSourced for exactly that
// reason. Exempting it would let a member kind the boundary refused on turn N
// walk back into the contest on turn N+1 wearing its own receipt — the
// substitution I11 forbids, laundered through the engine's own output.
//
// Both arms drive the SAME subject through the SAME production entry point and
// differ only in the hint's Source, so nothing but authorship can explain the
// difference in outcome.
func TestAnEngineMintedReceiptOfTheRefusedKindIsRefusedNotExempted(t *testing.T) {
	t.Parallel()
	member := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1", Label: "Platform Team"}

	resolveWithHintSource := func(t *testing.T, source string) (contextfabric.SubjectResolution, map[string]any) {
		t.Helper()
		// RETRIEVAL FINDS NO MEMBER KIND HERE, deliberately: the hint is then
		// the ONLY member-kind candidate in the call, so each disclosed count
		// is attributable to it and to nothing else. contestBackend's own team
		// would be refused on its own account and make both numbers ambiguous.
		anchor := candidateNode(contextfabric.SubjectRepository,
			"repository.v2:github:platform", "platform", 0.95, "*")
		backend := &fakeGraphBackend{
			searchResults:    map[string][]CandidateNode{"platform": {anchor}},
			enableSearchKind: true,
			searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
				"platform": {contextfabric.SubjectRepository: {anchor}},
			},
			exactHints: map[string]CandidateNode{
				SubjectKey(member): candidateNode(member.Kind, member.CanonicalID, member.Label, 1, "*"),
			},
		}
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		req := testRequest()
		req.Options.MaxSubjectCandidates = 20
		req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
			{Kind: member.Kind, ID: member.CanonicalID, Label: member.Label, Source: source},
		}
		deps := backend.deps()
		deps.ResolutionTracer = NewSlogResolutionTracer(logger)
		res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
			storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, req, testInterpreted("platform"),
			deps, confirmedTeamKind(), nil, contestFrame("platform"), contextfabric.SubjectRepository)
		if err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		var line map[string]any
		for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if raw == "" {
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal([]byte(raw), &entry); err != nil {
				t.Fatalf("emitted a line that is not JSON: %v", err)
			}
			if entry["stage"] == "decision_summary" {
				line = entry
			}
		}
		if line == nil {
			t.Fatalf("no decision_summary line was EMITTED at Info:\n%s", buf.String())
		}
		return res, line
	}

	t.Run("an engine-minted receipt is refused and COUNTED AS REFUSED", func(t *testing.T) {
		t.Parallel()
		res, line := resolveWithHintSource(t, "prior_subject_receipt")
		assertNoMemberKindAnywhere(t, res, "a prior_subject_receipt hint is this engine's own earlier output, "+
			"not a subject the caller named")
		if got := line["offer_pool_anchor_kind_withheld"]; got != float64(1) {
			t.Fatalf("emitted offer_pool_anchor_kind_withheld = %v, want 1 — the receipt was kept out of the "+
				"pool but the refusal was never recorded, so the line an operator reads cannot explain the "+
				"missing subject", got)
		}
		// THE HALF THAT MATTERS MOST: refused and exempted are different
		// dispositions, and a fix that merely stopped it committing while
		// still filing it under "exempted" would report that the caller
		// authorised something the caller never sent.
		if got, present := line["offer_pool_anchor_kind_exempted"]; present && got != float64(0) {
			t.Fatalf("emitted offer_pool_anchor_kind_exempted = %v, want 0 — an engine-minted receipt recorded "+
				"as a CALLER exemption attributes this engine's own output to the caller", got)
		}
	})

	t.Run("a caller-authored hint of the same subject still commits", func(t *testing.T) {
		t.Parallel()
		res, line := resolveWithHintSource(t, "workbench")
		committed := false
		for _, subject := range res.Committed {
			if subject.CanonicalID == member.CanonicalID {
				committed = true
			}
		}
		if !committed {
			t.Fatalf("committed %v — the SAME subject with a caller-authored source must still commit; without "+
				"this arm the refusal above would be satisfied by a fixture that simply refuses every hint",
				res.Committed)
		}
		if got := line["offer_pool_anchor_kind_exempted"]; got != float64(1) {
			t.Fatalf("emitted offer_pool_anchor_kind_exempted = %v, want 1", got)
		}
		if got := line["offer_pool_anchor_kind_withheld"]; got != float64(0) {
			t.Fatalf("emitted offer_pool_anchor_kind_withheld = %v, want 0", got)
		}
	})
}

// r2 FINDING 2, PERMANENT PIN. THE EVIDENCE-CENSUS RESCUE MUST HONOUR THE
// BOUNDARY'S ANSWER, not assume it.
//
// mergeCensusAttestedSatisfier returns (key, true) to tell its caller "the
// census witness committed". It computed that answer by mirroring
// NodeCandidate's own gating — deliberately, because inferring success from
// candidatesBySubject's contents is wrong (see its own comment). The admission
// boundary is a SECOND reason mergeSearchResults can decline the same node, and
// nothing was mirroring it: a refused member-kind satisfier was reported as
// merged, with a key naming a subject that is not in the pool.
//
// The assertion is on the boundary's own record of the refusal, and on the
// emitted census outcome — a rescue that quietly returns false while still
// tracing "merged" leaves the operator with the same unexplained gap.
func TestTheCensusRescueReportsARefusalRatherThanAMerge(t *testing.T) {
	t.Parallel()
	member := contextfabric.SubjectRef{
		Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:platform-owners", Label: "platform owners",
	}
	backend := &fakeGraphBackend{
		exactHints: map[string]CandidateNode{
			SubjectKey(member): candidateNode(member.Kind, member.CanonicalID, member.Label, 0.9, "*"),
		},
	}
	capture := &contestCapture{}
	census := &censusOutcomeCapture{}
	deps := backend.deps()
	deps.ResolutionTracer = multiTracer{capture, census}
	attestation := Attestation{
		Outcome: ShadowWouldCommit, UnscopedVisibility: true,
		Kinds: []KindAttestation{{
			Kind: contextfabric.SubjectTeam, Complete: true, Count: 1,
			SatisfierCanonicalID: member.CanonicalID,
		}},
	}
	pool := map[string]contextfabric.SubjectCandidate{}
	admission := newContestAdmission(contestScope{
		MemberKind: contextfabric.SubjectTeam, Source: contestScopeFrameMemberKind,
	})
	key, ok := mergeCensusAttestedSatisfier(context.Background(),
		storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, testRequest(), deps,
		attestation, pool, map[string]string{}, map[string]bool{},
		identityClaimants{}, identityMatchTerms{}, admission)
	if admission.withheldCount() != 1 {
		t.Fatalf("withheldCount() = %d, want 1 — the fixture never reached the refusal, so it measures nothing",
			admission.withheldCount())
	}
	if ok {
		t.Fatalf("mergeCensusAttestedSatisfier() = (%q, true) with a pool of %d — it reported a commit for a "+
			"candidate the admission boundary refused, handing its caller a key naming a subject the pool does "+
			"not contain", key, len(pool))
	}
	if key != "" {
		t.Fatalf("mergeCensusAttestedSatisfier() key = %q, want \"\"", key)
	}
	if got := census.outcomes(); len(got) != 1 || got[0] != contestSetDisposition {
		t.Fatalf("evidence_census_commit outcomes = %v, want exactly [%q] — tracing \"merged\" for a refused "+
			"candidate is the same defect written to the log", got, contestSetDisposition)
	}
}

// censusOutcomeCapture keeps the census stage's own outcome tokens, which the
// contest capture deliberately ignores (it filters to offer_pool stages).
type censusOutcomeCapture struct{ events []ResolutionTraceEvent }

func (c *censusOutcomeCapture) Trace(event ResolutionTraceEvent) {
	if event.Stage == "evidence_census_commit" {
		c.events = append(c.events, event)
	}
}

func (c *censusOutcomeCapture) outcomes() []string {
	out := make([]string, 0, len(c.events))
	for _, event := range c.events {
		out = append(out, event.Outcome)
	}
	return out
}

// multiTracer fans one event out to several tracers, so a single call can be
// observed at two stages without either capture filtering the other's events.
type multiTracer []ResolutionTracer

func (m multiTracer) Trace(event ResolutionTraceEvent) {
	for _, tracer := range m {
		tracer.Trace(event)
	}
}
