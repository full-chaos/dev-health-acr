package paritytest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A clarification prompt answers nothing, and the caller's next turn names it
// as its parent. The invariant this suite enumerates: the substitution guard
// evaluates every follow-up against the identity of the last ANSWERED turn in
// its chain, through a receipt verified against that turn's stored identity,
// however many clarification prompts lie between; a chain that cannot be
// verified fails closed to the remembered-unavailable outcome.
//
// Every chain is built by the REAL Engine.Investigate over the store under
// test, turn by turn: the answered result, the prompts after it, and the
// follow-up. A receipt state other than the one the engine writes is produced
// by re-seeding the stored row through the store's own raw seed, so every
// cell is read back exactly as the store reads it.

var chainPrincipal = storage.Principal{OrgID: "org_carried_parent_chain"}

var (
	chainSubject  = contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project-chain-answered", Label: "Answered"}
	chainOther    = contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project-chain-other", Label: "Other"}
	chainOtherTwo = contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project-chain-other-two", Label: "Other two"}
)

// ChainFollowUp is what the follow-up's own resolution commits, and whether
// its caller can be asked a question.
type ChainFollowUp string

const (
	ChainFollowUpSame           ChainFollowUp = "names_same_subject"
	ChainFollowUpDifferent      ChainFollowUp = "names_different_subject"
	ChainFollowUpDifferentNoAsk ChainFollowUp = "names_different_subject_caller_cannot_be_asked"
	ChainFollowUpSubjectless    ChainFollowUp = "subjectless"
	chainFollowUpCount                        = 4
	chainMaxDepth                             = 3
	chainNotAPromptDepth                      = 0
	chainPersistenceLogMessage                = "context fabric semantic state persistence"
	chainLedgerLogMessage                     = "context fabric confirmed need ledger"
	chainStatusServed                         = "served"
	chainStatusClarified                      = "clarified"
	chainStatusRefused                        = "refused"
	chainStatusUnresolved                     = "unresolved"
)

// ChainFollowUps is the follow-up axis.
func ChainFollowUps() [chainFollowUpCount]ChainFollowUp {
	return [chainFollowUpCount]ChainFollowUp{ChainFollowUpSame, ChainFollowUpDifferent, ChainFollowUpDifferentNoAsk, ChainFollowUpSubjectless}
}

// ChainRow is one row of the decision table: a parent result kind and a chain
// state, and the typed outcome for each follow-up with the reason.
type ChainRow struct {
	Kind   contextfabric.SubjectSubstitutionParentResultKind
	Chain  contextfabric.SubjectSubstitutionParentChain
	Want   map[ChainFollowUp]contextfabric.SubjectSubstitutionOutcome
	Reason string
}

// ChainInapplicable is a (kind, chain) pair no stored parent can produce, and
// why. Together with the rows it covers the whole generated domain.
type ChainInapplicable struct {
	Kind   contextfabric.SubjectSubstitutionParentResultKind
	Chain  contextfabric.SubjectSubstitutionParentChain
	Reason string
}

func outcomes(same, different, noAsk, subjectless contextfabric.SubjectSubstitutionOutcome) map[ChainFollowUp]contextfabric.SubjectSubstitutionOutcome {
	return map[ChainFollowUp]contextfabric.SubjectSubstitutionOutcome{
		ChainFollowUpSame: same, ChainFollowUpDifferent: different, ChainFollowUpDifferentNoAsk: noAsk, ChainFollowUpSubjectless: subjectless,
	}
}

// ChainDecisionTable is the decision, one row per reachable (kind, chain).
func ChainDecisionTable() []ChainRow {
	const (
		same        = contextfabric.SubjectSubstitutionSameSubject
		clarified   = contextfabric.SubjectSubstitutionClarified
		refused     = contextfabric.SubjectSubstitutionRefused
		unavailable = contextfabric.SubjectSubstitutionClarifiedRememberedUnavailable
		refusedUn   = contextfabric.SubjectSubstitutionRefusedRememberedUnavailable
		nothing     = contextfabric.SubjectSubstitutionNoCommittedSubject
		noIdentity  = contextfabric.SubjectSubstitutionParentNoIdentity
	)
	failClosed := outcomes(unavailable, unavailable, refusedUn, nothing)
	return []ChainRow{
		{contextfabric.SubjectSubstitutionParentResultNone, contextfabric.SubjectSubstitutionChainNotAPrompt,
			outcomes(contextfabric.SubjectSubstitutionNoParentReference, contextfabric.SubjectSubstitutionNoParentReference, contextfabric.SubjectSubstitutionNoParentReference, contextfabric.SubjectSubstitutionNoParentReference),
			"the follow-up names no parent, so there is nothing to substitute"},
		{contextfabric.SubjectSubstitutionParentResultUnreadable, contextfabric.SubjectSubstitutionChainNotAPrompt,
			outcomes(contextfabric.SubjectSubstitutionParentUnreadable, contextfabric.SubjectSubstitutionParentUnreadable, contextfabric.SubjectSubstitutionParentUnreadable, contextfabric.SubjectSubstitutionParentUnreadable),
			"the named parent does not read; reported as such, not as a parent with no identity"},
		{contextfabric.SubjectSubstitutionParentResultAnswer, contextfabric.SubjectSubstitutionChainNotAPrompt,
			outcomes(same, clarified, refused, nothing),
			"an answer's own served identity is compared directly"},
		{contextfabric.SubjectSubstitutionParentResultRefusal, contextfabric.SubjectSubstitutionChainNotAPrompt,
			outcomes(noIdentity, noIdentity, noIdentity, noIdentity),
			"a refusal answered nothing and offers nothing to answer, so it asserts no identity and continues no chain"},
		{contextfabric.SubjectSubstitutionParentResultPrompt, contextfabric.SubjectSubstitutionChainVerified,
			outcomes(same, clarified, refused, nothing),
			"the prompt speaks for the answered result its verified receipt names, at any depth"},
		{contextfabric.SubjectSubstitutionParentResultPrompt, contextfabric.SubjectSubstitutionChainAbsent, failClosed,
			"a prompt with no carried member cannot say which subject its chain was about: fail closed"},
		{contextfabric.SubjectSubstitutionParentResultPrompt, contextfabric.SubjectSubstitutionChainMalformed, failClosed,
			"a member that does not decode consistently proves nothing: fail closed"},
		{contextfabric.SubjectSubstitutionParentResultPrompt, contextfabric.SubjectSubstitutionChainReceiptMismatch, failClosed,
			"a receipt not minted from the carried result and identity proves nothing: fail closed"},
		{contextfabric.SubjectSubstitutionParentResultPrompt, contextfabric.SubjectSubstitutionChainAnswerUnreadable, failClosed,
			"the answered result does not read, so its identity cannot be verified: fail closed"},
		{contextfabric.SubjectSubstitutionParentResultPrompt, contextfabric.SubjectSubstitutionChainAnswerMismatch, failClosed,
			"the answered result does not serve the carried identity: fail closed"},
		{contextfabric.SubjectSubstitutionParentResultPrompt, contextfabric.SubjectSubstitutionChainUnavailable, failClosed,
			"the prompt's own turn could not read or verify its parent, and carried that forward: fail closed"},
		{contextfabric.SubjectSubstitutionParentResultPrompt, contextfabric.SubjectSubstitutionChainNoIdentity,
			outcomes(noIdentity, noIdentity, noIdentity, noIdentity),
			"the chain's answer served no single identity, or the chain began with no parent: nothing to substitute"},
		{contextfabric.SubjectSubstitutionParentResultGuardClarification, contextfabric.SubjectSubstitutionChainGuardReceipt,
			outcomes(same, clarified, refused, nothing),
			"a guard clarification speaks for the answered result its remembered-offer receipt was minted from, at any depth"},
	}
}

// ChainInapplicableCells names every (kind, chain) pair the table has no row
// for, with the reason no stored parent produces it.
func ChainInapplicableCells() []ChainInapplicable {
	var out []ChainInapplicable
	kinds := contextfabric.SubjectSubstitutionParentResultKindVocabulary()
	chains := contextfabric.SubjectSubstitutionParentChainVocabulary()
	for _, kind := range kinds {
		for _, chain := range chains {
			reason := ""
			switch kind {
			case contextfabric.SubjectSubstitutionParentResultPrompt:
				switch chain {
				case contextfabric.SubjectSubstitutionChainNotAPrompt:
					reason = "a prompt is always read through its chain"
				case contextfabric.SubjectSubstitutionChainGuardReceipt:
					reason = "only a guard clarification lists a remembered-offer receipt"
				}
			case contextfabric.SubjectSubstitutionParentResultGuardClarification:
				if chain != contextfabric.SubjectSubstitutionChainGuardReceipt {
					reason = "a guard clarification is decided by its own remembered-offer receipt"
				}
			default:
				if chain != contextfabric.SubjectSubstitutionChainNotAPrompt {
					reason = "only a prompt continues a chain"
				}
			}
			if reason != "" {
				out = append(out, ChainInapplicable{Kind: kind, Chain: chain, Reason: reason})
			}
		}
	}
	return out
}

// ChainCell is one executed cell.
type ChainCell struct {
	Kind     contextfabric.SubjectSubstitutionParentResultKind
	Chain    contextfabric.SubjectSubstitutionParentChain
	Producer string
	FollowUp ChainFollowUp
	Depth    int
	// Observed from the follow-up's own ledger line and served result.
	Guard           string
	LineKind        string
	LineChain       string
	LineDepth       int
	LineParentID    string
	LineParentRes   string
	Served          string
	CommittedIDs    []string
	RememberedFirst bool
}

// Name renders the cell.
func (c ChainCell) Name() string {
	return fmt.Sprintf("%s/%s/%s/depth%d/%s", c.Kind, c.Chain, c.Producer, c.Depth, c.FollowUp)
}

// chainRig is one cell's store, engine and scripted ports.
type chainRig struct {
	t        *testing.T
	store    contextfabric.InvestigationResultStore
	seed     SemanticSeed
	engine   *contextfabric.Engine
	sink     *bytes.Buffer
	response chainResponse
	windowed bool
	next     int
	prefix   string
	// answered is the answered result the chain under test continues, ""
	// when the chain has none.
	answered string
}

type chainResponse struct {
	resolution contextfabric.SubjectResolution
	material   contextfabric.StructureOfferMaterial
	bases      contextfabric.CommitBasisSet
}

type chainInterpreter struct{}

func (chainInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	// A trend class gives a turn with no window of its own a class default,
	// which the window gate answers with a window prompt.
	return contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, WindowClass: contextfabric.WindowClassTrendAssessment},
		contextfabric.QuestionFamilyOutcome{Family: contextfabric.QuestionFamilyUnclassified, Source: contextfabric.QuestionFamilySourceNone}, nil
}

type chainGraph struct{ rig *chainRig }

func (chainGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "carried-parent-chain-key", Epoch: 0}, nil
}

func (g chainGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	r := g.rig.response
	return r.resolution, r.material, r.bases, nil, nil
}

func (chainGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{Paths: []contextfabric.RelationshipPath{}, Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}}}, nil
}

func newChainRig(t *testing.T, newStore func(t *testing.T) (contextfabric.InvestigationResultStore, SemanticSeed), prefix string) *chainRig {
	t.Helper()
	store, seed := newStore(t)
	rig := &chainRig{t: t, store: store, seed: seed, sink: &bytes.Buffer{}, prefix: sanitizeCellName(prefix)}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: chainInterpreter{}, Graph: chainGraph{rig: rig}, Facts: substitutionFacts{}, Synthesizer: substitutionSynth{},
		Results:   store,
		Telemetry: contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(rig.sink, &slog.HandlerOptions{Level: slog.LevelInfo}))),
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{
		ServiceVersion: "carried-parent-chain",
		Now:            func() time.Time { return time.Unix(900, 0).UTC() },
		NewResultID: func() string {
			rig.next++
			return fmt.Sprintf("result-chain-%s-%02d", rig.prefix, rig.next)
		},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	rig.engine = engine
	return rig
}

func commitResponse(subjects ...contextfabric.SubjectRef) chainResponse {
	bases := contextfabric.CommitBasisSet{}
	candidates := []contextfabric.SubjectCandidate{}
	for i, subject := range subjects {
		bases.Record(subject, contextfabric.CommitBasisAuthoritativeIdentity)
		candidates = append(candidates, contextfabric.SubjectCandidate{
			ReceiptID: fmt.Sprintf("receipt-chain-%s-%d", subject.CanonicalID, i), Subject: subject, State: contractsv1.ContextFabricResolutionCommitted,
			MatchedTerms: []string{"project"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
		})
	}
	return chainResponse{resolution: contextfabric.SubjectResolution{Committed: append([]contextfabric.SubjectRef{}, subjects...), Candidates: candidates}, bases: bases}
}

func emptyResponse() chainResponse {
	return chainResponse{resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{}, Candidates: []contextfabric.SubjectCandidate{}}}
}

// ambiguousResponse commits nothing and offers two candidates, which the
// engine answers with a subject clarification prompt.
func ambiguousResponse() chainResponse {
	candidate := func(subject contextfabric.SubjectRef, receipt string) contextfabric.SubjectCandidate {
		return contextfabric.SubjectCandidate{ReceiptID: receipt, Subject: subject, State: contractsv1.ContextFabricResolutionAmbiguous,
			MatchedTerms: []string{"project"}, MatchReasons: []string{"matched"}, Confidence: 0.6, EvidenceRefIDs: []string{}}
	}
	return chainResponse{resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{},
		Candidates: []contextfabric.SubjectCandidate{candidate(chainOther, "receipt-chain-ambiguous-one"), candidate(chainOtherTwo, "receipt-chain-ambiguous-two")}}}
}

// kindOfferResponse commits nothing and offers only an expected-kind choice,
// which the engine answers with a kind prompt.
func kindOfferResponse() chainResponse {
	r := emptyResponse()
	// Retrieval found candidates and withheld them all, so the turn's only
	// redeemable offer is the kind choice.
	r.resolution.ClarificationPrompt = contextfabric.OfferPoolEmptiedClarificationPrompt
	r.material = contextfabric.StructureOfferMaterial{
		Missing:     []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []contractsv1.ContextFabricKindOption{{Kind: contractsv1.ContextFabricSubjectKind(contextfabric.SubjectProject), Label: "project", OfferSource: contractsv1.ContextFabricStructureOfferEngine}, {Kind: contractsv1.ContextFabricSubjectKind(contextfabric.SubjectTeam), Label: "team", OfferSource: contractsv1.ContextFabricStructureOfferEngine}},
	}
	return r
}

// turn runs one request through the engine and returns the served result.
func (r *chainRig) turn(parentID string, statedWindow, allowClarification bool, response chainResponse) contextfabric.InvestigationResult {
	r.t.Helper()
	r.response = response
	r.next++
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1,
		RequestID:     fmt.Sprintf("request-chain-%s-%02d", r.prefix, r.next),
		Question:      fmt.Sprintf("How is it going, turn %d?", r.next),
		TimeContext:   contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: allowClarification,
		},
		Consumer:       contextfabric.ConsumerInfo{Name: "context-fabric-workbench", Version: "0.1.0", Surface: "workbench"},
		ParentResultID: parentID,
	}
	if statedWindow {
		request.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	}
	served, err := r.engine.Investigate(context.Background(), chainPrincipal, request)
	if err != nil {
		r.t.Fatalf("%s: Investigate: %v", r.prefix, err)
	}
	return served
}

// promptTurn runs a turn that must end in a clarification prompt which
// carries its chain member, and returns its result id.
func (r *chainRig) promptTurn(producer, parentID string) string {
	r.t.Helper()
	var served contextfabric.InvestigationResult
	switch producer {
	case "window_prompt":
		served = r.turn(parentID, false, true, commitResponse(chainOther))
	case "candidate_prompt":
		served = r.turn(parentID, true, true, ambiguousResponse())
	case "kind_prompt":
		served = r.turn(parentID, true, true, kindOfferResponse())
	default:
		r.t.Fatalf("unknown prompt producer %q", producer)
	}
	if served.Status != contextfabric.InvestigationClarificationRequired || len(served.SubjectResolution.Committed) != 0 {
		r.t.Fatalf("%s: fixture defect: producer %s served status %q with %d committed, want a prompt", r.prefix, producer, served.Status, len(served.SubjectResolution.Committed))
	}
	line := r.lastLine(chainPersistenceLogMessage)
	if line["carried_parent"] != "attached" {
		r.t.Fatalf("%s: producer %s saved carried_parent=%v, want attached", r.prefix, producer, line["carried_parent"])
	}
	return served.ResultID
}

// lastLine is the most recent log line with msg.
func (r *chainRig) lastLine(msg string) map[string]any {
	r.t.Helper()
	var last map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(r.sink.Bytes()))
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<22)
	for scanner.Scan() {
		var line map[string]any
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if line["msg"] == msg {
			last = line
		}
	}
	if last == nil {
		r.t.Fatalf("%s: no %q line", r.prefix, msg)
	}
	return last
}

// reseed rewrites a stored row through the store's own raw seed.
func (r *chainRig) reseed(resultID string, mutatePayload func(*contextfabric.InvestigationResult), mutateState func(*contextfabric.PersistedSemanticState) []byte) {
	r.t.Helper()
	stored, err := r.store.Get(context.Background(), chainPrincipal, resultID)
	if err != nil {
		r.t.Fatalf("%s: reseed Get(%s): %v", r.prefix, resultID, err)
	}
	result := stored.Result
	if mutatePayload != nil {
		mutatePayload(&result)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		r.t.Fatalf("%s: reseed marshal: %v", r.prefix, err)
	}
	var snapshot []byte
	if mutateState != nil {
		snapshot = mutateState(stored.SemanticState)
	} else if stored.SemanticState != nil {
		snapshot, err = contextfabric.EncodeSemanticState(stored.SemanticState)
		if err != nil {
			r.t.Fatalf("%s: reseed encode: %v", r.prefix, err)
		}
	}
	r.seed(r.t, chainPrincipal.OrgID, resultID, payload, snapshot)
}

// rewriteMember re-encodes the snapshot with the carried member replaced by
// raw (removed when raw is nil).
func (r *chainRig) rewriteMember(state *contextfabric.PersistedSemanticState, raw func(member map[string]any) []byte) []byte {
	r.t.Helper()
	if state == nil {
		r.t.Fatalf("%s: fixture defect: the prompt saved no snapshot", r.prefix)
	}
	copied := *state
	copied.Extensions = contextfabric.SemanticStateExtensions{}
	var member map[string]any
	for name, value := range state.Extensions {
		if name == "carried_parent_identity" {
			if err := json.Unmarshal(value, &member); err != nil {
				r.t.Fatalf("%s: member decode: %v", r.prefix, err)
			}
			continue
		}
		copied.Extensions[name] = value
	}
	if member == nil {
		r.t.Fatalf("%s: fixture defect: the prompt carries no member", r.prefix)
	}
	if replaced := raw(member); replaced != nil {
		copied.Extensions["carried_parent_identity"] = replaced
	}
	encoded, err := contextfabric.EncodeSemanticState(&copied)
	if err != nil {
		r.t.Fatalf("%s: re-encode: %v", r.prefix, err)
	}
	return encoded
}

// chainProducers is how each (kind, chain) row is realised: the named
// parent's producer, and for prompt rows the receipt state's own
// realisation.
func chainProducers(kind contextfabric.SubjectSubstitutionParentResultKind) []string {
	switch kind {
	case contextfabric.SubjectSubstitutionParentResultNone:
		return []string{"no_parent"}
	case contextfabric.SubjectSubstitutionParentResultUnreadable:
		return []string{"missing_parent"}
	case contextfabric.SubjectSubstitutionParentResultAnswer:
		return []string{"answer"}
	case contextfabric.SubjectSubstitutionParentResultRefusal:
		return []string{"no_match", "window_prompt_refused"}
	case contextfabric.SubjectSubstitutionParentResultPrompt:
		return []string{"window_prompt", "candidate_prompt", "kind_prompt"}
	case contextfabric.SubjectSubstitutionParentResultGuardClarification:
		return []string{"guard_clarification"}
	}
	return nil
}

// buildChain builds the chain under the named parent and returns the id the
// follow-up names ("" for none).
func (r *chainRig) buildChain(row ChainRow, producer string, depth int) string {
	r.t.Helper()
	switch producer {
	case "no_parent":
		return ""
	case "missing_parent":
		return "result-chain-never-saved-" + r.prefix
	case "answer":
		r.answered = r.turn("", true, true, commitResponse(chainSubject)).ResultID
		return r.answered
	case "no_match":
		answered := r.turn("", true, true, commitResponse(chainSubject)).ResultID
		served := r.turn(answered, true, true, emptyResponse())
		if served.Status != contextfabric.InvestigationNoMatch {
			r.t.Fatalf("%s: fixture defect: no_match producer served %q", r.prefix, served.Status)
		}
		return served.ResultID
	case "window_prompt_refused":
		answered := r.turn("", true, true, commitResponse(chainSubject)).ResultID
		served := r.turn(answered, false, false, commitResponse(chainOther))
		if served.Status != contextfabric.InvestigationNoMatch {
			r.t.Fatalf("%s: fixture defect: refused window producer served %q", r.prefix, served.Status)
		}
		return served.ResultID
	}
	// A chain: the head, depth-1 window prompts, then the named parent.
	head := ""
	switch row.Chain {
	case contextfabric.SubjectSubstitutionChainNoIdentity:
		// Alternate the two ways a chain carries no identity: an answer
		// that served several subjects, and a chain that began with no
		// parent at all.
		if depth%2 == 0 {
			head = r.turn("", true, true, commitResponse(chainSubject, chainOtherTwo)).ResultID
		}
	case contextfabric.SubjectSubstitutionChainUnavailable:
		head = "result-chain-never-saved-" + r.prefix
	default:
		head = r.turn("", true, true, commitResponse(chainSubject)).ResultID
		r.answered = head
	}
	current := head
	for i := 1; i < depth; i++ {
		current = r.promptTurn("window_prompt", current)
	}
	if producer == "guard_clarification" {
		served := r.turn(current, true, true, commitResponse(chainOther))
		if served.Status != contextfabric.InvestigationClarificationRequired || len(served.SubjectResolution.Committed) != 0 {
			r.t.Fatalf("%s: fixture defect: the guard did not clarify (status %q)", r.prefix, served.Status)
		}
		return served.ResultID
	}
	named := r.promptTurn(producer, current)
	switch row.Chain {
	case contextfabric.SubjectSubstitutionChainAbsent:
		if depth%2 == 0 {
			r.reseed(named, nil, func(state *contextfabric.PersistedSemanticState) []byte { return nil })
		} else {
			r.reseed(named, nil, func(state *contextfabric.PersistedSemanticState) []byte {
				return r.rewriteMember(state, func(map[string]any) []byte { return nil })
			})
		}
	case contextfabric.SubjectSubstitutionChainMalformed:
		r.reseed(named, nil, func(state *contextfabric.PersistedSemanticState) []byte {
			return r.rewriteMember(state, func(member map[string]any) []byte {
				member["depth"] = 0
				encoded, _ := json.Marshal(member)
				return encoded
			})
		})
	case contextfabric.SubjectSubstitutionChainReceiptMismatch:
		r.reseed(named, nil, func(state *contextfabric.PersistedSemanticState) []byte {
			return r.rewriteMember(state, func(member map[string]any) []byte {
				member["receipt_id"] = "subr_000000000000000000000000"
				encoded, _ := json.Marshal(member)
				return encoded
			})
		})
	case contextfabric.SubjectSubstitutionChainAnswerUnreadable:
		r.seed(r.t, chainPrincipal.OrgID, head, []byte(`{"schema_version":"context_fabric_investigation_result.v1"}`), nil)
	case contextfabric.SubjectSubstitutionChainAnswerMismatch:
		// The answered row serves another identity than the one the
		// prompt carried: a real answer about another subject, stored under
		// the answered result's id.
		other := r.turn("", true, true, commitResponse(chainOtherTwo))
		r.reseed(other.ResultID, nil, nil)
		stored, err := r.store.Get(context.Background(), chainPrincipal, other.ResultID)
		if err != nil {
			r.t.Fatalf("%s: read the other answer: %v", r.prefix, err)
		}
		replaced := stored.Result
		replaced.ResultID = head
		payload, err := json.Marshal(replaced)
		if err != nil {
			r.t.Fatalf("%s: marshal: %v", r.prefix, err)
		}
		r.seed(r.t, chainPrincipal.OrgID, head, payload, nil)
		if check, err := r.store.Get(context.Background(), chainPrincipal, head); err != nil || len(check.Result.SubjectResolution.Committed) != 1 || check.Result.SubjectResolution.Committed[0].CanonicalID != chainOtherTwo.CanonicalID {
			r.t.Fatalf("%s: fixture defect: the replaced answer does not read as another subject: %v", r.prefix, err)
		}
	}
	return named
}

// followUp runs the follow-up turn for one cell.
func (r *chainRig) followUp(parentID string, followUp ChainFollowUp) contextfabric.InvestigationResult {
	r.t.Helper()
	switch followUp {
	case ChainFollowUpSame:
		return r.turn(parentID, true, true, commitResponse(chainSubject))
	case ChainFollowUpDifferent:
		return r.turn(parentID, true, true, commitResponse(chainOtherTwo))
	case ChainFollowUpDifferentNoAsk:
		return r.turn(parentID, true, false, commitResponse(chainOtherTwo))
	default:
		return r.turn(parentID, true, true, emptyResponse())
	}
}

func chainDepths(kind contextfabric.SubjectSubstitutionParentResultKind) []int {
	if kind == contextfabric.SubjectSubstitutionParentResultPrompt || kind == contextfabric.SubjectSubstitutionParentResultGuardClarification {
		return []int{1, 2, chainMaxDepth}
	}
	return []int{chainNotAPromptDepth}
}

// RunCarriedParentChainSuite executes the whole generated domain against one
// store and returns every cell. It fails when a cell's typed outcome, its
// served shape, or its published pre-decision fields disagree with the
// decision table.
func RunCarriedParentChainSuite(t *testing.T, newStore func(t *testing.T) (contextfabric.InvestigationResultStore, SemanticSeed)) []ChainCell {
	t.Helper()
	var cells []ChainCell
	for _, row := range ChainDecisionTable() {
		for _, producer := range chainProducers(row.Kind) {
			for _, depth := range chainDepths(row.Kind) {
				for _, followUp := range ChainFollowUps() {
					cell := ChainCell{Kind: row.Kind, Chain: row.Chain, Producer: producer, FollowUp: followUp, Depth: depth}
					rig := newChainRig(t, newStore, cell.Name())
					parentID := rig.buildChain(row, producer, depth)
					served := rig.followUp(parentID, followUp)
					line := rig.lastLine(chainLedgerLogMessage)
					cell.Guard, _ = line["substitution_guard"].(string)
					cell.LineKind, _ = line["substitution_parent_result_kind"].(string)
					cell.LineChain, _ = line["substitution_parent_chain"].(string)
					if depthValue, ok := line["substitution_parent_chain_depth"].(float64); ok {
						cell.LineDepth = int(depthValue)
					}
					cell.LineParentID, _ = line["substitution_parent_id"].(string)
					cell.LineParentRes, _ = line["substitution_parent_result_id"].(string)
					for _, subject := range served.SubjectResolution.Committed {
						cell.CommittedIDs = append(cell.CommittedIDs, string(subject.Kind)+":"+subject.CanonicalID)
					}
					cell.Served = chainServedShape(served)
					cell.RememberedFirst = len(served.SubjectResolution.Candidates) > 0 &&
						served.SubjectResolution.Candidates[0].Subject.CanonicalID == chainSubject.CanonicalID &&
						strings.HasPrefix(served.SubjectResolution.Candidates[0].ReceiptID, "subr_")
					assertChainCell(t, row, cell, served, rig.answered)
					cells = append(cells, cell)
				}
			}
		}
	}
	return cells
}

func chainServedShape(served contextfabric.InvestigationResult) string {
	switch {
	case len(served.SubjectResolution.Committed) > 0:
		return chainStatusServed
	case served.Status == contextfabric.InvestigationClarificationRequired:
		return chainStatusClarified
	case served.Status == contextfabric.InvestigationNoMatch:
		return chainStatusRefused
	default:
		return chainStatusUnresolved
	}
}

func assertChainCell(t *testing.T, row ChainRow, cell ChainCell, served contextfabric.InvestigationResult, answered string) {
	t.Helper()
	name := cell.Name()
	want := row.Want[cell.FollowUp]
	if cell.Guard != string(want) {
		t.Errorf("%s: substitution_guard = %q, want %q (%s)", name, cell.Guard, want, row.Reason)
	}
	if cell.LineKind != string(row.Kind) || cell.LineChain != string(row.Chain) {
		t.Errorf("%s: line kind/chain = %q/%q, want %q/%q", name, cell.LineKind, cell.LineChain, row.Kind, row.Chain)
	}
	// A prompt whose member does not read still counts itself: depth 1.
	wantDepth := cell.Depth
	if row.Chain == contextfabric.SubjectSubstitutionChainAbsent || row.Chain == contextfabric.SubjectSubstitutionChainMalformed {
		wantDepth = 1
	}
	if cell.LineDepth != wantDepth {
		t.Errorf("%s: substitution_parent_chain_depth = %d, want %d", name, cell.LineDepth, wantDepth)
	}
	outcome := contextfabric.SubjectSubstitutionOutcome(cell.Guard)
	switch {
	case outcome.Fired():
		// The invariant: a fired guard serves nothing.
		if len(cell.CommittedIDs) != 0 || len(served.ClaimedFacts) != 0 {
			t.Errorf("%s: fired guard served %v", name, cell.CommittedIDs)
		}
		listed := outcome == contextfabric.SubjectSubstitutionClarified || outcome == contextfabric.SubjectSubstitutionRefused
		if listed != cell.RememberedFirst {
			t.Errorf("%s: remembered subject listed first = %t, want %t", name, cell.RememberedFirst, listed)
		}
		if listed && (cell.LineParentID != chainSubject.CanonicalID || cell.LineParentRes != answered) {
			t.Errorf("%s: fired on parent %q of %q, want %q of the answered result %q", name, cell.LineParentID, cell.LineParentRes, chainSubject.CanonicalID, answered)
		}
		if !listed && cell.LineParentID != "" {
			t.Errorf("%s: fail-closed parent identity = %q, want none", name, cell.LineParentID)
		}
	case outcome == contextfabric.SubjectSubstitutionSameSubject:
		if cell.Served != chainStatusServed || len(cell.CommittedIDs) != 1 || cell.CommittedIDs[0] != string(chainSubject.Kind)+":"+chainSubject.CanonicalID {
			t.Errorf("%s: same subject served %v (%s)", name, cell.CommittedIDs, cell.Served)
		}
		if cell.LineParentRes != answered {
			t.Errorf("%s: same subject decided against parent result %q, want the answered result %q", name, cell.LineParentRes, answered)
		}
	}
	// A prompt is never an identity of its own: whatever parent result the
	// line names served the subject it names.
	if cell.LineParentRes != "" && cell.LineParentID != "" && cell.LineParentID != chainSubject.CanonicalID {
		t.Errorf("%s: parent identity %q is not the answered subject", name, cell.LineParentID)
	}
}

// ChainDomain is the generated domain: every (kind, chain) pair from the two
// closed vocabularies, in vocabulary order, as "kind/chain".
func ChainDomain() []string {
	var out []string
	kinds := contextfabric.SubjectSubstitutionParentResultKindVocabulary()
	chains := contextfabric.SubjectSubstitutionParentChainVocabulary()
	for _, kind := range kinds {
		for _, chain := range chains {
			out = append(out, string(kind)+"/"+string(chain))
		}
	}
	return out
}

// ChainTableCoverage is the (kind, chain) pairs the table and the
// inapplicable list name, sorted, each once.
func ChainTableCoverage() (covered []string, duplicates []string) {
	seen := map[string]int{}
	for _, row := range ChainDecisionTable() {
		seen[string(row.Kind)+"/"+string(row.Chain)]++
	}
	for _, cell := range ChainInapplicableCells() {
		seen[string(cell.Kind)+"/"+string(cell.Chain)]++
	}
	for key, count := range seen {
		covered = append(covered, key)
		if count > 1 {
			duplicates = append(duplicates, key)
		}
	}
	sort.Strings(covered)
	sort.Strings(duplicates)
	return covered, duplicates
}
