package hosted_test

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestCHAOS4336_ClassifyWindowGateOutcome_ReadsInferredsOwnCall is the
// red-first proof for CHAOS-4336's actual defect: WindowGatedOfferedCount/
// WindowGatedSilentCount used to read turn1Facts.WindowExpandOffered (the
// case's shared, window-blind turn1 call) instead of the inferred_tier
// arm's own gated call. This fixture is exactly the live shape that
// exposed it -- CHAOS-4314's Run C and CHAOS-4336's own Run D both had
// cases where the inferred_tier arm's own window call was correctly
// offered (composeWindowExpandOption fired) while the case's shared turn1
// call never reached a window gate at all and so had nothing to offer.
// Under the old (turn1Facts-reading) logic this fixture reports SILENT even
// though the call the report is actually gated on (inferred) was OFFERED --
// see twoTurnClassifyWindowGateOutcome's own doc comment for the full
// mechanism and the live evidence.
func TestCHAOS4336_ClassifyWindowGateOutcome_ReadsInferredsOwnCall(t *testing.T) {
	inferred := twoTurnCaseResult{
		Index:                       11,
		Member:                      string(contractsv1.ContextFabricStructureNeedWindow),
		Arm:                         "inferred_tier",
		Turn2Status:                 string(contractsv1.ContextFabricInvestigationClarificationRequired),
		TierRoutedCorrectly:         true,
		CommittedCount:              0,
		InferredWindowExpandOffered: true,  // the inferred_tier arm's OWN call: gate 2, offered=true
		Turn1WindowExpandOffered:    false, // the case's SHARED turn1 call: never reached a window gate at all
	}

	got := twoTurnClassifyWindowGateOutcome(inferred)

	if !got.Gated {
		t.Fatalf("Gated = false, want true: Turn2Status/TierRoutedCorrectly/CommittedCount all satisfy the window_gated condition")
	}
	if !got.GatedOffered {
		t.Fatalf("GatedOffered = false, want true: inferred.InferredWindowExpandOffered=true means the inferred_tier arm's OWN gated call WAS offered a wider tier -- the report must reflect that call, not the unrelated shared turn1 call (which was silent here, Turn1WindowExpandOffered=false)")
	}
	if got.ArmError {
		t.Fatalf("ArmError = true, want false: ArmInvalidReason is empty")
	}
}

// TestCHAOS4336_ClassifyWindowGateOutcome_SilentWhenInferredsOwnCallIsSilent
// is the mirror case: when the inferred_tier arm's OWN call was NOT
// offered, the outcome must be silent regardless of what the shared turn1
// call happened to show -- proves the fix reads the right field in BOTH
// directions, not just the direction the red-first fixture above covers.
func TestCHAOS4336_ClassifyWindowGateOutcome_SilentWhenInferredsOwnCallIsSilent(t *testing.T) {
	inferred := twoTurnCaseResult{
		Index:                       0,
		Member:                      string(contractsv1.ContextFabricStructureNeedWindow),
		Arm:                         "inferred_tier",
		Turn2Status:                 string(contractsv1.ContextFabricInvestigationClarificationRequired),
		TierRoutedCorrectly:         true,
		CommittedCount:              0,
		InferredWindowExpandOffered: false, // the inferred_tier arm's OWN call: genuinely silent
		Turn1WindowExpandOffered:    true,  // the shared turn1 call happened to be offered -- must NOT leak in
	}

	got := twoTurnClassifyWindowGateOutcome(inferred)

	if !got.Gated {
		t.Fatal("Gated = false, want true")
	}
	if got.GatedOffered {
		t.Fatal("GatedOffered = true, want false: inferred.InferredWindowExpandOffered=false must win even though the unrelated Turn1WindowExpandOffered=true")
	}
}

// TestCHAOS4336_ClassifyWindowGateOutcome_ArmErrorAndNotGated round out the
// partition: an errored call reports ArmError only, and a call that never
// reaches the gated shape (e.g. committed, or Turn2Status not
// clarification_required) reports neither Gated nor GatedOffered.
func TestCHAOS4336_ClassifyWindowGateOutcome_ArmErrorAndNotGated(t *testing.T) {
	t.Run("arm error", func(t *testing.T) {
		got := twoTurnClassifyWindowGateOutcome(twoTurnCaseResult{ArmInvalidReason: "investigate error: synthesis_rejected"})
		if !got.ArmError {
			t.Fatal("ArmError = false, want true")
		}
		if got.Gated || got.GatedOffered || got.Committed {
			t.Fatalf("got = %#v, want only ArmError set", got)
		}
	})
	t.Run("committed, not gated", func(t *testing.T) {
		got := twoTurnClassifyWindowGateOutcome(twoTurnCaseResult{
			Turn2Status:         string(contractsv1.ContextFabricInvestigationClarificationRequired),
			TierRoutedCorrectly: true,
			CommittedCount:      1,
		})
		if !got.Committed {
			t.Fatal("Committed = false, want true")
		}
		if got.Gated || got.GatedOffered {
			t.Fatalf("got = %#v, want Gated/GatedOffered both false: CommittedCount>0 fails the gated condition", got)
		}
	})
	t.Run("not tier-routed, not gated", func(t *testing.T) {
		got := twoTurnClassifyWindowGateOutcome(twoTurnCaseResult{
			Turn2Status:                 string(contractsv1.ContextFabricInvestigationClarificationRequired),
			TierRoutedCorrectly:         false,
			CommittedCount:              0,
			InferredWindowExpandOffered: true,
		})
		if got.Gated || got.GatedOffered {
			t.Fatalf("got = %#v, want Gated/GatedOffered both false: TierRoutedCorrectly=false fails the gated condition", got)
		}
	})
}

// TestCHAOS4336_ClassifyWindowGateOutcome_AlreadyWidestExcludedFromSilent is
// the red-first proof for the CHAOS-4336 follow-up: a gated call whose own
// effective window is already the registry's widest tier (all_time) has
// nothing wider pickWindowExpandTarget could ever recommend -- a
// legitimate non-offer, not a defect. Before this fix, InferredWindowAlreadyWidest
// did not exist and every non-offered gated row landed in
// WindowGatedSilentCount regardless of cause, which is exactly what Run E
// (16-shard kiac, tip a5f5f900) measured: window_gated_silent=2/65, both
// rows (cases 53, 56) later confirmed all_time via a by-hand annex
// cross-check that should never have been necessary. This fixture
// reproduces that shape directly: InferredWindowExpandOffered=false AND
// InferredWindowAlreadyWidest=true must report GatedAlreadyWidest=true,
// not silent.
func TestCHAOS4336_ClassifyWindowGateOutcome_AlreadyWidestExcludedFromSilent(t *testing.T) {
	inferred := twoTurnCaseResult{
		Index:                       53,
		Member:                      string(contractsv1.ContextFabricStructureNeedWindow),
		Arm:                         "inferred_tier",
		Turn2Status:                 string(contractsv1.ContextFabricInvestigationClarificationRequired),
		TierRoutedCorrectly:         true,
		CommittedCount:              0,
		InferredWindowExpandOffered: false,
		InferredWindowAlreadyWidest: true, // the inferred_tier arm's OWN call resolved to all_time: nothing wider exists
	}

	got := twoTurnClassifyWindowGateOutcome(inferred)

	if !got.Gated {
		t.Fatal("Gated = false, want true")
	}
	if got.GatedOffered {
		t.Fatal("GatedOffered = true, want false: no window_expand was actually composed")
	}
	if !got.GatedAlreadyWidest {
		t.Fatal("GatedAlreadyWidest = false, want true: InferredWindowAlreadyWidest=true means there was genuinely nothing wider to recommend -- this must NOT fall through to silent")
	}
}

// TestCHAOS4336_ClassifyWindowGateOutcome_GenuineSilentStillSilent proves
// the partition did not accidentally swallow the real defect signal: a
// gated call that was neither offered nor already-widest is still
// reported silent (the population the →0 bar actually targets).
func TestCHAOS4336_ClassifyWindowGateOutcome_GenuineSilentStillSilent(t *testing.T) {
	got := twoTurnClassifyWindowGateOutcome(twoTurnCaseResult{
		Turn2Status:                 string(contractsv1.ContextFabricInvestigationClarificationRequired),
		TierRoutedCorrectly:         true,
		CommittedCount:              0,
		InferredWindowExpandOffered: false,
		InferredWindowAlreadyWidest: false,
	})
	if !got.Gated {
		t.Fatal("Gated = false, want true")
	}
	if got.GatedOffered || got.GatedAlreadyWidest {
		t.Fatalf("got = %#v, want GatedOffered/GatedAlreadyWidest both false: neither an offer nor a legitimate already-widest reading", got)
	}
}
