package falkorgraph

// TURN-1 BINDING OF A TWO-NAMED-OPERAND COMPARISON -- the red-at-parent
// battery.
//
// WHAT THIS FILE PROVES, AND WHY IT IS HERE AND NOT IN package contextfabric.
// Every arm below drives a REAL contextfabric.Engine whose GraphReader is a
// REAL *Adapter over a fake connection, so the chain under test is
// interpreter -> engine -> adapter -> graphrank resolver, unmocked at the
// seam that matters. contextfabric cannot import falkorgraph (falkorgraph
// imports it), so this package is the only one that can hold both halves --
// the same reason cohort_pool_truncation_engine_test.go and
// engine_org_isolation_test.go live here.
//
// THE MEASUREMENT THAT MADE THIS FILE NECESSARY. The probe that first
// established the zero-commit result passed a NIL FRAME. A nil frame routes
// resolution down the flat-pool path and never reaches the production
// dispatch, so that probe measured a path the product does not take for a
// framed comparison. Every fixture here carries a REAL validated frame,
// built through the shipped DeriveFrameObligations rather than by hand-typing
// obligations, and an interpreter that returns it on the family outcome --
// which is the only way the frame reaches Adapter.ResolveSubjects at all.
//
// EVERY ARM ASSERTS ITS OWN FIXTURE FIRST. A comparison fixture that
// accidentally failed to retrieve either operand would satisfy "nothing was
// committed" for the wrong reason and would go on passing after the defect
// is fixed. Each arm therefore proves the candidate pool it depends on
// before it asserts anything about the decision taken over that pool.
//
// The ticket is referred to by role throughout; no tracker id appears in
// this file.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ---------------------------------------------------------------------------
// FIXTURE VOCABULARY
// ---------------------------------------------------------------------------

// The two operand terms are DISTINCT single words with no shared substring,
// so a candidate retrieved for one operand can never be mistaken for a
// retrieval hit on the other, and the fake connection can key its answers on
// the query text without ambiguity.
const (
	comparisonTermA = "platform"
	comparisonTermB = "payments"

	// comparisonQuestion contains BOTH operand terms, because that is what a
	// real comparison question looks like and it is precisely what makes the
	// whole-question retrieval pass able to return a subject neither operand
	// names. An arm that used a question mentioning neither term would
	// disable the question pass and quietly stop measuring it.
	comparisonQuestion = "compare platform and payments this quarter"

	// comparisonScopedAnchorTerm is the scoped operand's anchor. It is a
	// third distinct word: the scoped hold must fire on the operand VARIANT,
	// never on the anchor being unretrievable.
	comparisonScopedAnchorTerm = "infrastructure"
)

var (
	comparisonSubjectA = contextfabric.SubjectRef{
		Kind: contextfabric.SubjectTeam, CanonicalID: "team_platform", Label: comparisonTermA,
	}
	comparisonSubjectB = contextfabric.SubjectRef{
		Kind: contextfabric.SubjectTeam, CanonicalID: "team_payments", Label: comparisonTermB,
	}
	// comparisonRivalB is a SECOND exact claimant on operand B's term. It
	// exists for the arm that needs operand B ambiguous within its own slot
	// while operand A stays cleanly resolved.
	comparisonRivalB = contextfabric.SubjectRef{
		Kind: contextfabric.SubjectTeam, CanonicalID: "team_payments_platformops", Label: comparisonTermB,
	}
	// comparisonQuestionOnlySubject is reachable ONLY from the whole-question
	// retrieval pass: its label equals neither operand term, and its indexed
	// search text is the question's own connective prose. It is the subject
	// that must never stand in for an operand.
	comparisonQuestionOnlySubject = contextfabric.SubjectRef{
		Kind: contextfabric.SubjectTeam, CanonicalID: "team_quarterly_review", Label: "quarterly review",
	}
)

// ---------------------------------------------------------------------------
// FRAMES -- built through the shipped derivation, never hand-typed
// ---------------------------------------------------------------------------

// namedComparisonOperand builds one named operand with its kind STATED, which
// is the precondition the narrow cut is defined over. An operand whose kind is
// absent is deliberately outside this cut and is not fixtured here.
func namedComparisonOperand(term string, kind contextfabric.SubjectKind) contextfabric.SubjectOperand {
	expected := kind
	return contextfabric.SubjectOperand{
		Kind:  contextfabric.SubjectOperandNamed,
		Named: &contextfabric.NamedSubjectExpression{Terms: []string{term}, ExpectedKind: &expected},
	}
}

// twoNamedOperandComparisonFrame is the shape the cut is defined over: an
// explicit set of exactly two named operands, each stating its kind, under the
// compare goal.
//
// Built through DeriveFrameObligations so the obligations these arms travel
// with come from the product's own tables. A hand-typed obligation list would
// let a table change leave this file pinning a frame the server no longer
// derives.
func twoNamedOperandComparisonFrame() *contextfabric.QuestionFrame {
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCompare},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionExplicitSet,
			Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
				namedComparisonOperand(comparisonTermA, contextfabric.SubjectTeam),
				namedComparisonOperand(comparisonTermB, contextfabric.SubjectTeam),
			}},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)
	return &frame
}

// namedAndScopedOperandComparisonFrame is the same explicit set with the
// SECOND operand replaced by the scoped variant: "compare the platform team
// against the teams under infrastructure".
//
// The scoped operand carries its own MemberKind and its own anchor terms, and
// those are DIFFERENT CONCEPTS -- the anchor's own subject kind is settled at
// resolution time and the frame cannot name it. This fixture keeps them
// distinct so an implementation that resolved the anchor and slotted it in as
// a second named operand would be visible rather than convenient.
func namedAndScopedOperandComparisonFrame() *contextfabric.QuestionFrame {
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCompare},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionExplicitSet,
			Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
				namedComparisonOperand(comparisonTermA, contextfabric.SubjectTeam),
				{
					Kind: contextfabric.SubjectOperandScoped,
					Scoped: &contextfabric.ScopedSetExpression{
						AnchorTerms: []string{comparisonScopedAnchorTerm},
						MemberKind:  contextfabric.SubjectTeam,
					},
				},
			}},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)
	return &frame
}

// ---------------------------------------------------------------------------
// RETRIEVAL DOUBLE -- a real Adapter over a connection keyed on the query
// ---------------------------------------------------------------------------

// comparisonAuthorizedRow is a full-text row that every principal in this file
// is authorized to see. Authorization is asserted open deliberately: these
// arms are about operand binding, and a row silently dropped by
// AuthorizedAttributes would empty a slot for a reason no arm is measuring.
func comparisonAuthorizedRow(subject contextfabric.SubjectRef, searchText string, score float64) row {
	r := fulltextRow(string(subject.Kind), subject.CanonicalID, subject.Label, searchText, &score)
	r["node"].(*node).Properties["authorization_repositories"] = "*"
	return r
}

// comparisonConn answers the full-text query from a table keyed on the
// retrieval term, and records every query it was asked.
//
// KEYED ON params["query"], NOT ON THE CYPHER. runFulltextQuery puts the
// tokenized, pipe-joined term in that parameter and builds one identical
// cypher for every pass, so the cypher cannot tell the per-term passes apart
// from the whole-question pass. A double keyed on the cypher would return the
// same rows to every pass and would make the whole-question arm vacuous.
type comparisonConn struct {
	mu sync.Mutex
	// rowsForTerm returns the rows for one retrieval query. It receives the
	// RAW query parameter, so an arm can answer the whole-question pass
	// differently from either per-term pass.
	rowsForTerm func(query string) []row
	queries     []string
}

func (c *comparisonConn) record(query string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, query)
}

func (c *comparisonConn) observedQueries() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.queries...)
}

// newComparisonAdapter wires the recording connection into a REAL Adapter.
//
// `vectorRows` non-nil makes the adapter VECTOR-CAPABLE, which is not a
// convenience: ResolveDeps.SearchQuestion is wired to
// questionVectorSearchNodes (reader.go), and that function returns nothing at
// all when a.embedder is nil. newFakeAdapter configures no embedder, so on a
// plain fixture the whole-question pass NEVER RUNS -- an arm about what the
// question pass may not do would measure nothing, and a mutant deleting the
// question-search exclusion would read as killed while the path stayed dead.
// The fence needs an operational vector index of the stub vector's own
// dimension, or the pass is refused one step later for a different reason.
func newComparisonAdapter(t *testing.T, conn *comparisonConn, vectorRows []row) *Adapter {
	t.Helper()
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			conn.record(comparisonVectorQueryMarker)
			return vectorRows, nil
		case strings.Contains(cypher, "fulltext"):
			query, _ := params["query"].(string)
			conn.record(query)
			if conn.rowsForTerm == nil {
				return nil, nil
			}
			return conn.rowsForTerm(query), nil
		}
		return nil, nil
	}}
	if vectorRows == nil {
		return newFakeAdapter(t, fake)
	}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(comparisonVectorDimension)}, nil
	}
	adapter := newFakeAdapter(t, fake)
	adapter.attachEmbedder(EmbedderOptions{
		Embedder:        &stubEmbedder{vector: comparisonStubVector()},
		SimilarityFloor: 0.55,
	})
	return adapter
}

// comparisonVectorQueryMarker is what the recorder logs for the vector arm, so
// an arm can prove the whole-question pass actually reached the backend rather
// than inferring it from a candidate that could have come from a term pass.
const comparisonVectorQueryMarker = "[vector question pass]"

const comparisonVectorDimension = 2

func comparisonStubVector() []float32 { return []float32{1, 0} }

// termQueryContains reports whether a retrieval query carries the given
// operand term. runFulltextQuery joins tokenized terms with "|", so a
// substring test over the joined query is the honest read of "this pass was
// looking for that term".
func termQueryContains(query, term string) bool {
	return strings.Contains(strings.ToLower(query), strings.ToLower(term))
}

// perOperandRows is the ordinary answering policy: each operand term retrieves
// exactly the rows declared for it, and the whole-question pass retrieves
// whatever `question` declares (nil for most arms).
func perOperandRows(forA, forB, question []row) func(string) []row {
	return func(query string) []row {
		hasA := termQueryContains(query, comparisonTermA)
		hasB := termQueryContains(query, comparisonTermB)
		switch {
		// The whole-question pass carries BOTH operand words, which is what
		// distinguishes it from either per-term pass. Checked FIRST, because
		// a per-term test would otherwise claim it.
		case hasA && hasB:
			return question
		case hasA:
			return forA
		case hasB:
			return forB
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// ENGINE DRIVE
// ---------------------------------------------------------------------------

// comparisonInterpreter returns a fixed interpretation together with the
// VALIDATED FRAME and the question family.
//
// The frame is what every operand-structure consumer reads. An interpreter
// that returned an empty family outcome would hand resolution a nil frame,
// which is exactly the measurement error this file exists to avoid repeating.
type comparisonInterpreter struct {
	interpreted contextfabric.InterpretedQuestion
	frame       *contextfabric.QuestionFrame
	family      contextfabric.QuestionFamily
}

func (i comparisonInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	return i.interpreted, contextfabric.QuestionFamilyOutcome{
		Frame:            i.frame,
		FrameObligations: i.frame.Obligations,
		Family:           i.family,
		Source:           contextfabric.QuestionFamilySourceModel,
	}, nil
}

// refusingFactReader fails the test the moment a fact read is attempted.
//
// This is the no-early-read guard in its smallest form: a held comparison
// must terminate BEFORE the fact read, and "no facts appeared on the
// document" is satisfied just as well by a read that ran and returned
// nothing. Only a reader that refuses can tell those apart.
type refusingFactReader struct {
	t *testing.T
}

func (r refusingFactReader) ReadFacts(_ context.Context, _ storage.Principal, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	r.t.Helper()
	r.t.Errorf("the fact reader was invoked for a held comparison, over subjects %#v -- the hold is supposed to terminate before any read", request.Subjects)
	return contextfabric.CanonicalFactBundle{
		Facts: []contextfabric.CanonicalFact{}, Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		Version: "ops-v1", Versions: map[contextfabric.FactKind]string{}, Watermarks: map[contextfabric.FactKind]string{},
	}, nil
}

// refusingSynthesizer fails the test if synthesis is reached. A held
// comparison publishes no judgment, and the terminal path never calls a
// synthesizer, so any invocation is the defect.
type refusingSynthesizer struct {
	t *testing.T
}

func (s refusingSynthesizer) Synthesize(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	s.t.Helper()
	s.t.Error("the synthesizer was invoked for a held comparison -- a hold publishes no judgment and must terminate before synthesis")
	// WELL-FORMED, DELIBERATELY, even though reaching here is already the
	// failure. The first version of this double returned a zero-value result;
	// the engine then rejected it ("result identity or status violates v1
	// bounds") and Investigate returned an ERROR, which the drive turns into
	// a t.Fatalf -- so five arms died on a validation message instead of on
	// the assertion they exist to make. The t.Error above had already fired,
	// so the arms were red for the right reason, but the reason was masked.
	// A refusing double must still let the call it is refusing COMPLETE.
	return contextfabric.InvestigationResult{
		Status: contextfabric.InvestigationNoMatch, StrongestPressures: []string{}, Drivers: []contextfabric.DriverJudgment{},
		RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Paths: []contextfabric.RelationshipPath{},
		Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
		ClaimedFacts:        []contextfabric.ClaimedFact{},
		Coverage:            contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: "No confidently resolved subject was found in the authorized organization graph.", Warnings: []string{},
		Versions: contextfabric.VersionSet{
			Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
			InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
		},
	}, nil
}

// comparisonDrive is one full investigation. `facts` and `synthesizer` are
// parameters rather than fixtures because the two halves of this battery need
// opposite doubles: the arms that must NOT read pass refusing ones, and the
// arm that must read all the way through passes permissive ones.
type comparisonDrive struct {
	frame        *contextfabric.QuestionFrame
	family       contextfabric.QuestionFamily
	terms        []string
	conn         *comparisonConn
	facts        contextfabric.CanonicalFactReader
	synthesizer  contextfabric.AnswerSynthesizer
	priorResults contextfabric.InvestigationResultStore
	receipts     []contextfabric.BoundSubjectReceipt
	question     string
	// vectorRows, when non-nil, makes this drive's adapter vector-capable so
	// the whole-question pass runs at all. See newComparisonAdapter.
	vectorRows []row
}

func (d comparisonDrive) run(t *testing.T) contextfabric.InvestigationResult {
	t.Helper()

	results := d.priorResults
	if results == nil {
		results = discardingResultStore{}
	}
	question := d.question
	if question == "" {
		question = comparisonQuestion
	}

	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: comparisonInterpreter{
			interpreted: contextfabric.InterpretedQuestion{
				Shape:             contextfabric.ShapeExplicitCohort,
				RequestedJudgment: "comparison",
				SubjectTerms:      d.terms,
				TimeContext:       contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				FactRequirements:  []contextfabric.FactRequirement{},
			},
			frame:  d.frame,
			family: d.family,
		},
		Graph:        newComparisonAdapter(t, d.conn, d.vectorRows),
		Facts:        d.facts,
		Synthesizer:  d.synthesizer,
		Results:      results,
		Requirements: productionRequirementDeriver{},
	}, contextfabric.EngineOptions{
		ServiceVersion: "acr-test",
		NewResultID:    func() string { return "result_comparison_turn1" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1,
		RequestID:     "request_comparison_turn1",
		Question:      question,
		TimeContext: contextfabric.TimeContext{
			Axis: contextfabric.TemporalCurrent,
			// A confirmed window, so an unconfirmed-window clarification can
			// never be mistaken for the operand clarification under test.
			EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D},
		},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		PriorSubjectReceipts: d.receipts,
		Consumer:             contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

// ---------------------------------------------------------------------------
// SHARED ASSERTIONS
// ---------------------------------------------------------------------------

// committedKeys renders the committed set as stable, comparable strings.
func committedKeys(resolution contextfabric.SubjectResolution) []string {
	keys := make([]string, 0, len(resolution.Committed))
	for _, subject := range resolution.Committed {
		keys = append(keys, fmt.Sprintf("%s/%s", subject.Kind, subject.CanonicalID))
	}
	return keys
}

func subjectKey(subject contextfabric.SubjectRef) string {
	return fmt.Sprintf("%s/%s", subject.Kind, subject.CanonicalID)
}

// candidateFor returns the candidate the resolution published for a subject,
// or nil. Arms use it to prove their fixture actually retrieved what they
// think it retrieved.
func candidateFor(resolution contextfabric.SubjectResolution, subject contextfabric.SubjectRef) *contextfabric.SubjectCandidate {
	for index := range resolution.Candidates {
		if subjectKey(resolution.Candidates[index].Subject) == subjectKey(subject) {
			return &resolution.Candidates[index]
		}
	}
	return nil
}

// requireRetrieved fails loudly when a fixture did not put a subject into the
// candidate pool at all. Without this, every "it was not committed" assertion
// below could be satisfied by a fixture that never retrieved the subject, and
// would keep passing after the defect is fixed.
func requireRetrieved(t *testing.T, resolution contextfabric.SubjectResolution, subject contextfabric.SubjectRef, why string) contextfabric.SubjectCandidate {
	t.Helper()
	candidate := candidateFor(resolution, subject)
	if candidate == nil {
		t.Fatalf("the fixture did not retrieve %s at all (candidates = %v) -- %s, so nothing below this line measures the decision it claims to",
			subjectKey(subject), committedCandidateKeys(resolution), why)
	}
	return *candidate
}

// committedCandidateKeys renders the published pool with the THREE fields a
// commit decision actually reads: state, confidence, and match mechanisms.
//
// The mechanisms are here because of a real diagnosis: "committed = []" alone
// cannot tell a pool that was never retrieved from one that was retrieved and
// refused, nor an exact-label match that the gate declined from a candidate
// that never carried MatchExact at all. A failure message that forces the next
// person to re-run with a debugger is a failure message that has not finished
// its job.
func committedCandidateKeys(resolution contextfabric.SubjectResolution) []string {
	keys := make([]string, 0, len(resolution.Candidates))
	for _, candidate := range resolution.Candidates {
		mechanisms := make([]string, 0, len(candidate.MatchMechanisms))
		for _, mechanism := range candidate.MatchMechanisms {
			mechanisms = append(mechanisms, string(mechanism))
		}
		keys = append(keys, fmt.Sprintf("%s(state=%s,conf=%.2f,mechanisms=[%s],terms=%v)",
			subjectKey(candidate.Subject), candidate.State, candidate.Confidence,
			strings.Join(mechanisms, "+"), candidate.MatchedTerms))
	}
	return keys
}

// assertHeldComparison is the shared shape of every hold: nothing committed,
// one clarification that names BOTH operands, and the completion action
// retained because the status is the clarification-required one.
func assertHeldComparison(t *testing.T, result contextfabric.InvestigationResult, operandOne, operandTwo string) {
	t.Helper()

	if got := committedKeys(result.SubjectResolution); len(got) != 0 {
		t.Errorf("the held comparison published %d committed subjects (%v), want none -- publishing the resolved side alone is the half-comparison this rule refuses", len(got), got)
	}
	if result.Status != contextfabric.InvestigationClarificationRequired {
		t.Errorf("status = %q, want %q -- the shared projection drops a clarification unless the status is the clarification-required one, so any other status delivers the question without its completion action",
			result.Status, contextfabric.InvestigationClarificationRequired)
	}
	prompt := result.SubjectResolution.ClarificationPrompt
	if strings.TrimSpace(prompt) == "" {
		t.Fatal("the held comparison carries an empty clarification prompt -- the user is asked nothing and cannot complete the comparison")
	}
	lowered := strings.ToLower(prompt)
	// NECESSARY, NOT SUFFICIENT -- and the first version of this helper
	// stopped here, which made two arms VACUOUS. An exact label match
	// requires EqualFold(term, subject.Label) (graphrank/candidate.go), so
	// these fixtures' candidate labels MUST equal their operand terms; and
	// the parent's own prompt is graphrank.ClarificationPrompt, which is a
	// list of the first three candidate LABELS. So the single-subject prompt
	// contained both operand names by construction and satisfied these two
	// checks at the parent, on a resolution that had not held for any
	// operand reason at all. Measured, not argued: both arms passed at the
	// parent before the check below existed.
	if !strings.Contains(lowered, strings.ToLower(operandOne)) {
		t.Errorf("clarification prompt %q does not name operand one (%q)", prompt, operandOne)
	}
	if !strings.Contains(lowered, strings.ToLower(operandTwo)) {
		t.Errorf("clarification prompt %q does not name operand two (%q)", prompt, operandTwo)
	}
	// THE DISCRIMINATING CHECK, and the plan's actual stated harm: "a
	// SINGLE-SUBJECT disambiguation prompt for a two-subject request". A held
	// comparison must not be asking the generic "which of these subjects did
	// you mean" question -- it must name each operand, its state, and the
	// action required to complete the comparison.
	//
	// Expressed as a CALL, never as a copied literal. Asserting the absence
	// of the string "Which subject did you mean" would silently stop
	// discriminating the day that wording changed, and pinning another
	// function's prose in this file is the accretion this codebase keeps
	// paying for. Comparing against what the generic builder would produce
	// FOR THIS RESOLUTION'S OWN PUBLISHED CANDIDATES cannot go stale: the two
	// move together or the assertion fires.
	if generic := graphrank.ClarificationPrompt(result.SubjectResolution.Candidates); prompt == generic {
		t.Errorf("the held comparison's prompt is byte-identical to the generic single-subject enumeration %q -- "+
			"the operand names appear in it only because it lists candidate labels and exactness forces label==term, "+
			"so a two-subject request is still being answered with a one-subject question", generic)
	}
	if len(result.ClaimedFacts) != 0 {
		t.Errorf("the held comparison published %d claimed facts, want none", len(result.ClaimedFacts))
	}
	if len(result.Drivers) != 0 {
		t.Errorf("the held comparison published %d drivers, want none", len(result.Drivers))
	}
	if strings.TrimSpace(result.DirectJudgment) != "" {
		t.Errorf("the held comparison published a judgment %q, want none", result.DirectJudgment)
	}
}

// ---------------------------------------------------------------------------
// ARM 1 -- THE DISCRIMINATING ARM
// ---------------------------------------------------------------------------

// TestTurnOneBindsBothNamedOperandsOfAComparison is the arm the whole cut is
// measured by, and the one the earlier nil-frame probe could not express.
//
// Two named team operands, each exactly retrievable by its own term, no
// hints, a real frame, the production engine and the production resolver.
// Both operands must be committed and the turn must not ask a question.
//
// AT THE PARENT it fails for the reported harm: both operand terms are
// flattened into one deduped term bag before resolution begins, both exact
// claimants land in one candidate pool, the exact-label gate requires
// uniqueness ACROSS THE WHOLE POOL and therefore refuses, and the turn
// returns a resolution-wide ambiguity -- one question, for two subjects,
// committing neither.
func TestTurnOneBindsBothNamedOperandsOfAComparison(t *testing.T) {
	t.Parallel()

	conn := &comparisonConn{rowsForTerm: perOperandRows(
		[]row{comparisonAuthorizedRow(comparisonSubjectA, comparisonTermA, 1)},
		[]row{comparisonAuthorizedRow(comparisonSubjectB, comparisonTermB, 1)},
		nil,
	)}

	result := comparisonDrive{
		frame:       twoNamedOperandComparisonFrame(),
		family:      contextfabric.QuestionFamilyExplicitComparison,
		terms:       []string{comparisonTermA, comparisonTermB},
		conn:        conn,
		facts:       emptyFactReader{},
		synthesizer: countingSynthesizer{},
	}.run(t)

	// FIXTURE CONTROL. Both operands must actually have been searched for,
	// separately. If the resolver only ever issued one retrieval pass, the
	// arm is measuring a retrieval failure, not a binding decision.
	queries := conn.observedQueries()
	sawA, sawB := false, false
	for _, query := range queries {
		if termQueryContains(query, comparisonTermA) {
			sawA = true
		}
		if termQueryContains(query, comparisonTermB) {
			sawB = true
		}
	}
	if !sawA || !sawB {
		t.Fatalf("retrieval queries = %v; searched for %q = %t, for %q = %t -- both operands must reach retrieval or this arm measures nothing",
			queries, comparisonTermA, sawA, comparisonTermB, sawB)
	}

	// THE PROPERTY. Both operands bound, together, on turn one.
	got := committedKeys(result.SubjectResolution)
	want := []string{subjectKey(comparisonSubjectA), subjectKey(comparisonSubjectB)}
	if len(got) != 2 {
		t.Fatalf("committed = %v (%d subjects), want both operands %v -- a two-operand comparison whose operands are each unambiguously named must bind both on turn one.\n"+
			"PUBLISHED POOL: %v\n"+
			"(an empty pool means retrieval never reached the slots; a populated pool with conf=1 and a MatchExact mechanism means the per-slot gate refused a lone exact match, which is a different defect entirely)",
			got, len(got), want, committedCandidateKeys(result.SubjectResolution))
	}
	present := map[string]bool{got[0]: true, got[1]: true}
	for _, key := range want {
		if !present[key] {
			t.Errorf("committed = %v, missing operand %s", got, key)
		}
	}
	if result.Status == contextfabric.InvestigationClarificationRequired {
		t.Errorf("status = %q with both operands resolvable -- asking a question here is the round trip this work removes; prompt was %q",
			result.Status, result.SubjectResolution.ClarificationPrompt)
	}

	// DETERMINISM, at its smallest: the published order follows the frame's
	// operand order, never a map walk.
	if got[0] != subjectKey(comparisonSubjectA) {
		t.Errorf("committed order = %v, want the frame's operand order (%s first) -- published order assembled from a subject map is not an order at all", got, subjectKey(comparisonSubjectA))
	}
}

// ---------------------------------------------------------------------------
// ARM 2 -- THE HOLD
// ---------------------------------------------------------------------------

// TestOneResolvedOperandAndOneMissingOperandHoldsTheWholeComparison is the
// hold in its plainest form: operand A is exactly retrievable, operand B
// retrieves nothing at all.
//
// AT THE PARENT it fails for a harm distinct from arm 1's. With one exact
// claimant in the flat pool the exact-label gate is unique and DOES fire, so
// the parent commits operand A alone and proceeds to read facts for it: the
// user receives half a comparison, and -- because the shared projection drops
// a clarification unless the status is the clarification-required one -- no
// way to complete it.
func TestOneResolvedOperandAndOneMissingOperandHoldsTheWholeComparison(t *testing.T) {
	t.Parallel()

	conn := &comparisonConn{rowsForTerm: perOperandRows(
		[]row{comparisonAuthorizedRow(comparisonSubjectA, comparisonTermA, 1)},
		nil,
		nil,
	)}

	result := comparisonDrive{
		frame:       twoNamedOperandComparisonFrame(),
		family:      contextfabric.QuestionFamilyExplicitComparison,
		terms:       []string{comparisonTermA, comparisonTermB},
		conn:        conn,
		facts:       refusingFactReader{t: t},
		synthesizer: refusingSynthesizer{t: t},
	}.run(t)

	// FIXTURE CONTROL. Operand A must genuinely be the resolvable side. If it
	// were not retrieved either, this arm would degenerate into "nothing was
	// found", which the parent already handles and which measures nothing
	// about holding.
	resolved := requireRetrieved(t, result.SubjectResolution, comparisonSubjectA,
		"operand A is the side this arm needs RESOLVED so that holding it back is a decision rather than an absence")
	if resolved.Confidence != 1 {
		t.Fatalf("operand A's candidate confidence = %v, want 1 (an exact label match) -- a lower-confidence fixture would be held for the ordinary ambiguity reason and would not exercise the hold", resolved.Confidence)
	}

	assertHeldComparison(t, result, comparisonTermA, comparisonTermB)
}

// TestOneResolvedOperandAndOneAmbiguousOperandHoldsTheWholeComparison is the
// same rule where operand B has TWO exact claimants of its own.
//
// It is a separate arm because the parent reaches it by a different route: two
// rivals inside operand B's own term make the pool-wide exact gate refuse, so
// the parent commits nothing here and asks a single-subject question. The
// after-state is identical to the arm above, which is the point -- the hold is
// a property of the comparison, not of which way the operand failed.
func TestOneResolvedOperandAndOneAmbiguousOperandHoldsTheWholeComparison(t *testing.T) {
	t.Parallel()

	conn := &comparisonConn{rowsForTerm: perOperandRows(
		[]row{comparisonAuthorizedRow(comparisonSubjectA, comparisonTermA, 1)},
		[]row{
			comparisonAuthorizedRow(comparisonSubjectB, comparisonTermB, 1),
			comparisonAuthorizedRow(comparisonRivalB, comparisonTermB, 1),
		},
		nil,
	)}

	result := comparisonDrive{
		frame:       twoNamedOperandComparisonFrame(),
		family:      contextfabric.QuestionFamilyExplicitComparison,
		terms:       []string{comparisonTermA, comparisonTermB},
		conn:        conn,
		facts:       refusingFactReader{t: t},
		synthesizer: refusingSynthesizer{t: t},
	}.run(t)

	// FIXTURE CONTROL. Both of operand B's rivals must be in the pool, or the
	// slot is not ambiguous and this arm is a duplicate of the one above.
	requireRetrieved(t, result.SubjectResolution, comparisonSubjectB, "operand B's first exact claimant is what makes its slot ambiguous")
	requireRetrieved(t, result.SubjectResolution, comparisonRivalB, "operand B's second exact claimant is what makes its slot ambiguous")

	assertHeldComparison(t, result, comparisonTermA, comparisonTermB)
}

// TestReversedOperandOrderHoldsTheSameComparison is the symmetry control.
// Nothing about the hold may depend on which operand position failed.
func TestReversedOperandOrderHoldsTheSameComparison(t *testing.T) {
	t.Parallel()

	conn := &comparisonConn{rowsForTerm: perOperandRows(
		nil,
		[]row{comparisonAuthorizedRow(comparisonSubjectB, comparisonTermB, 1)},
		nil,
	)}

	result := comparisonDrive{
		frame:       twoNamedOperandComparisonFrame(),
		family:      contextfabric.QuestionFamilyExplicitComparison,
		terms:       []string{comparisonTermA, comparisonTermB},
		conn:        conn,
		facts:       refusingFactReader{t: t},
		synthesizer: refusingSynthesizer{t: t},
	}.run(t)

	requireRetrieved(t, result.SubjectResolution, comparisonSubjectB,
		"operand B is the resolvable side in the reversed arm; without it this is not the mirror of the arm above")

	assertHeldComparison(t, result, comparisonTermA, comparisonTermB)
}

// ---------------------------------------------------------------------------
// ARM 3 -- THE SCOPED HOLD
// ---------------------------------------------------------------------------

// TestANamedOperandPairedWithAScopedOperandHoldsBeforeAnyRead is the scoped
// rule, and it holds on the operand VARIANT, before operand retrieval.
//
// The refusing fact reader is doing real work here: admitting this pair would
// flip the whole answer into the absorbing degraded state, so a resolver
// limitation would reach the user as a data problem. The arm asserts the read
// never happened at all rather than that the document came back empty.
func TestANamedOperandPairedWithAScopedOperandHoldsBeforeAnyRead(t *testing.T) {
	t.Parallel()

	// The anchor IS resolvable and its children ARE available. The hold must
	// not depend on the scoped side being unreachable -- that would be a
	// retrieval outcome, not the variant rule.
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_infrastructure", Label: comparisonScopedAnchorTerm}
	conn := &comparisonConn{rowsForTerm: func(query string) []row {
		switch {
		case termQueryContains(query, comparisonScopedAnchorTerm):
			return []row{comparisonAuthorizedRow(anchor, comparisonScopedAnchorTerm, 1)}
		case termQueryContains(query, comparisonTermA):
			return []row{comparisonAuthorizedRow(comparisonSubjectA, comparisonTermA, 1)}
		}
		return nil
	}}

	result := comparisonDrive{
		frame:       namedAndScopedOperandComparisonFrame(),
		family:      contextfabric.QuestionFamilyExplicitComparison,
		terms:       []string{comparisonTermA, comparisonScopedAnchorTerm},
		conn:        conn,
		facts:       refusingFactReader{t: t},
		synthesizer: refusingSynthesizer{t: t},
		question:    "compare platform against the teams under infrastructure",
	}.run(t)

	assertHeldComparison(t, result, comparisonTermA, comparisonScopedAnchorTerm)

	// THE SUBSTITUTION REFUSAL. The anchor must never be resolved and slotted
	// in as the second named operand: the frame cannot name the anchor's
	// subject kind, so an implementation that inserted it would be filling a
	// coordinate with a guess.
	for _, subject := range result.SubjectResolution.Committed {
		if subjectKey(subject) == subjectKey(anchor) {
			t.Errorf("the scope anchor %s was committed as an operand -- the anchor is a retrieval pointer, never an operand value", subjectKey(anchor))
		}
	}
	if result.Cohort != nil {
		t.Errorf("a held scoped comparison published a cohort of %d members -- the scoped side must not be expanded before the hold", len(result.Cohort.Members))
	}
}

// ---------------------------------------------------------------------------
// ARM 4 -- THE WHOLE-QUESTION-ONLY SUBJECT
// ---------------------------------------------------------------------------

// TestAWholeQuestionOnlySubjectStillCannotCommitOnItsOwn is a CONTROL, and it
// is GREEN AT THE PARENT AND GREEN AFTER. It is written down as a control
// because the first version of it was written as a red-at-parent arm and was
// wrong about the product.
//
// WHAT THE MEASUREMENT SHOWED. A whole-question-only candidate arrives at
// confidence 0.70 and cannot reach the lone-candidate commit gate -- not
// because of anything this fixture does, but because two shipped facts
// compose. `ResolveDeps.SearchQuestion` is wired to `questionVectorSearchNodes`
// (reader.go), so the whole-question pass is VECTOR-ONLY; and the vector
// relevance ceiling is set deliberately below graphrank's lone-candidate gate
// so that "a vector hit alone never commits a subject" is true by ARITHMETIC
// rather than by a rule that could later be special-cased away. That intent is
// stated in vector.go's own doc comment on the constant, and it is pinned by
// two existing tests -- `TestD11Class_NoVectorOnlyConfidenceCanReachTheCommitGate`
// (graphrank/vector_ladder_regression_test.go) and
// `TestAC_3778_3_VectorOnlyCandidateCannotReachTheLoneCommitGate`
// (graphrank/corroboration_test.go). This file cites them by NAME and does not
// restate the constant: a copied number here would be a second, silently
// divergent authority for a value those two already own.
//
// SO WHY KEEP THE ARM AT ALL. After this work there are TWO independent
// reasons a question-only subject cannot stand in for an operand: the ceiling,
// and its exclusion from every slot's identity pool. A control that passes on
// either and fails only when BOTH are gone is exactly the property worth
// holding -- and it is the arm that would catch someone raising the ceiling on
// the assumption that the slot exclusion now covers it.
//
// The RED half of the original row moved to the receipt route, where the
// ceiling gives no protection at all -- see
// TestAQuestionOnlySubjectCarriedInOnAReceiptCannotBeBoundToAnOperand.
func TestAWholeQuestionOnlySubjectStillCannotCommitOnItsOwn(t *testing.T) {
	t.Parallel()

	result, conn := questionOnlyComparisonDrive(t)

	// FIXTURE CONTROL 1. The whole-question vector pass must actually have
	// REACHED the backend. Without it this control cannot tell "the pass ran
	// and its hit could not commit" -- the property -- from "the pass never
	// ran", which is what a fixture with no embedder silently does.
	requireQuestionPassRan(t, conn)

	// FIXTURE CONTROL 2. The subject must actually be IN the pool. A control
	// asserting something was not committed proves nothing about a subject
	// that was never a candidate.
	candidate := requireRetrieved(t, result.SubjectResolution, comparisonQuestionOnlySubject,
		"the whole-question pass is the only source this control has, and its hit is the thing under test")
	if candidate.Confidence <= 0 {
		t.Fatalf("the question-only candidate's confidence = %.2f -- a hit that reads as no signal at all is not the weak-evidence case this control is about", candidate.Confidence)
	}

	// THE PROPERTY.
	if subjectCommitted(result.SubjectResolution, comparisonQuestionOnlySubject) {
		t.Errorf("%s committed on a whole-question hit alone (confidence %.2f) -- a vector-only candidate reaching the lone commit gate means the ceiling and the slot exclusion are BOTH gone, and a subject answering neither operand is now answering the question",
			subjectKey(comparisonQuestionOnlySubject), candidate.Confidence)
	}
}

// questionOnlyComparisonDrive runs the two-named-operand comparison where
// NEITHER operand term retrieves anything and the whole-question vector pass
// retrieves one subject whose label matches neither operand.
//
// The receipt arm in the file beside this one builds the SAME retrieval shape
// on the receipt-aware adapter (which additionally answers the by-kind-and-id
// lookup a carried receipt re-authorizes through), sharing this file's
// questionOnlyVectorRow so the two halves of the split row cannot drift into
// measuring different subjects.
func questionOnlyComparisonDrive(t *testing.T) (contextfabric.InvestigationResult, *comparisonConn) {
	t.Helper()
	conn := &comparisonConn{rowsForTerm: perOperandRows(nil, nil, nil)}
	result := comparisonDrive{
		frame:       twoNamedOperandComparisonFrame(),
		family:      contextfabric.QuestionFamilyExplicitComparison,
		terms:       []string{comparisonTermA, comparisonTermB},
		conn:        conn,
		vectorRows:  []row{questionOnlyVectorRow()},
		facts:       refusingFactReader{t: t},
		synthesizer: refusingSynthesizer{t: t},
	}.run(t)
	return result, conn
}

// questionOnlyVectorRow is the whole-question pass's single hit: distance 0,
// i.e. as close as the ANN query can report, so nothing about this fixture is
// holding the candidate back.
func questionOnlyVectorRow() row {
	closest := 0.0
	return row{"node": &node{Properties: map[string]interface{}{
		propKind:                     string(comparisonQuestionOnlySubject.Kind),
		propCanonicalID:              comparisonQuestionOnlySubject.CanonicalID,
		propLabel:                    comparisonQuestionOnlySubject.Label,
		propSearchText:               comparisonQuestion,
		"authorization_repositories": "*",
	}}, "score": closest}
}

// requireQuestionPassRan proves the whole-question vector pass reached the
// backend, by its own recorded marker rather than by inferring it from a
// candidate that a term pass could also have produced.
func requireQuestionPassRan(t *testing.T, conn *comparisonConn) {
	t.Helper()
	for _, query := range conn.observedQueries() {
		if query == comparisonVectorQueryMarker {
			return
		}
	}
	t.Fatalf("the whole-question vector pass never reached the backend (queries = %v) -- nothing here measures a rider on a pass that did not run",
		conn.observedQueries())
}

func subjectCommitted(resolution contextfabric.SubjectResolution, subject contextfabric.SubjectRef) bool {
	for _, committed := range resolution.Committed {
		if subjectKey(committed) == subjectKey(subject) {
			return true
		}
	}
	return false
}
