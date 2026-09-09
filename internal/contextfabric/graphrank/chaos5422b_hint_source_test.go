package graphrank

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// BEHAVIOUR PRESERVED EXACTLY, and this is the measurement that says so rather
// than a claim that it is.
//
// Replacing the string test with the enumeration touches the classification of
// EVERY hint, and the classification feeds two different decisions: the contest
// exemption and the caller-hint SHORT CIRCUIT (which is also where
// CommitBasisCallerCanonicalID is stamped). This drives the three populations
// that exist — a caller string, the answer-reuse recheck, and a prior-subject
// receipt — through the production entry point in the reuse-recheck call shape
// (nil frame, nil confirmed kind) and pins committed set, commit basis AND
// candidate count for each.
//
// The candidate count is the discriminating column: the receipt does NOT
// short-circuit, so it falls through to hybrid search and the pool is larger.
// A change that moved the recheck off the short circuit would show up here as
// its count changing from 1 to 3, and nothing about the committed set would
// have told you.
func TestTheEnumerationPreservesEveryHintSourcesBehaviour(t *testing.T) {
	t.Parallel()
	member := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1", Label: "Platform Team"}
	type want struct {
		candidates int
		basis      string
	}
	cases := map[string]want{
		"workbench": {candidates: 1, basis: "caller_canonical_id"},
		string(hintsource.AnswerReuseAuthorizationRecheck): {candidates: 1, basis: "caller_canonical_id"},
		string(hintsource.PriorSubjectReceipt):             {candidates: 3, basis: "caller_canonical_id"},
	}
	for source, expected := range cases {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			backend := contestBackend("platform")
			backend.exactHints = map[string]CandidateNode{
				SubjectKey(member): candidateNode(member.Kind, member.CanonicalID, member.Label, 1, "*"),
			}
			req := testRequest()
			req.Options.MaxSubjectCandidates = 20
			req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
				{Kind: member.Kind, ID: member.CanonicalID, Label: member.Label, Source: source},
			}
			// The reuse-recheck call shape EXACTLY: nil confirmed kind, nil
			// frame, empty anchor kind.
			res, _, bases, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
				storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, req, testInterpreted("platform"),
				backend.deps(), nil, nil, nil, "")
			if err != nil {
				t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
			}
			committed := false
			for _, subject := range res.Committed {
				if subject.CanonicalID == member.CanonicalID {
					committed = true
				}
			}
			basis := string(bases[SubjectKey(member)])
			t.Logf("source=%q committed=%v basis=%q candidates=%d", source, res.Committed, basis, len(res.Candidates))
			if !committed {
				t.Fatalf("committed %v, want the hinted subject", res.Committed)
			}
			if basis != expected.basis {
				t.Fatalf("commit basis = %q, want %q", basis, expected.basis)
			}
			if len(res.Candidates) != expected.candidates {
				t.Fatalf("candidates = %d, want %d — the short-circuit decision for this source changed. "+
					"A source that stops short-circuiting falls through to hybrid search, which is a different "+
					"resolution on a live path, and the committed set alone would not have shown it",
					len(res.Candidates), expected.candidates)
			}
		})
	}
}

// THE TWO ATTRIBUTES ARE SEPARATE, and the answer-reuse recheck is the member
// that proves they must be: engine-minted AND contest-exempt AND short-circuit
// eligible. Collapsing them back into one boolean fails here.
func TestTheRecheckIsEngineMintedAndStillShortCircuitEligible(t *testing.T) {
	t.Parallel()
	recheck := hintsource.Lookup(string(hintsource.AnswerReuseAuthorizationRecheck))
	if !recheck.EngineMinted {
		t.Errorf("the reuse recheck is minted by this engine; calling it caller-authored is the misnaming this "+
			"change removes. got %+v", recheck)
	}
	if !recheck.ContestExempt || !recheck.ShortCircuitEligible {
		t.Errorf("the reuse recheck must keep both policies: its subjects commit through the caller-hint short "+
			"circuit, and moving it off that exit changes the answer-reuse path. got %+v", recheck)
	}
	receipt := hintsource.Lookup(string(hintsource.PriorSubjectReceipt))
	if !receipt.EngineMinted || receipt.ContestExempt || receipt.ShortCircuitEligible {
		t.Errorf("a prior-subject receipt is engine-minted and neither contest-exempt nor short-circuit "+
			"eligible. got %+v", receipt)
	}
	if receipt.EngineMinted == recheck.ContestExempt && receipt.ContestExempt == recheck.ContestExempt {
		t.Errorf("authorship and policy agree on every member, so this fixture cannot tell a collapsed boolean " +
			"from the split one and measures nothing")
	}
	// THE FAILURE DIRECTION for the CONTEST, which is what an unclassified
	// candidate source must fall to. An unenumerated hint STRING is caller
	// authored by definition (Source is a caller-supplied wire string), but a
	// candidateSource nobody classified must be refused, never exempt.
	admission := newContestAdmission(contestScope{
		MemberKind: contextfabric.SubjectTeam, Source: contestScopeFrameMemberKind,
	})
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_x"}
	if admission.admits(subject, candidateSource("a_source_nobody_classified")) {
		t.Errorf("an unclassified candidate source was ADMITTED; the failure direction must be refuse, not exempt")
	}
}

// THE CLASSIFIER'S OWN CONTRACT, pinned directly rather than through a
// resolution — because for one member it CANNOT be pinned through a resolution.
//
// A mutation arm that made the contest read AUTHORSHIP instead of the contest
// policy survived the whole end-to-end battery. It is a real difference for
// exactly one member: the answer-reuse recheck is engine-minted AND
// contest-exempt, so authorship and policy disagree there — but that member
// cannot reach a refusing scope (its call site passes no frame and no confirmed
// kind), so no end-to-end fixture can observe the difference. Rather than
// disclose that arm as an allowed survivor, the property it tests is asserted
// where it IS observable: on the function that makes the decision.
func TestTheClassifierDecidesByContestPolicyNotByAuthorship(t *testing.T) {
	t.Parallel()
	for source, want := range map[string]candidateSource{
		string(hintsource.PriorSubjectReceipt):             sourceEngineHint,
		string(hintsource.AnswerReuseAuthorizationRecheck): sourceCallerHint,
		"workbench":                  sourceCallerHint,
		"a_source_no_producer_emits": sourceCallerHint,
	} {
		if got := hintCandidateSource(source); got != want {
			t.Errorf("hintCandidateSource(%q) = %q, want %q — the classifier must read the CONTEST policy of "+
				"the source, not its authorship. The reuse recheck is engine-minted and still exempt, and "+
				"reading authorship here silently refuses it", source, got, want)
		}
	}
}

// ROW 4 OF THE AXES TABLE: the reuse recheck cannot reach a refusing contest
// scope, and the pin asserts the REASON rather than the outcome — it drives the
// recheck's own call shape and requires the scope to be `none`. If a future
// change starts passing a confirmed kind or a frame there, this fails loudly
// instead of the exemption quietly deciding something new.
func TestTheReuseRecheckCallShapeCannotReachARefusingScope(t *testing.T) {
	t.Parallel()
	frame := contestFrame("platform")
	confirmed := confirmedTeamKind()
	if scope := decideContestScope(frame, confirmed, anchorPoolKindScope{}); scope.MemberKind == "" {
		t.Fatalf("the control failed: this frame and confirmed kind must produce a refusing scope, or the "+
			"assertion below is vacuous. got %+v", scope)
	}
	// The recheck passes nil for BOTH, and either alone is sufficient.
	for name, scope := range map[string]contestScope{
		"nil frame and nil confirmed kind (the recheck's actual call)": decideContestScope(nil, nil, anchorPoolKindScope{}),
		"nil confirmed kind alone":                                     decideContestScope(frame, nil, anchorPoolKindScope{}),
		"nil frame alone":                                              decideContestScope(nil, confirmed, anchorPoolKindScope{}),
	} {
		if scope.MemberKind != "" || scope.Source != contestScopeNone {
			t.Errorf("%s produced a REFUSING scope %+v; the answer-reuse recheck's exemption was argued from "+
				"this being unreachable, and it is now reachable", name, scope)
		}
	}
}

// THE EMITTED LINE, WITH VALUES, through the real handler at the production
// level — a recording tracer proves a field was set, only the handler proves it
// is emitted. An engine-minted receipt of the refused kind must be counted as
// REFUSED and never as an exemption.
func TestTheEnumerationsRefusalIsOnTheEmittedLineWithValues(t *testing.T) {
	t.Parallel()
	member := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1", Label: "Platform Team"}
	for source, expected := range map[string]struct{ withheld, exempted float64 }{
		string(hintsource.PriorSubjectReceipt): {withheld: 1, exempted: 0},
		"workbench":                            {withheld: 0, exempted: 1},
	} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
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
			deps := backend.deps()
			deps.ResolutionTracer = NewSlogResolutionTracer(
				slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
			req := testRequest()
			req.Options.MaxSubjectCandidates = 20
			req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
				{Kind: member.Kind, ID: member.CanonicalID, Label: member.Label, Source: source},
			}
			if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
				storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, req, testInterpreted("platform"),
				deps, confirmedTeamKind(), nil, contestFrame("platform"), contextfabric.SubjectRepository); err != nil {
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
			if got := line["offer_pool_anchor_kind_withheld"]; got != expected.withheld {
				t.Errorf("emitted offer_pool_anchor_kind_withheld = %v, want %v", got, expected.withheld)
			}
			if got := line["offer_pool_anchor_kind_exempted"]; got != expected.exempted {
				t.Errorf("emitted offer_pool_anchor_kind_exempted = %v, want %v", got, expected.exempted)
			}
		})
	}
}
