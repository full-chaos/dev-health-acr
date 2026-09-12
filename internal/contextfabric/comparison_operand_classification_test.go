package contextfabric

// THE COMPARISON OPERAND CLASSIFIER, against frameRoleSlots.
//
// EVERY LOOP IN THIS FILE COUNTS THE INPUTS THAT REACHED ITS ASSERTIONS AND
// FAILS AT ZERO, for the reason subject_role_test.go's header already states
// for this layer: the declaration slice shipped a coordinate derivation whose
// oracle passed while a whole operand variant was never derived. A loop that
// can `continue` past its assertions proves nothing about the cases it
// skipped.
//
// THE AGREEMENT IS ASSERTED IN BOTH DIRECTIONS, AND EACH SIDE'S EXPECTATION IS
// PINNED INDEPENDENTLY. ClassifyComparisonOperands takes its role/kind
// projection FROM frameRoleSlots, so "they agree" is true by construction and
// worth nothing on its own -- two functions sharing one authority drift
// together, and a bare equality check would pass all the way through that
// drift. So every fixture below states what IT expects, as literals, for both
// sides, and the agreement check is what proves the two readings describe the
// same frame rather than the same bug.

import (
	"testing"
)

// comparisonNamedOperand builds one named operand with its kind stated.
func comparisonNamedOperand(term string, kind SubjectKind) SubjectOperand {
	return SubjectOperand{
		Kind:  SubjectOperandNamed,
		Named: &NamedSubjectExpression{Terms: []string{term}, ExpectedKind: kindPointer(kind)},
	}
}

// comparisonNamedOperandWithoutKind is the out-of-cut shape: the question
// named a subject but did not constrain its kind.
func comparisonNamedOperandWithoutKind(term string) SubjectOperand {
	return SubjectOperand{
		Kind:  SubjectOperandNamed,
		Named: &NamedSubjectExpression{Terms: []string{term}},
	}
}

func comparisonScopedOperand(anchorTerm string, member SubjectKind) SubjectOperand {
	return SubjectOperand{
		Kind:   SubjectOperandScoped,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{anchorTerm}, MemberKind: member},
	}
}

// comparisonFrame builds a frame THROUGH the shipped derivation, never by
// hand-typing an obligation list -- the same discipline frameWith already
// applies, so a change to the goal tables moves these fixtures with it.
func comparisonFrame(operands ...SubjectOperand) QuestionFrame {
	return frameWith(
		[]InvestigationGoal{GoalCompare},
		SubjectExpression{
			Kind:     SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: operands},
		},
		TemporalIntentCurrent,
		nil,
	)
}

// operandRoleKinds is frameRoleSlots' OPERAND-role projection, in order. This
// is the independent side of the agreement: the test reads the authority
// directly rather than asking the classifier what the authority said.
func operandRoleKinds(frame QuestionFrame) []SubjectKind {
	var kinds []SubjectKind
	for _, slot := range frameRoleSlots(frame.SubjectExpression) {
		if slot.Role == SubjectRoleOperand {
			kinds = append(kinds, slot.Subject)
		}
	}
	return kinds
}

// TestClassifierAdmitsExactlyTheTwoNamedStatedKindCut is the cut itself, and
// it pins position, variant, kind and TERMS -- the terms being the whole point
// of the type, since frameRoleSlots carries the role/kind projection but not
// the per-operand terms a slot must resolve from.
func TestClassifierAdmitsExactlyTheTwoNamedStatedKindCut(t *testing.T) {
	t.Parallel()

	frame := comparisonFrame(
		comparisonNamedOperand("platform", SubjectTeam),
		comparisonNamedOperand("payments", SubjectTeam),
	)
	got := ClassifyComparisonOperands(&frame)

	if got.Admission != ComparisonAdmittedNamedPair {
		t.Fatalf("Admission = %q, want %q -- two named operands each stating a kind IS the cut", got.Admission, ComparisonAdmittedNamedPair)
	}
	if !got.Admitted() {
		t.Error("Admitted() = false on the admitted member -- the predicate and the vocabulary disagree")
	}
	if len(got.Slots) != 2 {
		t.Fatalf("Slots = %d, want 2", len(got.Slots))
	}

	// PINNED INDEPENDENTLY, as literals, rather than derived from the frame
	// the classifier just read.
	wantTerms := []string{"platform", "payments"}
	for index, slot := range got.Slots {
		if slot.Position != index {
			t.Errorf("slot %d Position = %d -- position is the ONLY ordering authority downstream, so it must be the frame's own index", index, slot.Position)
		}
		if slot.Variant != ComparisonOperandNamed {
			t.Errorf("slot %d Variant = %q, want %q", index, slot.Variant, ComparisonOperandNamed)
		}
		if slot.Kind != SubjectTeam {
			t.Errorf("slot %d Kind = %q, want %q", index, slot.Kind, SubjectTeam)
		}
		if len(slot.Terms) != 1 || slot.Terms[0] != wantTerms[index] {
			t.Errorf("slot %d Terms = %v, want [%q] -- a slot resolved from anything but its OWN terms is the defect this work removes", index, slot.Terms, wantTerms[index])
		}
	}
}

// TestClassifierAgreesWithFrameRoleSlotsInBothDirections is the cross-layer
// check, over a table whose every row states BOTH readings for itself.
func TestClassifierAgreesWithFrameRoleSlotsInBothDirections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// The frame under test.
		frame QuestionFrame
		// SIDE A, pinned independently: what frameRoleSlots must project.
		wantRoleKinds []SubjectKind
		// SIDE B, pinned independently: what the classifier must report.
		wantAdmission    ComparisonAdmission
		wantSlotKinds    []SubjectKind
		wantSlotVariants []ComparisonOperandVariant
	}{
		{
			name:             "two named teams",
			frame:            comparisonFrame(comparisonNamedOperand("a", SubjectTeam), comparisonNamedOperand("b", SubjectTeam)),
			wantRoleKinds:    []SubjectKind{SubjectTeam, SubjectTeam},
			wantAdmission:    ComparisonAdmittedNamedPair,
			wantSlotKinds:    []SubjectKind{SubjectTeam, SubjectTeam},
			wantSlotVariants: []ComparisonOperandVariant{ComparisonOperandNamed, ComparisonOperandNamed},
		},
		{
			name:             "two named operands of DIFFERENT kinds",
			frame:            comparisonFrame(comparisonNamedOperand("a", SubjectTeam), comparisonNamedOperand("b", SubjectProject)),
			wantRoleKinds:    []SubjectKind{SubjectTeam, SubjectProject},
			wantAdmission:    ComparisonAdmittedNamedPair,
			wantSlotKinds:    []SubjectKind{SubjectTeam, SubjectProject},
			wantSlotVariants: []ComparisonOperandVariant{ComparisonOperandNamed, ComparisonOperandNamed},
		},
		{
			// The ORDER case. Both readings must report project SECOND, so a
			// classifier that sorted or map-walked its slots is visible here
			// and nowhere else in this table.
			name:             "kind order follows operand position, not sort order",
			frame:            comparisonFrame(comparisonNamedOperand("a", SubjectProject), comparisonNamedOperand("b", SubjectTeam)),
			wantRoleKinds:    []SubjectKind{SubjectProject, SubjectTeam},
			wantAdmission:    ComparisonAdmittedNamedPair,
			wantSlotKinds:    []SubjectKind{SubjectProject, SubjectTeam},
			wantSlotVariants: []ComparisonOperandVariant{ComparisonOperandNamed, ComparisonOperandNamed},
		},
		{
			name:             "named beside scoped",
			frame:            comparisonFrame(comparisonNamedOperand("a", SubjectTeam), comparisonScopedOperand("anchor", SubjectProject)),
			wantRoleKinds:    []SubjectKind{SubjectTeam, SubjectProject},
			wantAdmission:    ComparisonHeldScopedOperand,
			wantSlotKinds:    []SubjectKind{SubjectTeam, SubjectProject},
			wantSlotVariants: []ComparisonOperandVariant{ComparisonOperandNamed, ComparisonOperandScoped},
		},
		{
			// frameRoleSlots emits NO slot for an operand with no stated
			// kind, which is exactly the signal the classifier reads. Both
			// sides state that here rather than one inferring it.
			name:             "one operand states no kind",
			frame:            comparisonFrame(comparisonNamedOperand("a", SubjectTeam), comparisonNamedOperandWithoutKind("b")),
			wantRoleKinds:    []SubjectKind{SubjectTeam},
			wantAdmission:    ComparisonOutOfCutUnstatedKind,
			wantSlotKinds:    []SubjectKind{SubjectTeam, ""},
			wantSlotVariants: []ComparisonOperandVariant{ComparisonOperandNamed, ComparisonOperandNamed},
		},
		{
			name:             "three operands",
			frame:            comparisonFrame(comparisonNamedOperand("a", SubjectTeam), comparisonNamedOperand("b", SubjectTeam), comparisonNamedOperand("c", SubjectTeam)),
			wantRoleKinds:    []SubjectKind{SubjectTeam, SubjectTeam, SubjectTeam},
			wantAdmission:    ComparisonOutOfCutOperandCount,
			wantSlotKinds:    []SubjectKind{SubjectTeam, SubjectTeam, SubjectTeam},
			wantSlotVariants: []ComparisonOperandVariant{ComparisonOperandNamed, ComparisonOperandNamed, ComparisonOperandNamed},
		},
		{
			name:             "one operand",
			frame:            comparisonFrame(comparisonNamedOperand("a", SubjectTeam)),
			wantRoleKinds:    []SubjectKind{SubjectTeam},
			wantAdmission:    ComparisonOutOfCutOperandCount,
			wantSlotKinds:    []SubjectKind{SubjectTeam},
			wantSlotVariants: []ComparisonOperandVariant{ComparisonOperandNamed},
		},
	}

	reached := 0
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// SIDE A, read from the authority itself.
			gotRoleKinds := operandRoleKinds(testCase.frame)
			if len(gotRoleKinds) != len(testCase.wantRoleKinds) {
				t.Fatalf("frameRoleSlots operand kinds = %v, want %v", gotRoleKinds, testCase.wantRoleKinds)
			}
			for index := range gotRoleKinds {
				if gotRoleKinds[index] != testCase.wantRoleKinds[index] {
					t.Errorf("frameRoleSlots operand kind %d = %q, want %q", index, gotRoleKinds[index], testCase.wantRoleKinds[index])
				}
			}

			// SIDE B, read from the classifier.
			got := ClassifyComparisonOperands(&testCase.frame)
			if got.Admission != testCase.wantAdmission {
				t.Errorf("Admission = %q, want %q", got.Admission, testCase.wantAdmission)
			}
			if len(got.Slots) != len(testCase.wantSlotKinds) {
				t.Fatalf("Slots = %d, want %d", len(got.Slots), len(testCase.wantSlotKinds))
			}
			for index, slot := range got.Slots {
				if slot.Kind != testCase.wantSlotKinds[index] {
					t.Errorf("slot %d Kind = %q, want %q", index, slot.Kind, testCase.wantSlotKinds[index])
				}
				if slot.Variant != testCase.wantSlotVariants[index] {
					t.Errorf("slot %d Variant = %q, want %q", index, slot.Variant, testCase.wantSlotVariants[index])
				}
			}

			// THE AGREEMENT, both directions, over the two independently
			// pinned readings above. Every kind the authority projects must
			// appear on a slot, in order; and every slot carrying a kind must
			// be one the authority projected.
			var slotKinds []SubjectKind
			for _, slot := range got.Slots {
				if slot.Kind != "" {
					slotKinds = append(slotKinds, slot.Kind)
				}
			}
			if len(slotKinds) != len(gotRoleKinds) {
				t.Fatalf("the classifier carries %d kinded slots and the authority projects %d operand kinds -- one reading sees an operand the other does not",
					len(slotKinds), len(gotRoleKinds))
			}
			for index := range slotKinds {
				if slotKinds[index] != gotRoleKinds[index] {
					t.Errorf("kinded slot %d = %q but the authority projects %q at that position", index, slotKinds[index], gotRoleKinds[index])
				}
			}
			reached++
		})
	}

	t.Cleanup(func() {
		if reached != len(cases) {
			t.Errorf("only %d of %d rows reached the agreement assertions -- a table that skips rows proves nothing about the ones it skipped", reached, len(cases))
		}
	})
}

// TestClassifierReportsEveryNonExplicitSetVariantAsNotAComparison quantifies
// over the WHOLE closed union rather than over the variants this file happened
// to think of. A variant added to the union with no case here fails by name.
func TestClassifierReportsEveryNonExplicitSetVariantAsNotAComparison(t *testing.T) {
	t.Parallel()

	expressions := map[SubjectExpressionKind]SubjectExpression{
		SubjectExpressionNamed:             namedExpression(SubjectTeam),
		SubjectExpressionGroupedMembers:    groupedExpression(SubjectProject, SubjectTeam),
		SubjectExpressionChildrenOfScope:   scopedExpression(SubjectProject),
		SubjectExpressionDiscoveredKind:    discoveredExpression(SubjectTeam),
		SubjectExpressionOrganizationScope: orgExpression(kindPointer(SubjectTeam)),
	}

	checked := 0
	for _, kind := range SubjectExpressionKindVocabulary() {
		if kind == SubjectExpressionExplicitSet {
			continue
		}
		expression, ok := expressions[kind]
		if !ok {
			t.Errorf("variant %q is in the union with no fixture here -- it has never been shown to classify as not-a-comparison", kind)
			continue
		}
		frame := frameWith([]InvestigationGoal{GoalCompare}, expression, TemporalIntentCurrent, nil)
		got := ClassifyComparisonOperands(&frame)
		if got.Admission != ComparisonNotAComparison {
			t.Errorf("variant %q classified %q, want %q", kind, got.Admission, ComparisonNotAComparison)
		}
		if len(got.Slots) != 0 {
			t.Errorf("variant %q reported %d slots; a non-comparison has no operands to describe", kind, len(got.Slots))
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no variant reached the assertions -- this test measured nothing")
	}
}

// TestClassifierRefusesANilAndAnIncompleteFrameWithoutPanicking covers the
// frames that never passed invariant I1. The classifier is reached from a
// resolution path that cannot assume validation ran.
func TestClassifierRefusesANilAndAnIncompleteFrameWithoutPanicking(t *testing.T) {
	t.Parallel()

	if got := ClassifyComparisonOperands(nil); got.Admission != ComparisonNotAComparison {
		t.Errorf("nil frame classified %q, want %q", got.Admission, ComparisonNotAComparison)
	}

	// Discriminator says explicit set, pointer absent.
	emptyPointer := QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionExplicitSet}}
	if got := ClassifyComparisonOperands(&emptyPointer); got.Admission != ComparisonNotAComparison {
		t.Errorf("explicit set with a nil Explicit classified %q, want %q", got.Admission, ComparisonNotAComparison)
	}

	// Two operands, NEITHER pointer set. The count is right and the kinds are
	// absent, so this must read as out-of-cut-unstated-kind and must still
	// report two positioned slots -- an operand the classifier cannot describe
	// is still an operand the question asked about.
	hollow := comparisonFrame(SubjectOperand{Kind: SubjectOperandNamed}, SubjectOperand{Kind: SubjectOperandNamed})
	got := ClassifyComparisonOperands(&hollow)
	if got.Admission != ComparisonOutOfCutUnstatedKind {
		t.Errorf("two hollow operands classified %q, want %q", got.Admission, ComparisonOutOfCutUnstatedKind)
	}
	if len(got.Slots) != 2 {
		t.Errorf("two hollow operands reported %d slots, want 2 -- dropping an operand the classifier cannot describe understates what the question asked", len(got.Slots))
	}
}

// TestClassifierDoesNotAliasTheFramesOwnTermSlices is a small ownership pin
// with a real hazard behind it: the returned slots travel into resolution,
// which combines and truncates term lists, and the frame is required to stay
// IMMUTABLE for planning and read evaluation downstream. A slot aliasing the
// frame's own backing array would let one of those edits reach back.
func TestClassifierDoesNotAliasTheFramesOwnTermSlices(t *testing.T) {
	t.Parallel()

	frame := comparisonFrame(
		comparisonNamedOperand("platform", SubjectTeam),
		comparisonNamedOperand("payments", SubjectTeam),
	)
	got := ClassifyComparisonOperands(&frame)
	if len(got.Slots) != 2 || len(got.Slots[0].Terms) != 1 {
		t.Fatalf("fixture shape moved: slots = %d", len(got.Slots))
	}

	got.Slots[0].Terms[0] = "MUTATED"
	if frame.SubjectExpression.Explicit.Operands[0].Named.Terms[0] != "platform" {
		t.Errorf("editing a returned slot's terms reached back into the frame (now %q) -- the frame stays immutable for planning and read evaluation, and a shared backing array is how that promise gets broken quietly",
			frame.SubjectExpression.Explicit.Operands[0].Named.Terms[0])
	}
}

// TestClassifierKeepsTwoOperandsThatNameTheSameTermAsTwoSlots is the
// no-identity-matching pin, expressed as the observable consequence rather
// than as a claim about what the function does not do.
//
// Two operands naming the same term are still TWO operands. A classifier that
// deduped them -- the first move any identity-aware implementation reaches
// for -- would report one slot and make the pair look out of cut.
func TestClassifierKeepsTwoOperandsThatNameTheSameTermAsTwoSlots(t *testing.T) {
	t.Parallel()

	frame := comparisonFrame(
		comparisonNamedOperand("platform", SubjectTeam),
		comparisonNamedOperand("platform", SubjectTeam),
	)
	got := ClassifyComparisonOperands(&frame)

	if got.Admission != ComparisonAdmittedNamedPair {
		t.Errorf("Admission = %q, want %q -- whether two operands denote the same subject is resolution's question, decided per operand against its own terms, and the classifier performs no identity matching at all",
			got.Admission, ComparisonAdmittedNamedPair)
	}
	if len(got.Slots) != 2 {
		t.Fatalf("Slots = %d, want 2 -- deduplicating operands by term is identity reasoning, and it is not this layer's to do", len(got.Slots))
	}
	if got.Slots[0].Position == got.Slots[1].Position {
		t.Error("both slots report the same position")
	}
}
