package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// storedStateFor builds the persisted reading a turn with this frame and
// anchor kind accepts -- the frame validated and gated as interpretation does,
// the snapshot passed through the codec -- so the state a test hands the read
// side is one a production row could hold.
func storedStateFor(t *testing.T, frame *QuestionFrame, anchorKind SubjectKind) *PersistedSemanticState {
	t.Helper()
	proposed := *frame
	if len(proposed.Goals) == 0 {
		proposed.Goals = []InvestigationGoal{GoalAssessState}
		if proposed.SubjectExpression.Kind == SubjectExpressionOrganizationScope && proposed.SubjectExpression.Org != nil && proposed.SubjectExpression.Org.MemberKind != nil {
			proposed.Goals = []InvestigationGoal{GoalCountOrAggregate}
		}
	}
	if proposed.Temporal == "" {
		proposed.Temporal = TemporalIntentCurrent
	}
	validated := ValidateFrame(proposed, nil, ShapeOpen)
	if validated.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture defect: frame invalid (%v %v)", validated.Failure.Invariant, validated.Failure.Detail)
	}
	state := BuildSemanticState(SemanticStateInput{
		Outcome: QuestionFamilyOutcome{
			Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceModel, Frame: &validated.Frame,
			Gate: DecideFrameGate(validated, true), WinningSample: FamilySample{ScopeAnchorKind: anchorKind, ScopeAnchorTerm: "anchor-term"},
		},
		EmittedShape:    ShapeOpen,
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: carrierRequestIdentity("stored question"),
	})
	if groupKind, ok := validated.Frame.SubjectExpression.GroupKind(); ok {
		state.GroupKind = groupKind
	}
	column, err := SemanticStateOf(state).EncodedColumn()
	if err != nil {
		t.Fatalf("fixture defect: the reading does not encode: %v", err)
	}
	decoded, status := DecodeSemanticState(column)
	if status != SemanticStateReadAvailable {
		t.Fatalf("fixture defect: the reading decodes as %q", status)
	}
	return decoded
}

// storedClarification is a stored clarification serving the given subject
// candidates, with the clarification sentence a composing turn attaches.
func storedClarification(candidates ...SubjectCandidate) InvestigationResult {
	return InvestigationResult{
		ResultID:            "result_stored_clarification",
		Status:              InvestigationClarificationRequired,
		SubjectResolution:   SubjectResolution{Candidates: candidates, Committed: []SubjectRef{}},
		Limitations:         []string{clarificationRequiredLimitation},
		DeterministicAnswer: "Clarification is required before this question can be answered.",
	}
}

func storedCIRunCandidates() []SubjectCandidate { return chaos5660CIRunCandidates(2) }

// TestStoredAnswerabilityOverTheWholeReadDomain executes every cell of the
// read side in one pass: the stored status, the read status of the persisted
// reading (every vocabulary member, the empty value, and "available" with no
// state), and the offers (none, window only, advancing, not advancing).
func TestStoredAnswerabilityOverTheWholeReadDomain(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	named := storedStateFor(t, chaos5660NamedFrame(&project), "")
	projectCandidate := roleCandidate("subr_project", SubjectProject, "project:1")
	windowOnly := storedClarification()
	windowOnly.WindowClarification = &contractsv1.ContextFabricWindowClarification{Options: []contractsv1.ContextFabricWindowOption{{}}}
	noFrame := BuildSemanticState(SemanticStateInput{
		Outcome:      QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceModel, Gate: FrameGate{Outcome: FrameGateNotEvaluated}},
		EmittedShape: ShapeOpen, FamilyVersion: QuestionFamilyTableVersion, RequestIdentity: carrierRequestIdentity("stored question"),
	})
	if _, err := EncodeSemanticState(noFrame); err != nil {
		t.Fatalf("fixture defect: frameless reading does not encode: %v", err)
	}

	for _, testCase := range []struct {
		cell          string
		result        InvestigationResult
		state         *PersistedSemanticState
		read          SemanticStateReadStatus
		determination StoredAnswerabilityDetermination
		reading       string
	}{
		{"complete result", InvestigationResult{Status: InvestigationComplete}, named, SemanticStateReadAvailable, StoredAnswerabilityNotApplicable, "not_read"},
		{"no_match result", InvestigationResult{Status: InvestigationNoMatch, SubjectResolution: SubjectResolution{Candidates: storedCIRunCandidates()}}, named, SemanticStateReadAvailable, StoredAnswerabilityNotApplicable, "not_read"},
		{"partial result", InvestigationResult{Status: InvestigationPartial}, nil, SemanticStateReadAbsent, StoredAnswerabilityNotApplicable, "not_read"},
		{"clarification, wrong-kind offers, reading available", storedClarification(storedCIRunCandidates()...), named, SemanticStateReadAvailable, StoredAnswerabilityUnanswerable, "available"},
		{"clarification, declared-kind offer, reading available", storedClarification(append(storedCIRunCandidates(), projectCandidate)...), named, SemanticStateReadAvailable, StoredAnswerabilityAnswerable, "available"},
		{"clarification, reading with no frame", storedClarification(storedCIRunCandidates()...), noFrame, SemanticStateReadAvailable, StoredAnswerabilityAnswerable, "available"},
		{"clarification, available status with no state", storedClarification(storedCIRunCandidates()...), nil, SemanticStateReadAvailable, StoredAnswerabilityUnavailable, "unreported"},
		{"clarification, reading absent", storedClarification(storedCIRunCandidates()...), nil, SemanticStateReadAbsent, StoredAnswerabilityUnavailable, "absent"},
		{"clarification, reading malformed", storedClarification(storedCIRunCandidates()...), nil, SemanticStateReadMalformed, StoredAnswerabilityUnavailable, "malformed"},
		{"clarification, reading oversized", storedClarification(storedCIRunCandidates()...), nil, SemanticStateReadOversized, StoredAnswerabilityUnavailable, "oversized"},
		{"clarification, reading unsupported version", storedClarification(storedCIRunCandidates()...), nil, SemanticStateReadUnsupportedVersion, StoredAnswerabilityUnavailable, "unsupported_version"},
		{"clarification, read status empty", storedClarification(storedCIRunCandidates()...), nil, "", StoredAnswerabilityUnavailable, "unreported"},
		{"clarification, a non-available status with a state present", storedClarification(storedCIRunCandidates()...), named, SemanticStateReadMalformed, StoredAnswerabilityUnavailable, "malformed"},
		{"clarification, window options only, no reading", windowOnly, nil, SemanticStateReadAbsent, StoredAnswerabilityAnswerable, "not_read"},
		{"clarification, offers with empty kinds only, no reading", storedClarification(roleCandidate("subr_empty", "", "x")), nil, SemanticStateReadAbsent, StoredAnswerabilityAnswerable, "not_read"},
		{"clarification, structure options of the wrong kind, reading available", func() InvestigationResult {
			result := storedClarification()
			result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
				Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectHandle},
				HandleOptions: []contractsv1.ContextFabricHandleOption{chaos5660HandleOption(contractsv1.ContextFabricSubjectCIRun)},
			}
			return result
		}(), named, SemanticStateReadAvailable, StoredAnswerabilityUnanswerable, "available"},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			got := DecideStoredAnswerability(testCase.result, testCase.state, testCase.read)
			if got.Determination != testCase.determination || got.Reading != testCase.reading {
				t.Fatalf("determination/reading = %q/%q, want %q/%q", got.Determination, got.Reading, testCase.determination, testCase.reading)
			}
		})
	}
}

// TestTheReadAdapterAndTheComposingAdapterTakeOneDecision is the one-predicate
// claim, executed: a turn is composed fresh through the engine, its SAVED
// document and the persisted form of the reading that turn accepted are handed
// to the read side, and the read side must reach the fresh terminal's own
// decision, value for value. A
// refused turn is read back as the clarification an earlier build composed
// for the same offers, which is exactly the stored row the read side exists
// for.
func TestTheReadAdapterAndTheComposingAdapterTakeOneDecision(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	for _, testCase := range []struct {
		cell       string
		frame      *QuestionFrame
		anchorKind SubjectKind
		resolution SubjectResolution
		material   StructureOfferMaterial
		refused    bool
	}{
		{"scoped: team anchors", roleScopedFrame(SubjectProject), SubjectTeam, SubjectResolution{Candidates: roleTwoTeamAnchors(), Committed: []SubjectRef{}}, StructureOfferMaterial{}, false},
		{"scoped: member-kind candidates", roleScopedFrame(SubjectProject), SubjectTeam, SubjectResolution{Candidates: []SubjectCandidate{roleCandidate("subr_role_parity_p", SubjectProject, "project:p")}, Committed: []SubjectRef{}}, StructureOfferMaterial{}, true},
		{"grouped: group-kind candidates", roleGroupedFrame(SubjectProject, SubjectTeam), "", SubjectResolution{Candidates: roleTwoTeamAnchors(), Committed: []SubjectRef{}}, StructureOfferMaterial{}, true},
		{"named: wrong kinds across channels", chaos5660NamedFrame(&project), "", SubjectResolution{Candidates: chaos5660CIRunCandidates(2), Committed: []SubjectRef{}}, chaos5660MeasuredOffers(), true},
		{"named: a declared-kind candidate", chaos5660NamedFrame(&project), "", SubjectResolution{Candidates: append(chaos5660CIRunCandidates(1), roleCandidate("subr_role_parity_q", SubjectProject, "project:q")), Committed: []SubjectRef{}}, chaos5660MeasuredOffers(), false},
		{"organization scope", roleOrgFrameWithGoals(nil, GoalAssessState), "", SubjectResolution{Candidates: chaos5660CIRunCandidates(1), Committed: []SubjectRef{}}, StructureOfferMaterial{}, true},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			store := &staticResultStore{results: map[string]InvestigationResult{}}
			telemetry := &recordingTelemetry{}
			factReads := 0
			engine := roleEngine(t, roleInterpreter{frame: testCase.frame, anchorKind: testCase.anchorKind}, &roleGraph{first: testCase.resolution, material: testCase.material}, store, telemetry, &factReads)
			fresh, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_role"}, validInvestigationRequestWithConfirmedWindow())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if store.saved == nil {
				t.Fatal("fixture defect: the terminal saved no result")
			}
			state, read := storedStateFor(t, testCase.frame, testCase.anchorKind), SemanticStateReadAvailable
			stored := *store.saved
			if testCase.refused {
				if stored.Status != InvestigationNoMatch || (stored.RefusalBasis != declaredKindTerminalBasis && stored.RefusalBasis != organizationScopeTerminalBasis) {
					t.Fatalf("fixture defect: fresh status/basis = %q/%q, want the declared-kind refusal", stored.Status, stored.RefusalBasis)
				}
				stored.Status = InvestigationClarificationRequired
				stored.RefusalBasis = ""
			} else if fresh.Status != InvestigationClarificationRequired {
				t.Fatalf("fixture defect: fresh status = %q, want clarification_required", fresh.Status)
			}
			got := DecideStoredAnswerability(stored, state, read)
			want := StoredAnswerabilityAnswerable
			if testCase.refused {
				want = StoredAnswerabilityUnanswerable
			}
			if got.Determination != want {
				t.Fatalf("read-side determination = %q, want %q", got.Determination, want)
			}
			if got.ObservableAnswerability() != telemetry.subjectlessTerminalAnswerability[0] {
				t.Fatalf("read-side decision %+v differs from the fresh terminal's %+v", got.ObservableAnswerability(), telemetry.subjectlessTerminalAnswerability[0])
			}
		})
	}
}

// TestAStoredClarificationIsRepairedOnReadOnlyWhenItsReadingRefusesIt pins the
// repair at the unit the route calls, including every arm that must not fire.
func TestAStoredClarificationIsRepairedOnReadOnlyWhenItsReadingRefusesIt(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	named := storedStateFor(t, chaos5660NamedFrame(&project), "")

	t.Run("unanswerable: the served copy becomes the fresh terminal", func(t *testing.T) {
		t.Parallel()
		stored := storedClarification(storedCIRunCandidates()...)
		stored.Limitations = []string{"A retrieval mechanism was unavailable.", clarificationRequiredLimitation}
		stored.SubjectResolution.ClarificationPrompt = "Which subject did you mean?"
		original := append([]string(nil), stored.Limitations...)
		served := stored
		served.Limitations = append([]string(nil), stored.Limitations...)
		got := RepairStoredClarification(&served, named, SemanticStateReadAvailable)
		if got.Determination != StoredAnswerabilityUnanswerable || !got.Repaired {
			t.Fatalf("determination/repaired = %q/%v, want unanswerable/true", got.Determination, got.Repaired)
		}
		if served.Status != InvestigationNoMatch || served.RefusalBasis != declaredKindTerminalBasis {
			t.Fatalf("served status/basis = %q/%q, want no_match/declared_kind_unmatched", served.Status, served.RefusalBasis)
		}
		if len(served.Limitations) != 2 || served.Limitations[0] != original[0] || served.Limitations[1] != declaredKindTerminalLimitation {
			t.Fatalf("served limitations = %#v, want the clarification sentence replaced in place", served.Limitations)
		}
		if served.DeterministicAnswer != statusSentence(InvestigationNoMatch, served.SubjectResolution) {
			t.Fatalf("served answer = %q, want the no_match status sentence", served.DeterministicAnswer)
		}
		if served.SubjectResolution.ClarificationPrompt == "" || len(served.SubjectResolution.Candidates) != 2 {
			t.Fatal("the repair removed the stored prompt or candidates")
		}
	})
	t.Run("an organization-scope reading that counts nothing is repaired on its own basis", func(t *testing.T) {
		t.Parallel()
		orgReading := storedStateFor(t, roleOrgFrameWithGoals(nil, GoalAssessState), "")
		served := storedClarification(storedCIRunCandidates()...)
		got := RepairStoredClarification(&served, orgReading, SemanticStateReadAvailable)
		if got.Determination != StoredAnswerabilityUnanswerable || !got.Repaired {
			t.Fatalf("determination/repaired = %q/%v, want unanswerable/true", got.Determination, got.Repaired)
		}
		if served.RefusalBasis != organizationScopeTerminalBasis || !roleContains(served.Limitations, organizationScopeTerminalLimitation) || roleContains(served.Limitations, declaredKindTerminalLimitation) {
			t.Fatalf("served basis/limitations = %q/%#v, want organization_scope_unsupported with its own sentence only", served.RefusalBasis, served.Limitations)
		}
	})
	t.Run("unanswerable with no clarification sentence: the basis sentence is added", func(t *testing.T) {
		t.Parallel()
		served := storedClarification(storedCIRunCandidates()...)
		served.Limitations = []string{}
		RepairStoredClarification(&served, named, SemanticStateReadAvailable)
		if len(served.Limitations) != 1 || served.Limitations[0] != declaredKindTerminalLimitation {
			t.Fatalf("served limitations = %#v, want the basis sentence", served.Limitations)
		}
	})
	for _, testCase := range []struct {
		cell   string
		result InvestigationResult
		state  *PersistedSemanticState
		read   SemanticStateReadStatus
		want   StoredAnswerabilityDetermination
	}{
		{"answerable", storedClarification(roleCandidate("subr_project", SubjectProject, "project:1")), named, SemanticStateReadAvailable, StoredAnswerabilityAnswerable},
		{"unavailable (legacy row)", storedClarification(storedCIRunCandidates()...), nil, SemanticStateReadAbsent, StoredAnswerabilityUnavailable},
		{"not a clarification", InvestigationResult{Status: InvestigationComplete, DeterministicAnswer: "done"}, named, SemanticStateReadAvailable, StoredAnswerabilityNotApplicable},
	} {
		t.Run(testCase.cell+" is untouched", func(t *testing.T) {
			t.Parallel()
			served := testCase.result
			before, _ := json.Marshal(served)
			got := RepairStoredClarification(&served, testCase.state, testCase.read)
			after, _ := json.Marshal(served)
			if got.Determination != testCase.want || got.Repaired || !bytes.Equal(before, after) {
				t.Fatalf("determination/repaired = %q/%v (want %q/false), document changed = %v", got.Determination, got.Repaired, testCase.want, !bytes.Equal(before, after))
			}
		})
	}
	t.Run("nil is a no-op", func(t *testing.T) {
		t.Parallel()
		if got := RepairStoredClarification(nil, named, SemanticStateReadAvailable); got.Repaired || got.Determination != StoredAnswerabilityNotApplicable {
			t.Fatalf("nil repair = %+v", got)
		}
	})
}

// storedAnswerabilityStore serves one reuse candidate's persisted reading, or
// fails its Get.
type storedAnswerabilityStore struct {
	resultStoreStub
	state  *PersistedSemanticState
	read   SemanticStateReadStatus
	getErr error
	gets   int
}

func (s *storedAnswerabilityStore) Get(_ context.Context, _ storage.Principal, _ string) (StoredInvestigationResult, error) {
	s.gets++
	if s.getErr != nil {
		return StoredInvestigationResult{}, s.getErr
	}
	return StoredInvestigationResult{SemanticState: s.state, SemanticStateRead: s.read}, nil
}

// TestReuseTakesTheStoredDeterminationBeforeServingAClarification drives
// tryReuse under a gate that returns the candidate, so the filter's own
// lines execute, over every determination the read side can reach.
func TestReuseTakesTheStoredDeterminationBeforeServingAClarification(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	named := storedStateFor(t, chaos5660NamedFrame(&project), "")
	windowOnly := func(candidate *InvestigationResult) {
		candidate.SubjectResolution = withheldPoolResolution()
		candidate.WindowClarification = &contractsv1.ContextFabricWindowClarification{Options: []contractsv1.ContextFabricWindowOption{{}}}
	}
	for _, testCase := range []struct {
		cell          string
		candidates    []SubjectCandidate
		mutate        func(*InvestigationResult)
		store         *storedAnswerabilityStore
		noStore       bool
		wantReuse     bool
		determination StoredAnswerabilityDetermination
		reading       string
	}{
		{"unanswerable reading: declined", storedCIRunCandidates(), nil, &storedAnswerabilityStore{state: named, read: SemanticStateReadAvailable}, false, false, StoredAnswerabilityUnanswerable, "available"},
		{"answerable reading: served", nil, func(candidate *InvestigationResult) {
			project, _ := reusableCandidate()
			candidate.SubjectResolution.Candidates = []SubjectCandidate{{
				ReceiptID: "subr_reuse_project", Subject: project, State: ResolutionAmbiguous,
				MatchReasons: []string{"Exact canonical subject label match."}, Confidence: 0.9,
			}}
		}, &storedAnswerabilityStore{state: named, read: SemanticStateReadAvailable}, false, true, StoredAnswerabilityAnswerable, "available"},
		{"legacy row without a reading: declined", storedCIRunCandidates(), nil, &storedAnswerabilityStore{read: SemanticStateReadAbsent}, false, false, StoredAnswerabilityUnavailable, "absent"},
		{"reading load fails: declined", storedCIRunCandidates(), nil, &storedAnswerabilityStore{getErr: errors.New("store down")}, false, false, StoredAnswerabilityUnavailable, "load_failed"},
		{"no result store: declined", storedCIRunCandidates(), nil, nil, true, false, StoredAnswerabilityUnavailable, "store_unconfigured"},
		{"window offers only, no reading: served", nil, windowOnly, &storedAnswerabilityStore{read: SemanticStateReadAbsent}, false, true, StoredAnswerabilityAnswerable, "not_read"},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			project, candidate := reusableCandidate()
			candidate.Status = InvestigationClarificationRequired
			candidate.StructureNeeds = nil
			candidate.SubjectResolution = SubjectResolution{Candidates: testCase.candidates, Committed: []SubjectRef{}}
			if testCase.mutate != nil {
				testCase.mutate(&candidate)
			}
			telemetry := &recordingTelemetry{}
			deps := EngineDependencies{
				Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}, bases: provenCommitBases(project)},
				Telemetry: telemetry,
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			}
			if !testCase.noStore {
				deps.Results = testCase.store
			}
			engine := mustReuseTestEngine(t, deps)
			if testCase.noStore {
				engine.results = nil
			}
			_, ok := engine.tryReuse(context.Background(), reusePrincipal(), validInvestigationRequest(),
				TimeContext{Axis: TemporalCurrent}, "", windowKeyRederivable, ResolvedGraphBinding{GraphKey: "some-key", Epoch: 0})
			if ok != testCase.wantReuse {
				t.Fatalf("tryReuse hit = %v, want %v", ok, testCase.wantReuse)
			}
			if len(telemetry.storedAnswerability) != 1 {
				t.Fatalf("stored answerability records = %d, want exactly 1 -- the determination was not taken, or taken twice", len(telemetry.storedAnswerability))
			}
			record := telemetry.storedAnswerability[0]
			if record.surface != StoredAnswerabilitySurfaceReuse || record.answerability.Determination != testCase.determination || record.answerability.Reading != testCase.reading {
				t.Fatalf("recorded surface/determination/reading = %q/%q/%q, want reuse/%q/%q", record.surface, record.answerability.Determination, record.answerability.Reading, testCase.determination, testCase.reading)
			}
			wantServed := InvestigationStatus("")
			if testCase.wantReuse {
				wantServed = InvestigationClarificationRequired
			}
			if record.storedStatus != InvestigationClarificationRequired || record.servedStatus != wantServed {
				t.Fatalf("recorded stored/served = %q/%q, want clarification_required/%q", record.storedStatus, record.servedStatus, wantServed)
			}
		})
	}
}

// TestTheStoredAnswerabilityLineThroughTheProductionSink reads the line back
// out of the production sink at Info, with values that differ pairwise.
func TestTheStoredAnswerabilityLineThroughTheProductionSink(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	named := storedStateFor(t, chaos5660NamedFrame(&project), "")
	served := storedClarification(append(storedCIRunCandidates(), roleCandidate("subr_project", SubjectProject, "project:1"), roleCandidate("subr_project_2", SubjectProject, "project:2"))...)
	answerability := DecideStoredAnswerability(served, named, SemanticStateReadAvailable)
	var buf bytes.Buffer
	sink := SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	sink.RecordStoredAnswerability(context.Background(), storage.Principal{OrgID: "org_stored"}, StoredAnswerabilitySurfaceReuse, answerability, InvestigationClarificationRequired, InvestigationClarificationRequired)
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("line is not JSON: %v (%s)", err, buf.String())
	}
	want := map[string]any{
		"msg": StoredAnswerabilityLogMessage, "level": "INFO", "org_id": "org_stored",
		"surface": "reuse", "determination": "answerable", "decided_by": "role", "semantic_state": "available", "repaired": false,
		"stored_status": "clarification_required", "served_status": "clarification_required",
		"evaluated_roles": "subject:project:open", "advanced_role": "subject:project", "advancing_channel": "subject_candidate",
		"offers_evaluated": float64(4), "offers_advancing": float64(2),
	}
	for key, value := range want {
		if line[key] != value {
			t.Errorf("%s = %#v, want %#v", key, line[key], value)
		}
	}
	args := StoredAnswerabilityLogArgs(StoredAnswerabilitySurfaceResultByID, StoredAnswerability{Determination: StoredAnswerabilityUnavailable, Reading: "absent"}, InvestigationClarificationRequired, "")
	rendered := ""
	for index := 0; index+1 < len(args); index += 2 {
		rendered += args[index].(string) + "="
		if text, ok := args[index+1].(string); ok {
			rendered += text
		}
		rendered += " "
	}
	for _, fragment := range []string{"surface=result_by_id", "determination=unavailable", "decided_by=none", "semantic_state=absent", "served_status=none", "evaluated_roles=none", "advanced_role=none"} {
		if !strings.Contains(rendered, fragment) {
			t.Errorf("unavailable line %q lacks %q", rendered, fragment)
		}
	}
}

// TestEveryStoredAnswerabilityTokenHasAProductionDriver enumerates both
// closed vocabularies from their declarations and requires a production
// driver for each member.
func TestEveryStoredAnswerabilityTokenHasAProductionDriver(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	named := storedStateFor(t, chaos5660NamedFrame(&project), "")
	seen := map[StoredAnswerabilityDetermination]bool{}
	for _, drive := range []StoredAnswerability{
		DecideStoredAnswerability(InvestigationResult{Status: InvestigationComplete}, nil, SemanticStateReadAbsent),
		DecideStoredAnswerability(storedClarification(roleCandidate("subr_project", SubjectProject, "project:1")), named, SemanticStateReadAvailable),
		DecideStoredAnswerability(storedClarification(storedCIRunCandidates()...), named, SemanticStateReadAvailable),
		DecideStoredAnswerability(storedClarification(storedCIRunCandidates()...), nil, SemanticStateReadAbsent),
	} {
		seen[drive.Determination] = true
	}
	for _, member := range StoredAnswerabilityDeterminations() {
		if !seen[member] {
			t.Errorf("determination %q has no production driver", member)
		}
	}
	if surfaces := StoredAnswerabilitySurfaces(); len(surfaces) != 2 {
		t.Fatalf("surface vocabulary = %v, want reuse and result_by_id; a new surface needs its own driver here", surfaces)
	}
}

// TestTheWireDisclosureIsPresentExactlyWhenTheDeterminationIsUnavailable
// executes the D49 mapping over every determination and every reading token
// an unavailable determination can carry.
func TestTheWireDisclosureIsPresentExactlyWhenTheDeterminationIsUnavailable(t *testing.T) {
	t.Parallel()
	for _, determination := range StoredAnswerabilityDeterminations() {
		if determination == StoredAnswerabilityUnavailable {
			continue
		}
		if got := (StoredAnswerability{Determination: determination, Reading: "absent"}).WireSemanticReading(); got != nil {
			t.Fatalf("determination %q disclosed %+v, want nothing", determination, got)
		}
	}
	for reading, want := range map[string]contractsv1.ContextFabricSemanticReadingReason{
		string(SemanticStateReadAbsent):             contractsv1.ContextFabricSemanticReadingStateAbsent,
		storedReadingUnreported:                     contractsv1.ContextFabricSemanticReadingStateAbsent,
		string(SemanticStateReadMalformed):          contractsv1.ContextFabricSemanticReadingStateUnreadable,
		string(SemanticStateReadOversized):          contractsv1.ContextFabricSemanticReadingStateUnreadable,
		string(SemanticStateReadUnsupportedVersion): contractsv1.ContextFabricSemanticReadingStateUnreadable,
	} {
		got := (StoredAnswerability{Determination: StoredAnswerabilityUnavailable, Reading: reading}).WireSemanticReading()
		if got == nil || got.Status != contractsv1.ContextFabricSemanticReadingUnavailable || got.Reason != want {
			t.Fatalf("reading %q disclosed %+v, want unavailable/%q", reading, got, want)
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("reading %q disclosed an invalid document: %v", reading, err)
		}
	}
}

// TestAnOrganizationScopeReadingIsRefusedOnBothSidesWhateverItsRoles holds the
// one reading where the organization-scope predicate and the role decision can
// disagree: an organization_scope frame whose pointer is absent derives no
// role, so no offer can make the role decision unsatisfiable, and still the
// question counts nothing. Fresh composition refuses it on its own basis
// (resolveTerminalStatus consults the predicate first); the read side must
// refuse the stored clarification the same way, or reuse and result-by-id
// would serve a clarification fresh composition never composes.
func TestAnOrganizationScopeReadingIsRefusedOnBothSidesWhateverItsRoles(t *testing.T) {
	t.Parallel()
	frame := &QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: SubjectExpression{Kind: SubjectExpressionOrganizationScope}}
	resolution := SubjectResolution{Candidates: storedCIRunCandidates(), Committed: []SubjectRef{}}

	fresh := decideDeclaredKind(frame, "", resolution, StructureOfferMaterial{})
	if fresh.Unsatisfiable || !fresh.OrganizationScopeUnsupported {
		t.Fatalf("fixture defect: fresh unsatisfiable/org = %v/%v, want false/true -- the reading must derive no role", fresh.Unsatisfiable, fresh.OrganizationScopeUnsupported)
	}
	request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: true}}
	if status, limitation := resolveTerminalStatus(request, &resolution, frame, true, fresh); status != InvestigationNoMatch || limitation != organizationScopeTerminalLimitation {
		t.Fatalf("fresh status/limitation = %q/%q, want the organization-scope refusal", status, limitation)
	}

	state := &PersistedSemanticState{FramePresent: true, Frame: frame}
	stored := storedClarification(storedCIRunCandidates()...)
	if got := DecideStoredAnswerability(stored, state, SemanticStateReadAvailable); got.Determination != StoredAnswerabilityUnanswerable {
		t.Fatalf("read-side determination = %q, want unanswerable, the refusal fresh composition takes", got.Determination)
	}
	served := storedClarification(storedCIRunCandidates()...)
	if got := RepairStoredClarification(&served, state, SemanticStateReadAvailable); !got.Repaired || served.RefusalBasis != organizationScopeTerminalBasis {
		t.Fatalf("repaired/basis = %v/%q, want true/organization_scope_unsupported", got.Repaired, served.RefusalBasis)
	}
}

// TestStoredAnswerabilityTakesFreshCompositionsPrecedence executes every step
// of the read side's precedence, in order, with the cells that separate each
// step from the one after it: a window-gate row carrying a refusing reading, an
// organization-scope reading with and without offers, a subjectless-terminal
// clarification that carries a window clarification, and the offer-less rows.
func TestStoredAnswerabilityTakesFreshCompositionsPrecedence(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	named := storedStateFor(t, chaos5660NamedFrame(&project), "")
	organization := storedStateFor(t, roleOrgFrameWithGoals(nil, GoalAssessState), "")
	counting := storedStateFor(t, roleOrgFrameWithGoals(&project, GoalCountOrAggregate), "")
	gate := func(result InvestigationResult) InvestigationResult {
		result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
			Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow, contractsv1.ContextFabricStructureNeedSubjectHandle},
			WindowOptions: []contractsv1.ContextFabricWindowOption{{}},
			HandleOptions: []contractsv1.ContextFabricHandleOption{chaos5660HandleOption(contractsv1.ContextFabricSubjectCIRun)},
		}
		return result
	}
	nudged := func(result InvestigationResult) InvestigationResult {
		result.WindowClarification = &contractsv1.ContextFabricWindowClarification{Options: []contractsv1.ContextFabricWindowOption{{}}}
		return result
	}
	offerLess := storedClarification()
	for _, testCase := range []struct {
		cell          string
		result        InvestigationResult
		state         *PersistedSemanticState
		read          SemanticStateReadStatus
		determination StoredAnswerabilityDetermination
		step          StoredAnswerabilityStep
		reading       string
	}{
		{"not a clarification", InvestigationResult{Status: InvestigationNoMatch}, organization, SemanticStateReadAvailable, StoredAnswerabilityNotApplicable, StoredAnswerabilityStepNone, "not_read"},
		{"window gate, named reading its offers refuse", gate(storedClarification()), named, SemanticStateReadAvailable, StoredAnswerabilityAnswerable, StoredAnswerabilityStepWindowGate, "not_read"},
		{"window gate, organization-scope reading", gate(storedClarification(storedCIRunCandidates()...)), organization, SemanticStateReadAvailable, StoredAnswerabilityAnswerable, StoredAnswerabilityStepWindowGate, "not_read"},
		{"window gate, no reading", gate(storedClarification(storedCIRunCandidates()...)), nil, SemanticStateReadAbsent, StoredAnswerabilityAnswerable, StoredAnswerabilityStepWindowGate, "not_read"},
		{"organization scope, wrong-kind offers", storedClarification(storedCIRunCandidates()...), organization, SemanticStateReadAvailable, StoredAnswerabilityUnanswerable, StoredAnswerabilityStepOrganizationScope, "available"},
		{"organization scope, no offer at all", offerLess, organization, SemanticStateReadAvailable, StoredAnswerabilityUnanswerable, StoredAnswerabilityStepOrganizationScope, "available"},
		{"organization scope, window clarification only", nudged(storedClarification()), organization, SemanticStateReadAvailable, StoredAnswerabilityUnanswerable, StoredAnswerabilityStepOrganizationScope, "available"},
		{"organization count, wrong-kind offers: the role decides", storedClarification(storedCIRunCandidates()...), counting, SemanticStateReadAvailable, StoredAnswerabilityUnanswerable, StoredAnswerabilityStepRole, "available"},
		{"terminal clarification carrying a window clarification: the role decides", nudged(storedClarification(storedCIRunCandidates()...)), named, SemanticStateReadAvailable, StoredAnswerabilityUnanswerable, StoredAnswerabilityStepRole, "available"},
		{"role, declared-kind offer", storedClarification(roleCandidate("subr_project", SubjectProject, "project:1")), named, SemanticStateReadAvailable, StoredAnswerabilityAnswerable, StoredAnswerabilityStepRole, "available"},
		{"role, no reading", storedClarification(storedCIRunCandidates()...), nil, SemanticStateReadAbsent, StoredAnswerabilityUnavailable, StoredAnswerabilityStepRole, "absent"},
		{"no subject offer, window clarification only, no reading", nudged(storedClarification()), nil, SemanticStateReadAbsent, StoredAnswerabilityAnswerable, StoredAnswerabilityStepNoSubjectOffer, "not_read"},
		{"no subject offer, named reading", nudged(storedClarification()), named, SemanticStateReadAvailable, StoredAnswerabilityAnswerable, StoredAnswerabilityStepNoSubjectOffer, "not_read"},
		{"offer-less, no reading", offerLess, nil, SemanticStateReadAbsent, StoredAnswerabilityUnanswerable, StoredAnswerabilityStepOfferLess, "not_read"},
		{"offer-less, named reading", offerLess, named, SemanticStateReadAvailable, StoredAnswerabilityUnanswerable, StoredAnswerabilityStepOfferLess, "not_read"},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			got := DecideStoredAnswerability(testCase.result, testCase.state, testCase.read)
			if got.Determination != testCase.determination || got.Step != testCase.step || got.Reading != testCase.reading {
				t.Fatalf("determination/step/reading = %q/%q/%q, want %q/%q/%q", got.Determination, got.Step, got.Reading, testCase.determination, testCase.step, testCase.reading)
			}
		})
	}
}

// TestTheRepairTakesTheBasisOfTheStepThatRefused holds what the served copy
// becomes at each refusing step, and that a window-gate row is untouched even
// when its reading would refuse a terminal clarification.
func TestTheRepairTakesTheBasisOfTheStepThatRefused(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	named := storedStateFor(t, chaos5660NamedFrame(&project), "")
	organization := storedStateFor(t, roleOrgFrameWithGoals(nil, GoalAssessState), "")
	t.Run("an offer-less row with an organization-scope reading takes the organization basis", func(t *testing.T) {
		t.Parallel()
		served := storedClarification()
		got := RepairStoredClarification(&served, organization, SemanticStateReadAvailable)
		if got.Step != StoredAnswerabilityStepOrganizationScope || !got.Repaired {
			t.Fatalf("step/repaired = %q/%v, want organization_scope/true", got.Step, got.Repaired)
		}
		if served.Status != InvestigationNoMatch || served.RefusalBasis != organizationScopeTerminalBasis || !roleContains(served.Limitations, organizationScopeTerminalLimitation) {
			t.Fatalf("served status/basis/limitations = %q/%q/%#v, want no_match/organization_scope_unsupported with its sentence", served.Status, served.RefusalBasis, served.Limitations)
		}
		if roleContains(served.Limitations, noMatchLimitationOfferPoolEmptied) {
			t.Fatalf("served limitations = %#v carry the offer-less sentence on an organization-scope refusal", served.Limitations)
		}
	})
	t.Run("an offer-less row with no reading is repaired with no basis", func(t *testing.T) {
		t.Parallel()
		served := storedClarification()
		got := RepairStoredClarification(&served, nil, SemanticStateReadAbsent)
		if got.Step != StoredAnswerabilityStepOfferLess || got.Determination != StoredAnswerabilityUnanswerable || !got.Repaired {
			t.Fatalf("step/determination/repaired = %q/%q/%v, want offer_less/unanswerable/true", got.Step, got.Determination, got.Repaired)
		}
		if served.Status != InvestigationNoMatch || served.RefusalBasis != "" || !roleContains(served.Limitations, noMatchLimitationOfferPoolEmptied) {
			t.Fatalf("served status/basis/limitations = %q/%q/%#v, want no_match, no basis, the offer-pool sentence", served.Status, served.RefusalBasis, served.Limitations)
		}
	})
	for _, reading := range []struct {
		name  string
		state *PersistedSemanticState
	}{{"named", named}, {"organization scope", organization}} {
		t.Run("a window-gate row with a "+reading.name+" reading is untouched", func(t *testing.T) {
			t.Parallel()
			served := storedClarification(storedCIRunCandidates()...)
			served.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
				Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
				WindowOptions: []contractsv1.ContextFabricWindowOption{{}},
			}
			before, _ := json.Marshal(served)
			got := RepairStoredClarification(&served, reading.state, SemanticStateReadAvailable)
			after, _ := json.Marshal(served)
			if got.Step != StoredAnswerabilityStepWindowGate || got.Repaired || !bytes.Equal(before, after) {
				t.Fatalf("step/repaired = %q/%v, document changed = %v; want window_gate, untouched", got.Step, got.Repaired, !bytes.Equal(before, after))
			}
		})
	}
}

// TestEveryStoredAnswerabilityStepHasAProductionDriver enumerates the step
// vocabulary from its declaration and requires the production decision to
// reach every member.
func TestEveryStoredAnswerabilityStepHasAProductionDriver(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	named := storedStateFor(t, chaos5660NamedFrame(&project), "")
	organization := storedStateFor(t, roleOrgFrameWithGoals(nil, GoalAssessState), "")
	gateRow := storedClarification()
	gateRow.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow}, WindowOptions: []contractsv1.ContextFabricWindowOption{{}}}
	windowOnly := storedClarification()
	windowOnly.WindowClarification = &contractsv1.ContextFabricWindowClarification{Options: []contractsv1.ContextFabricWindowOption{{}}}
	seen := map[StoredAnswerabilityStep]bool{}
	for _, drive := range []StoredAnswerability{
		DecideStoredAnswerability(InvestigationResult{Status: InvestigationComplete}, nil, SemanticStateReadAbsent),
		DecideStoredAnswerability(gateRow, named, SemanticStateReadAvailable),
		DecideStoredAnswerability(storedClarification(storedCIRunCandidates()...), organization, SemanticStateReadAvailable),
		DecideStoredAnswerability(storedClarification(storedCIRunCandidates()...), named, SemanticStateReadAvailable),
		DecideStoredAnswerability(windowOnly, nil, SemanticStateReadAbsent),
		DecideStoredAnswerability(storedClarification(), nil, SemanticStateReadAbsent),
	} {
		seen[drive.Step] = true
	}
	for _, member := range StoredAnswerabilitySteps() {
		if !seen[member] {
			t.Errorf("step %q has no production driver", member)
		}
	}
}

// TestReuseTakesFreshCompositionsPrecedence drives tryReuse over the steps the
// role-only filter did not separate: the window gate's clarification is served
// whatever its reading, and an organization-scope or offer-less row is
// declined with its determination recorded exactly once.
func TestReuseTakesFreshCompositionsPrecedence(t *testing.T) {
	t.Parallel()
	organization := storedStateFor(t, roleOrgFrameWithGoals(nil, GoalAssessState), "")
	// The gate row offers only window options, so the authorization recheck
	// after the filter has no subject to re-prove: a miss would be the
	// filter's.
	gateNeeds := func(candidate *InvestigationResult) {
		candidate.SubjectResolution = withheldPoolResolution()
		candidate.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
			Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
			WindowOptions: []contractsv1.ContextFabricWindowOption{{}},
		}
	}
	offerLess := func(candidate *InvestigationResult) {
		candidate.SubjectResolution = withheldPoolResolution()
		candidate.WindowClarification = nil
	}
	for _, testCase := range []struct {
		cell       string
		candidates []SubjectCandidate
		mutate     func(*InvestigationResult)
		store      *storedAnswerabilityStore
		wantReuse  bool
		step       StoredAnswerabilityStep
	}{
		{"window gate, organization-scope reading: served", nil, gateNeeds, &storedAnswerabilityStore{state: organization, read: SemanticStateReadAvailable}, true, StoredAnswerabilityStepWindowGate},
		{"organization scope, wrong-kind offers: declined", storedCIRunCandidates(), nil, &storedAnswerabilityStore{state: organization, read: SemanticStateReadAvailable}, false, StoredAnswerabilityStepOrganizationScope},
		{"organization scope, no offer at all: declined", nil, offerLess, &storedAnswerabilityStore{state: organization, read: SemanticStateReadAvailable}, false, StoredAnswerabilityStepOrganizationScope},
		{"offer-less, no reading: declined", nil, offerLess, &storedAnswerabilityStore{read: SemanticStateReadAbsent}, false, StoredAnswerabilityStepOfferLess},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			project, candidate := reusableCandidate()
			candidate.Status = InvestigationClarificationRequired
			candidate.StructureNeeds = nil
			candidate.SubjectResolution = SubjectResolution{Candidates: testCase.candidates, Committed: []SubjectRef{}}
			if testCase.mutate != nil {
				testCase.mutate(&candidate)
			}
			telemetry := &recordingTelemetry{}
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}, bases: provenCommitBases(project)},
				Telemetry: telemetry,
				Results:   testCase.store,
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			})
			_, ok := engine.tryReuse(context.Background(), reusePrincipal(), validInvestigationRequest(),
				TimeContext{Axis: TemporalCurrent}, "", windowKeyRederivable, ResolvedGraphBinding{GraphKey: "some-key", Epoch: 0})
			if ok != testCase.wantReuse {
				t.Fatalf("tryReuse hit = %v, want %v", ok, testCase.wantReuse)
			}
			if len(telemetry.storedAnswerability) != 1 {
				t.Fatalf("stored answerability records = %d, want exactly 1", len(telemetry.storedAnswerability))
			}
			if record := telemetry.storedAnswerability[0]; record.answerability.Step != testCase.step {
				t.Fatalf("recorded step = %q, want %q", record.answerability.Step, testCase.step)
			}
		})
	}
}

// TestOnlyTheWindowGateComposesStructureWindowOptions keeps the read side's
// window-gate discriminator honest by driving every other producer of a
// clarification: the subjectless terminal, with and without the window
// nudge, must never populate StructureNeeds.WindowOptions. The gate itself is
// the positive control. A producer that starts writing them fails here before
// the read side can serve its clarification as the gate's.
func TestOnlyTheWindowGateComposesStructureWindowOptions(t *testing.T) {
	t.Parallel()
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	gateInterpreter := &countingInterpreter{
		interpretation: bootstrapInterpretation(),
		family:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone, Frame: chaos5660NamedFrame(&project), Gate: FrameGate{Outcome: FrameGatePassed}},
	}
	gateGraph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext(), material: chaos5660MeasuredOffers()}
	gated, err := buildWindowGateEngine(t, gateInterpreter, gateGraph, newMapResultStore()).Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("gate Investigate: %v", err)
	}
	if gated.Status != InvestigationClarificationRequired || !windowGateClarification(gated) {
		t.Fatalf("positive control: the gate's clarification status=%q carries no structure window options", gated.Status)
	}

	for _, mode := range []contractsv1.ContextFabricWindowConfirmationMode{"", contractsv1.ContextFabricWindowConfirmationNudge} {
		t.Run("subjectless terminal, window confirmation mode "+string(mode), func(t *testing.T) {
			t.Parallel()
			factReads := 0
			engine := roleEngine(t, roleInterpreter{frame: roleScopedFrame(SubjectProject), anchorKind: SubjectTeam}, &roleGraph{first: SubjectResolution{Candidates: roleTwoTeamAnchors(), Committed: []SubjectRef{}}}, newMapResultStore(), &recordingTelemetry{}, &factReads)
			request := validInvestigationRequestWithConfirmedWindow()
			request.Options.WindowConfirmationMode = mode
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_role"}, request)
			if err != nil {
				t.Fatalf("Investigate: %v", err)
			}
			if result.Status != InvestigationClarificationRequired {
				t.Fatalf("fixture defect: the subjectless terminal status = %q, want clarification_required", result.Status)
			}
			if windowGateClarification(result) {
				t.Fatalf("the subjectless terminal composed structure window options %#v -- the read side would serve its clarification as the window gate's", result.StructureNeeds.WindowOptions)
			}
			// Every inferred window is gated or replaced by its carry before
			// the terminal runs (engine.go), and the terminal composes a window
			// clarification only for an inferred window, so in either mode it
			// carries none. The day one reaches it, the read side must still
			// take it at the role step, which the check below holds.
			if result.WindowClarification != nil {
				t.Logf("the subjectless terminal now carries a window clarification (%d options)", len(result.WindowClarification.Options))
			}
			scoped := storedStateFor(t, roleScopedFrame(SubjectProject), SubjectTeam)
			if got := DecideStoredAnswerability(result, scoped, SemanticStateReadAvailable); got.Step != StoredAnswerabilityStepRole {
				t.Fatalf("the read side took the terminal's clarification at step %q, want role", got.Step)
			}
		})
	}
}
