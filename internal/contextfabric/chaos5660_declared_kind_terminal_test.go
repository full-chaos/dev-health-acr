package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// chaos5660NamedFrame builds the frame shape both measured rows produced: a
// named_subject expression whose ExpectedKind the model stated.
func chaos5660NamedFrame(expected *SubjectKind) *QuestionFrame {
	return &QuestionFrame{SubjectExpression: SubjectExpression{
		Kind:  SubjectExpressionNamed,
		Named: &NamedSubjectExpression{Terms: []string{"acr"}, ExpectedKind: expected},
	}}
}

// chaos5660KindOption/chaos5660HandleOption/chaos5660CandidateOption/chaos5660AnchorOption/chaos5660SubjectCandidate build
// one offer of a given kind on each of the five channels a caller can redeem.
func chaos5660KindOption(kind contractsv1.ContextFabricSubjectKind) contractsv1.ContextFabricKindOption {
	return contractsv1.ContextFabricKindOption{ReceiptID: "kndr_1", Kind: kind}
}

func chaos5660HandleOption(kind contractsv1.ContextFabricSubjectKind) contractsv1.ContextFabricHandleOption {
	return contractsv1.ContextFabricHandleOption{ReceiptID: "handr_1", Kind: kind}
}

func chaos5660CandidateOption(kind contractsv1.ContextFabricSubjectKind) contractsv1.ContextFabricCandidateOption {
	return contractsv1.ContextFabricCandidateOption{ReceiptID: "candr_1", Kind: kind}
}

func chaos5660AnchorOption(kind contractsv1.ContextFabricSubjectKind) contractsv1.ContextFabricAnchorOption {
	return contractsv1.ContextFabricAnchorOption{ReceiptID: "ancr_1", Kind: kind}
}

func chaos5660SubjectCandidate(kind contractsv1.ContextFabricSubjectKind) contractsv1.ContextFabricSubjectCandidate {
	return contractsv1.ContextFabricSubjectCandidate{
		ReceiptID: "subr_1",
		Subject:   contractsv1.ContextFabricSubjectRef{Kind: kind, CanonicalID: "canonical"},
	}
}

// TestTheMeasuredOddTurnShapeTerminates pins the shape both rows served on
// turns 1/3/5 of every replicate, read from the yardstick of record
// (~/.cache/acr-kiac-askdev/proofs/2026-09-12-main-b6579178): the frame
// declared `project`, NO kind options were raised at all (the declared kind
// was unofferable, so chaos3900_structure_offers.go withheld the need), the
// candidate list was empty, and the handle/candidate option channels carried
// ci_pipeline_run and pull_request only.
//
// Before CHAOS-5660 this turn was clarification_required and the caller had
// nothing among those options to answer a question about a project with.
func TestTheMeasuredOddTurnShapeTerminates(t *testing.T) {
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	material := StructureOfferMaterial{
		HandleOptions: []contractsv1.ContextFabricHandleOption{
			chaos5660HandleOption(contractsv1.ContextFabricSubjectCIRun),
			chaos5660HandleOption(contractsv1.ContextFabricSubjectPullRequest),
		},
		CandidateOptions: []contractsv1.ContextFabricCandidateOption{
			chaos5660CandidateOption(contractsv1.ContextFabricSubjectCIRun),
		},
	}
	resolution := SubjectResolution{ClarificationPrompt: OfferPoolEmptiedClarificationPrompt}
	decision := decideDeclaredKind(chaos5660NamedFrame(&project), resolution, material)
	if !decision.Unsatisfiable {
		t.Fatalf("Unsatisfiable = false for the measured odd-turn shape (declared %v, offered %v) -- this is the exact document that looped five turns",
			decision.DeclaredKinds, decision.OfferedKinds)
	}
	if got := decision.ObservableOfferedKinds(); got != "ci_pipeline_run,pull_request" {
		t.Fatalf("offered_kinds = %q, want the first-seen order across channels", got)
	}
	if clarificationOffersRedeemable(resolution, material, nil, decision) {
		t.Fatal("the predicate still calls this turn answerable -- CHAOS-5637's conjunct passes here (options exist), so the whole fix is the declared-kind conjunct")
	}
	request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: true}}
	status, limitation := resolveTerminalStatus(request, &resolution, false, decision)
	if status != InvestigationNoMatch {
		t.Fatalf("status = %q, want no_match on turn ONE", status)
	}
	if limitation != declaredKindTerminalLimitation {
		t.Fatalf("limitation = %q, want the declared-kind terminal sentence", limitation)
	}
	if reason := subjectlessTerminalReason(FrameGate{Outcome: FrameGatePassed}, resolution, 0, decision); reason != declaredKindTerminalReason {
		t.Fatalf("reason = %q, want %q", reason, declaredKindTerminalReason)
	}
}

// TestTheMeasuredEvenTurnShapeTerminates pins the OTHER measured shape --
// turns 2/4, where the kind need WAS raised with four options and the
// candidate list carried seven ci_pipeline_runs. The kind options are
// present and none of them is the declared kind, which is the case
// CHAOS-5637's "any option" predicate admits and this one does not.
func TestTheMeasuredEvenTurnShapeTerminates(t *testing.T) {
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	material := StructureOfferMaterial{
		KindOptions: []contractsv1.ContextFabricKindOption{
			chaos5660KindOption(contractsv1.ContextFabricSubjectCIRun),
			chaos5660KindOption(contractsv1.ContextFabricSubjectPullRequest),
			chaos5660KindOption(contractsv1.ContextFabricSubjectPullRequestReview),
			chaos5660KindOption(contractsv1.ContextFabricSubjectRepository),
		},
		HandleOptions: []contractsv1.ContextFabricHandleOption{
			chaos5660HandleOption(contractsv1.ContextFabricSubjectCIRun),
		},
		CandidateOptions: []contractsv1.ContextFabricCandidateOption{
			chaos5660CandidateOption(contractsv1.ContextFabricSubjectCIRun),
		},
	}
	var candidates []contractsv1.ContextFabricSubjectCandidate
	for range 7 {
		candidates = append(candidates, chaos5660SubjectCandidate(contractsv1.ContextFabricSubjectCIRun))
	}
	resolution := SubjectResolution{Candidates: candidates}
	decision := decideDeclaredKind(chaos5660NamedFrame(&project), resolution, material)
	if !decision.Unsatisfiable {
		t.Fatalf("Unsatisfiable = false for the measured even-turn shape (offered %v)", decision.OfferedKinds)
	}
	request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: true}}
	status, limitation := resolveTerminalStatus(request, &resolution, true, decision)
	if status != InvestigationNoMatch || limitation != declaredKindTerminalLimitation {
		t.Fatalf("status/limitation = %q/%q, want no_match with the declared-kind sentence -- a non-empty pool of the WRONG kind is not an ambiguity the caller can resolve", status, limitation)
	}
	if reason := subjectlessTerminalReason(FrameGate{Outcome: FrameGatePassed}, resolution, 0, decision); reason == "ambiguous" {
		t.Fatal("reason = ambiguous -- the new terminal is indistinguishable from the loop it replaces in the one line an operator counts it on")
	}
}

// TestAControlCandidateOfTheDeclaredKindStillClarifies is the discriminating
// control: the SAME shape with ONE candidate of the declared kind added.
// Nothing else changes. The turn stays a clarification, because the caller
// now has something among the options that answers their own question.
//
// It is the control this change is worthless without: a predicate that
// terminated here would end every genuinely ambiguous named-subject
// conversation, which is strictly worse than the loop.
func TestAControlCandidateOfTheDeclaredKindStillClarifies(t *testing.T) {
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	material := StructureOfferMaterial{
		KindOptions: []contractsv1.ContextFabricKindOption{
			chaos5660KindOption(contractsv1.ContextFabricSubjectCIRun),
			chaos5660KindOption(contractsv1.ContextFabricSubjectPullRequest),
		},
		CandidateOptions: []contractsv1.ContextFabricCandidateOption{
			chaos5660CandidateOption(contractsv1.ContextFabricSubjectCIRun),
		},
	}
	resolution := SubjectResolution{Candidates: []contractsv1.ContextFabricSubjectCandidate{
		chaos5660SubjectCandidate(contractsv1.ContextFabricSubjectCIRun),
		chaos5660SubjectCandidate(contractsv1.ContextFabricSubjectProject),
	}}
	decision := decideDeclaredKind(chaos5660NamedFrame(&project), resolution, material)
	if decision.Unsatisfiable {
		t.Fatalf("Unsatisfiable = true with a project candidate in the pool (offered %v) -- this conversation can converge and must not be terminated", decision.OfferedKinds)
	}
	if !clarificationOffersRedeemable(resolution, material, nil, decision) {
		t.Fatal("the predicate refuses a turn that carries a candidate of the declared kind")
	}
	request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: true}}
	status, _ := resolveTerminalStatus(request, &resolution, true, decision)
	if status != InvestigationClarificationRequired {
		t.Fatalf("status = %q, want clarification_required", status)
	}
	if reason := subjectlessTerminalReason(FrameGate{Outcome: FrameGatePassed}, resolution, 0, decision); reason != "ambiguous" {
		t.Fatalf("reason = %q, want the unchanged ambiguous reading", reason)
	}
}

// TestEveryChannelCanSatisfyTheDeclaredKind sweeps the control across ALL
// FIVE redeemable channels, one at a time. A caller may answer through any
// of them, so an option of the declared kind on any one makes the turn
// satisfiable -- a predicate reading fewer channels would terminate a turn
// the caller could in fact have answered.
func TestEveryChannelCanSatisfyTheDeclaredKind(t *testing.T) {
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	wrong := contractsv1.ContextFabricSubjectCIRun
	right := contractsv1.ContextFabricSubjectProject
	for _, testCase := range []struct {
		channel    string
		material   StructureOfferMaterial
		resolution SubjectResolution
	}{
		{"kind_options", StructureOfferMaterial{KindOptions: []contractsv1.ContextFabricKindOption{chaos5660KindOption(right)}}, SubjectResolution{}},
		{"anchor_options", StructureOfferMaterial{AnchorOptions: []contractsv1.ContextFabricAnchorOption{chaos5660AnchorOption(right)}}, SubjectResolution{}},
		{"handle_options", StructureOfferMaterial{HandleOptions: []contractsv1.ContextFabricHandleOption{chaos5660HandleOption(right)}}, SubjectResolution{}},
		{"candidate_options", StructureOfferMaterial{CandidateOptions: []contractsv1.ContextFabricCandidateOption{chaos5660CandidateOption(right)}}, SubjectResolution{}},
		{"subject_candidates", StructureOfferMaterial{KindOptions: []contractsv1.ContextFabricKindOption{chaos5660KindOption(wrong)}}, SubjectResolution{Candidates: []contractsv1.ContextFabricSubjectCandidate{chaos5660SubjectCandidate(right)}}},
	} {
		t.Run(testCase.channel, func(t *testing.T) {
			decision := decideDeclaredKind(chaos5660NamedFrame(&project), testCase.resolution, testCase.material)
			if decision.Unsatisfiable {
				t.Fatalf("%s carrying the declared kind did not satisfy it (offered %v)", testCase.channel, decision.OfferedKinds)
			}
		})
	}
}

// TestTheDeclaredKindGuardOverItsWholeInputDomain executes every cell of
// both inputs' domain in one pass: the frame (each union variant, plus
// absent/nil/empty/out-of-vocabulary declared kinds) crossed with the offer
// side (absent, empty, empty-kind, wrong, duplicate, canonical).
func TestTheDeclaredKindGuardOverItsWholeInputDomain(t *testing.T) {
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	empty := SubjectKind("")
	outOfVocabulary := SubjectKind("a_kind_no_vocabulary_names")
	team := SubjectKind(contractsv1.ContextFabricSubjectTeam)

	t.Run("frame domain", func(t *testing.T) {
		for _, testCase := range []struct {
			cell  string
			frame *QuestionFrame
			want  []SubjectKind
		}{
			{"frame absent (nil pointer)", nil, nil},
			{"named, Named nil", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionNamed}}, nil},
			{"named, ExpectedKind absent", chaos5660NamedFrame(nil), nil},
			{"named, ExpectedKind empty string", chaos5660NamedFrame(&empty), nil},
			{"named, ExpectedKind out of vocabulary", chaos5660NamedFrame(&outOfVocabulary), nil},
			{"named, ExpectedKind canonical", chaos5660NamedFrame(&project), []SubjectKind{project}},
			{"discovered", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: team}}}, []SubjectKind{team}},
			{"discovered, Discovered nil", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionDiscoveredKind}}, nil},
			{"scoped", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionChildrenOfScope, Scoped: &ScopedSetExpression{MemberKind: project}}}, []SubjectKind{project}},
			{"grouped, BOTH axes", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionGroupedMembers, Grouped: &GroupedSetExpression{MemberKind: project, GroupKind: team}}}, []SubjectKind{team, project}},
			{"org, MemberKind nil", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}}}, nil},
			{"org, MemberKind set", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{MemberKind: &team}}}, []SubjectKind{team}},
			{"variant out of vocabulary", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionKind("not_a_variant")}}, nil},
		} {
			t.Run(testCase.cell, func(t *testing.T) {
				got := testCase.frame.DeclaredKinds()
				if len(got) != len(testCase.want) {
					t.Fatalf("DeclaredKinds() = %v, want %v", got, testCase.want)
				}
				for index := range got {
					if got[index] != testCase.want[index] {
						t.Fatalf("DeclaredKinds() = %v, want %v (vocabulary order is the guarantee)", got, testCase.want)
					}
				}
				// A frame that declares nothing can never make a turn
				// unsatisfiable, whatever is offered: absent is a weaker
				// claim than declared.
				if len(testCase.want) == 0 {
					decision := decideDeclaredKind(testCase.frame, SubjectResolution{}, StructureOfferMaterial{
						KindOptions: []contractsv1.ContextFabricKindOption{chaos5660KindOption(contractsv1.ContextFabricSubjectCIRun)},
					})
					if decision.Unsatisfiable {
						t.Fatal("a frame declaring no kind made a turn unsatisfiable -- a question that constrained nothing cannot be unanswerable for constraining the wrong thing")
					}
				}
			})
		}
	})

	t.Run("offer domain", func(t *testing.T) {
		for _, testCase := range []struct {
			cell     string
			material StructureOfferMaterial
			want     bool
		}{
			{"every channel absent (nil slices)", StructureOfferMaterial{}, false},
			{"every channel empty (non-nil, len 0)", StructureOfferMaterial{
				KindOptions:      []contractsv1.ContextFabricKindOption{},
				AnchorOptions:    []contractsv1.ContextFabricAnchorOption{},
				HandleOptions:    []contractsv1.ContextFabricHandleOption{},
				CandidateOptions: []contractsv1.ContextFabricCandidateOption{},
			}, false},
			{"one option, Kind empty string", StructureOfferMaterial{
				KindOptions: []contractsv1.ContextFabricKindOption{chaos5660KindOption("")},
			}, false},
			{"one option, wrong kind", StructureOfferMaterial{
				KindOptions: []contractsv1.ContextFabricKindOption{chaos5660KindOption(contractsv1.ContextFabricSubjectCIRun)},
			}, true},
			{"one option, out-of-vocabulary kind", StructureOfferMaterial{
				KindOptions: []contractsv1.ContextFabricKindOption{chaos5660KindOption(contractsv1.ContextFabricSubjectKind("a_kind_no_vocabulary_names"))},
			}, true},
			{"duplicates of the wrong kind", StructureOfferMaterial{
				KindOptions: []contractsv1.ContextFabricKindOption{
					chaos5660KindOption(contractsv1.ContextFabricSubjectCIRun),
					chaos5660KindOption(contractsv1.ContextFabricSubjectCIRun),
				},
			}, true},
			{"duplicates including the declared kind", StructureOfferMaterial{
				KindOptions: []contractsv1.ContextFabricKindOption{
					chaos5660KindOption(contractsv1.ContextFabricSubjectCIRun),
					chaos5660KindOption(contractsv1.ContextFabricSubjectProject),
					chaos5660KindOption(contractsv1.ContextFabricSubjectProject),
				},
			}, false},
		} {
			t.Run(testCase.cell, func(t *testing.T) {
				decision := decideDeclaredKind(chaos5660NamedFrame(&project), SubjectResolution{}, testCase.material)
				if decision.Unsatisfiable != testCase.want {
					t.Fatalf("Unsatisfiable = %v, want %v (declared %v, offered %v)",
						decision.Unsatisfiable, testCase.want, decision.DeclaredKinds, decision.OfferedKinds)
				}
			})
		}
	})

	t.Run("an empty offer set is CHAOS-5637's condition, not this one", func(t *testing.T) {
		decision := decideDeclaredKind(chaos5660NamedFrame(&project), SubjectResolution{}, StructureOfferMaterial{})
		if decision.Unsatisfiable {
			t.Fatal("a turn with NO offers reported unsatisfiable here -- that state is CHAOS-5637's, and giving it two names in two log lines is the collapse both vocabularies exist to avoid")
		}
		if clarificationOffersRedeemable(SubjectResolution{}, StructureOfferMaterial{}, nil, decision) {
			t.Fatal("CHAOS-5637's own conjunct stopped holding")
		}
	})

	t.Run("a duplicate kind is emitted once, in first-seen order", func(t *testing.T) {
		decision := decideDeclaredKind(chaos5660NamedFrame(&project), SubjectResolution{
			Candidates: []contractsv1.ContextFabricSubjectCandidate{chaos5660SubjectCandidate(contractsv1.ContextFabricSubjectCIRun)},
		}, StructureOfferMaterial{
			KindOptions:   []contractsv1.ContextFabricKindOption{chaos5660KindOption(contractsv1.ContextFabricSubjectPullRequest), chaos5660KindOption(contractsv1.ContextFabricSubjectCIRun)},
			HandleOptions: []contractsv1.ContextFabricHandleOption{chaos5660HandleOption(contractsv1.ContextFabricSubjectPullRequest)},
		})
		if got := decision.ObservableOfferedKinds(); got != "pull_request,ci_pipeline_run" {
			t.Fatalf("offered_kinds = %q, want each kind once in first-seen order", got)
		}
	})
}

// TestTheObservableTokensAreNeverEmpty holds the missing-versus-measured-zero
// rule on the two new log values: an empty rendering is indistinguishable
// from a key nobody wrote.
func TestTheObservableTokensAreNeverEmpty(t *testing.T) {
	decision := decideDeclaredKind(nil, SubjectResolution{}, StructureOfferMaterial{})
	if decision.ObservableDeclaredKinds() != "none" || decision.ObservableOfferedKinds() != "none" {
		t.Fatalf("declared/offered = %q/%q, want the explicit none token on both",
			decision.ObservableDeclaredKinds(), decision.ObservableOfferedKinds())
	}
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	set := decideDeclaredKind(chaos5660NamedFrame(&project), SubjectResolution{}, StructureOfferMaterial{
		KindOptions: []contractsv1.ContextFabricKindOption{chaos5660KindOption(contractsv1.ContextFabricSubjectCIRun)},
	})
	if set.ObservableDeclaredKinds() != "project" || set.ObservableOfferedKinds() != "ci_pipeline_run" {
		t.Fatalf("declared/offered = %q/%q, want the measured values", set.ObservableDeclaredKinds(), set.ObservableOfferedKinds())
	}
}

// TestAFrameGateRefusalStillWinsTheReason pins the ordering the new member
// was inserted into: a refused frame never reached retrieval, so it cannot
// be reported as a finding about what retrieval offered.
func TestAFrameGateRefusalStillWinsTheReason(t *testing.T) {
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	decision := decideDeclaredKind(chaos5660NamedFrame(&project), SubjectResolution{}, StructureOfferMaterial{
		KindOptions: []contractsv1.ContextFabricKindOption{chaos5660KindOption(contractsv1.ContextFabricSubjectCIRun)},
	})
	if !decision.Unsatisfiable {
		t.Fatal("fixture did not reach the unsatisfiable state it exists to order against")
	}
	gate := FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable}
	if reason := subjectlessTerminalReason(gate, SubjectResolution{}, 0, decision); reason != "frame_gate_refused" {
		t.Fatalf("reason = %q, want frame_gate_refused to keep winning", reason)
	}
}

// TestACallerThatDeclinedClarificationKeepsItsOwnSentences holds the arm's
// deliberate scope. Such a caller already terminates, and the sentences it
// receives describe what happened to the pool for someone who was never
// going to be asked anything.
func TestACallerThatDeclinedClarificationKeepsItsOwnSentences(t *testing.T) {
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	resolution := SubjectResolution{Candidates: []contractsv1.ContextFabricSubjectCandidate{
		chaos5660SubjectCandidate(contractsv1.ContextFabricSubjectCIRun),
		chaos5660SubjectCandidate(contractsv1.ContextFabricSubjectCIRun),
	}}
	decision := decideDeclaredKind(chaos5660NamedFrame(&project), resolution, StructureOfferMaterial{})
	if !decision.Unsatisfiable {
		t.Fatal("fixture did not reach the unsatisfiable state")
	}
	request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: false}}
	status, limitation := resolveTerminalStatus(request, &resolution, false, decision)
	if status != InvestigationNoMatch {
		t.Fatalf("status = %q, want no_match", status)
	}
	if limitation != ambiguousNoClarificationLimitation {
		t.Fatalf("limitation = %q, want the unchanged no-clarification sentence", limitation)
	}
}

// TestTheTerminalSentenceIsAServiceAuthoredDisclosure holds the property the
// sentence is reused FOR: it must be recognised as service-authored, or a
// disclosure nothing may displace becomes displaceable.
func TestTheTerminalSentenceIsAServiceAuthoredDisclosure(t *testing.T) {
	if strings.TrimSpace(declaredKindTerminalLimitation) == "" {
		t.Fatal("the terminal carries no sentence at all")
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(declaredKindTerminalLimitation) {
		t.Fatalf("the declared-kind terminal sentence is not recognised as service-authored: %q", declaredKindTerminalLimitation)
	}
}

// TestTheWireBasisGapIsDeclaredNotForgotten pins the tracked gap as a fact
// rather than a comment: the basis constant is empty today, so the class is
// countable in the log line and NOT on the wire. The moment the vocabulary
// member lands, setting the constant is the whole change and this test is
// what says so.
func TestTheWireBasisGapIsDeclaredNotForgotten(t *testing.T) {
	// Empty is the state this change ships in, and it is legal ONLY as the
	// empty value: the wire vocabulary's own rule is that an empty basis
	// means the turn was not refused, so this terminal discloses no refusal
	// and the class stays uncountable on the wire. That is the tracked gap.
	if declaredKindTerminalBasis == "" {
		if contractsv1.ValidContextFabricRefusalBasis(declaredKindTerminalBasis) {
			t.Fatal("the empty basis became a vocabulary member -- the gap this test tracks is no longer expressible")
		}
		return
	}
	// The moment the constant is set, it must be a real member, and it must
	// NOT be member_kind_unservable: that member claims no discovery arm
	// serves the kind, which is false for this state -- other rows in the
	// same measured replicate served projects.
	if !contractsv1.ValidContextFabricRefusalBasis(declaredKindTerminalBasis) {
		t.Fatalf("declaredKindTerminalBasis = %q is not a vocabulary member", declaredKindTerminalBasis)
	}
	if declaredKindTerminalBasis == contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
		t.Fatal("member_kind_unservable claims no discovery arm serves the declared kind, which is false here -- the kind is served, this subject was not found")
	}
}

// TestDeclaredKindsMatchesTheDerivationGraphrankReplaced is the equivalence
// the delegation rests on: DeclaredKinds must return, for every variant, what
// graphrank's frameKindHints computed before it became a delegation --
// including the vocabulary ordering both phase 4 and this decision depend on.
func TestDeclaredKindsMatchesTheDerivationGraphrankReplaced(t *testing.T) {
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	team := SubjectKind(contractsv1.ContextFabricSubjectTeam)
	frame := &QuestionFrame{SubjectExpression: SubjectExpression{
		Kind:    SubjectExpressionGroupedMembers,
		Grouped: &GroupedSetExpression{MemberKind: project, GroupKind: team},
	}}
	// The replaced body: MemberKind + GroupKind, emitted in the closed
	// vocabulary's own order. Recomputed here rather than remembered, so
	// this control fails if the vocabulary order itself moves.
	declared := map[SubjectKind]bool{}
	if kind, ok := frame.SubjectExpression.MemberKind(); ok {
		declared[kind] = true
	}
	if kind, ok := frame.SubjectExpression.GroupKind(); ok {
		declared[kind] = true
	}
	var want []SubjectKind
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		if declared[SubjectKind(kind)] {
			want = append(want, SubjectKind(kind))
		}
	}
	got := frame.DeclaredKinds()
	if len(got) != len(want) {
		t.Fatalf("DeclaredKinds() = %v, want %v", got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("DeclaredKinds() = %v, want %v", got, want)
		}
	}
}
