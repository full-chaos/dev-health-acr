package contextfabric

import "reflect"

// This file owns ONE rule: every DriverJudgment that reaches an assembled
// InvestigationResult carries an identity no other driver in that result
// carries.
//
// WHY IT HAS TO LIVE HERE, AT THE PRODUCER. validateDrivers
// (contracts/v1/validate_context_fabric_helpers.go) refuses a result whose
// drivers share a driver_id, and that refusal is classified ErrInvalidResult
// -> HTTP 500: ACR telling a caller that ACR built something invalid. But the
// contribution stack is assembled from two producers, and only one of them
// ever guaranteed unique ids:
//
//   - narrateCohortDriverJudgments mints ENGINE ids and has always
//     deconflicted them against the ids synthesis already used
//     (cohort_driver_narration.go).
//   - RuntimeAnswerSynthesizer.Synthesize copies the MODEL's drivers into
//     result.Drivers verbatim (`Drivers: cloneSlice(draft.Drivers)`), and
//     SynthesisDraft.ValidateAgainst -- which checks a driver's subjects,
//     path ids, evidence ids, claim grounding and group-membership closure --
//     never checked its identity.
//
// So a model that stamped two drivers with one driver_id produced a draft
// that passed every synthesis gate and an answer that ACR then refused as its
// own defect. Observed live on the kiac rig against dump dh_0906 across two
// corpus rows, at concurrency 1 and 2, on three separate acr builds: synthesis
// reported `outcome=success drivers=3` and the very next line was
// `failure_stage=validation failure_classification=invalid_result
// validation_rule="...driver IDs must be unique"`.
//
// WHAT A DUPLICATE MEANS, and why neither reading may drop a contribution.
// The two readings are separated because they are genuinely different facts
// about the model's output, and an operator who cannot tell them apart cannot
// tell a harmless restatement from a stack whose entries lost their identity:
//
//   - RESTATED: the two entries are the same contribution written twice. The
//     second says nothing the first did not, so collapsing them removes no
//     judgment from the stack.
//   - REIDENTIFIED: the two entries are DIFFERENT contributions that happen to
//     share an id. Both are real judgments the model made, so both are kept and
//     the later one is given a deterministic variant of its id. Dropping one
//     would delete a contribution from the answer because the model repeated a
//     string -- the exact trade cohort narration already settled in
//     deconflictDriverJudgmentID's own doc comment ("Dropping the narrated
//     driver instead would be worse: a judgment the cohort earned would vanish
//     because the model picked a string"). This applies that decided rule to
//     the model's own drivers rather than inventing a second one.
//
// A driver_id is a LOCAL handle: nothing in the result contract references a
// driver by id (a driver cites claims and paths, never the other way round),
// so re-identifying one changes no closure. Deconfliction is deterministic, so
// a replay of the same draft produces the same ids as the stored answer.

// DriverIdentityCollisions is what ResolveDriverIdentityCollisions found in
// one draft: content-safe by construction, two counts and nothing else --
// never a driver_id, a title, or any other model text.
type DriverIdentityCollisions struct {
	// Restated is how many entries were dropped as an exact restatement of a
	// driver already carrying that id.
	Restated int
	// Reidentified is how many DISTINCT entries were kept under a
	// deterministically deconflicted id.
	Reidentified int
}

// Total is the number of colliding entries seen, restated and reidentified
// together -- the single number that answers "did this draft collide at all".
func (c DriverIdentityCollisions) Total() int { return c.Restated + c.Reidentified }

// ResolveDriverIdentityCollisions returns drivers with unique DriverIDs and
// the counts naming what it did. It preserves input order, and on a draft
// whose ids are already unique it returns an equal slice and a zero
// DriverIdentityCollisions -- the ordinary case, and the one the caller still
// reports so a pass that CHECKED identity is distinguishable from a pass that
// never ran the check at all.
func ResolveDriverIdentityCollisions(drivers []DriverJudgment) ([]DriverJudgment, DriverIdentityCollisions) {
	var counts DriverIdentityCollisions
	if len(drivers) == 0 {
		return drivers, counts
	}
	// taken holds every id already SERVED by this slice, including the ids
	// deconfliction itself minted -- a variant must clear the whole set, not
	// only the model's own ids.
	taken := make(map[string]struct{}, len(drivers))
	// keptByAuthoredID is keyed by the id the MODEL wrote, and holds the
	// entries kept under it in their pre-deconfliction form. Restatement is
	// judged against what the model authored, never against an entry this
	// function has already rewritten, so the comparison describes the draft
	// rather than this function's own effect on it.
	keptByAuthoredID := make(map[string][]DriverJudgment, len(drivers))
	resolved := make([]DriverJudgment, 0, len(drivers))
	for _, driver := range drivers {
		authoredID := driver.DriverID
		if isRestatedDriver(keptByAuthoredID[authoredID], driver) {
			counts.Restated++
			continue
		}
		keptByAuthoredID[authoredID] = append(keptByAuthoredID[authoredID], driver)
		if _, clash := taken[authoredID]; clash {
			counts.Reidentified++
			driver.DriverID = deconflictDriverJudgmentID(authoredID, taken)
		}
		taken[driver.DriverID] = struct{}{}
		resolved = append(resolved, driver)
	}
	return resolved, counts
}

// isRestatedDriver reports whether candidate is an exact restatement of one
// of the entries already kept under the same authored id.
//
// reflect.DeepEqual, not a hand-written field census: a field added to
// DriverJudgment later must participate in this comparison automatically. A
// census would keep compiling while silently collapsing two entries that
// differ only in the new field -- which is the one outcome this function
// exists to prevent (a contribution deleted because two entries LOOKED the
// same).
func isRestatedDriver(kept []DriverJudgment, candidate DriverJudgment) bool {
	for _, existing := range kept {
		if reflect.DeepEqual(existing, candidate) {
			return true
		}
	}
	return false
}
