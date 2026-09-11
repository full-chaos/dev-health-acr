package contextfabric

import (
	"context"
	"fmt"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE INPUT DOMAIN of every guard, validator and counter this change adds or
// modifies, enumerated and EXECUTED in one pass rather than sampled.
//
// The domain per field is {absent, null, zero, empty container, wrong container
// type, wrong scalar type, fractional where integral, out of vocabulary,
// boundary-1, boundary, boundary+1, duplicate, canonical}. Several cells are
// STRUCTURALLY UNREACHABLE in Go and are recorded as such with the reason,
// never silently dropped:
//
//   - wrong container type / wrong scalar type: the fields are statically
//     typed, so a wrong type is a compile error, not a runtime input. There is
//     no wire decode into these types -- the declaration is a compile-time
//     table and readEvidence is unexported and never deserialized -- so no
//     untrusted path can present one.
//   - fractional where integral: the count fields are `int`. Same reason.
//   - null: only a nil map/slice is expressible, which is the "absent"/"empty
//     container" cell already covered; there are no pointer fields here.
//
// Every REACHABLE cell below is executed and its outcome asserted. A cell whose
// outcome is "accepted" when it should be refused is a finding, and this file
// records one that was found this way (see the ACCEPTS-EMPTY-KEY row).
func TestTheObservationInputDomainIsEnumeratedAndExecuted(t *testing.T) {
	t.Parallel()

	type row struct {
		surface string
		field   string
		cell    string
		got     string
		want    string
	}
	var table []row
	record := func(surface, field, cell, got, want string) {
		table = append(table, row{surface, field, cell, got, want})
	}

	// ---------------------------------------------------------------- surface 1
	// FactCapability.Validate -- the observation-key declaration guard.
	base := func() FactCapability {
		return FactCapability{
			Kind: FactHealth, Name: "health", Version: "v1",
			SupportedSubjectKinds: []SubjectKind{SubjectTeam},
			RequiresEvidence:      true,
			Dimension:             HealthDimensionDeliveryFlow,
			SubjectRoles:          []FactRole{FactRoleSubject},
			Obligations:           map[SubjectKind][]AnswerObligation{SubjectTeam: nil},
		}
	}
	validateCell := func(cell string, mutate func(*FactCapability), want string) {
		c := base()
		mutate(&c)
		err := c.Validate()
		got := "accepted"
		if err != nil {
			got = "refused"
		}
		record("FactCapability.Validate", "ObservationKey", cell, got, want)
		if got != want {
			t.Errorf("Validate/%s = %s, want %s (err=%v)", cell, got, want, err)
		}
	}

	validateCell("absent (field never set)", func(c *FactCapability) {}, "accepted")
	validateCell("empty map", func(c *FactCapability) {
		c.ObservationKey = map[SubjectKind][]ObservationKey{}
	}, "accepted")
	validateCell("nil list for a served subject kind", func(c *FactCapability) {
		c.ObservationKey = map[SubjectKind][]ObservationKey{SubjectTeam: nil}
	}, "accepted")
	validateCell("empty list for a served subject kind", func(c *FactCapability) {
		c.ObservationKey = map[SubjectKind][]ObservationKey{SubjectTeam: {}}
	}, "accepted")
	validateCell("canonical single key", func(c *FactCapability) {
		c.ObservationKey = map[SubjectKind][]ObservationKey{SubjectTeam: {"team_risk_rollup"}}
	}, "accepted")
	validateCell("duplicate key within one cell", func(c *FactCapability) {
		c.ObservationKey = map[SubjectKind][]ObservationKey{SubjectTeam: {"k", "k"}}
	}, "accepted")
	validateCell("UNSUPPORTED subject kind (the clause's own rule)", func(c *FactCapability) {
		c.ObservationKey = map[SubjectKind][]ObservationKey{SubjectProject: {"k"}}
	}, "refused")
	// THE FINDING. An empty key string is not a key: dedupeObservationKeys drops
	// it, so the cell silently behaves as UNKEYED while reading, to anyone
	// editing the registry, like a declared pairing. The registry guard should
	// refuse it. It currently does not -- recorded, not hidden.
	validateCell("EMPTY key string (out of vocabulary)", func(c *FactCapability) {
		c.ObservationKey = map[SubjectKind][]ObservationKey{SubjectTeam: {""}}
	}, "refused")
	// WHITESPACE-ONLY is a distinct cell from EMPTY, and it was missed on the
	// first pass: the guard was written with TrimSpace, but nothing proved the
	// TrimSpace was load-bearing, so a mutation removing it SURVIVED. A blank
	// key is the same defect as an empty one -- it reads as a declared pairing
	// and pairs with nothing -- and "out of vocabulary" for a string field
	// covers both.
	validateCell("WHITESPACE-ONLY key string (out of vocabulary)", func(c *FactCapability) {
		c.ObservationKey = map[SubjectKind][]ObservationKey{SubjectTeam: {"   "}}
	}, "refused")
	validateCell("key with surrounding whitespace (not canonical, still a key)", func(c *FactCapability) {
		c.ObservationKey = map[SubjectKind][]ObservationKey{SubjectTeam: {" team_risk_rollup "}}
	}, "accepted")

	// ---------------------------------------------------------------- surface 2
	// observationCover -- the counter.
	a := teamAssignment()
	coverCell := func(cell string, served []FactKind, subject SubjectKind, assignment observationKeyAssignment, want int) {
		got := observationCover(served, subject, assignment)
		record("observationCover", "served/subject/assignment", cell, fmt.Sprint(got), fmt.Sprint(want))
		if got != want {
			t.Errorf("observationCover/%s = %d, want %d", cell, got, want)
		}
	}
	coverCell("served absent (nil slice)", nil, SubjectTeam, a, 0)
	coverCell("served empty container", []FactKind{}, SubjectTeam, a, 0)
	coverCell("assignment absent (nil map)", []FactKind{FactHealth, FactFlow}, SubjectTeam, nil, 2)
	coverCell("assignment empty container", []FactKind{FactHealth, FactFlow}, SubjectTeam, observationKeyAssignment{}, 2)
	coverCell("subject out of vocabulary", []FactKind{FactHealth, FactFlow}, SubjectKind("not_a_subject_kind"), a, 2)
	coverCell("served kind out of vocabulary", []FactKind{FactKind("not_a_kind")}, SubjectTeam, a, 1)
	coverCell("duplicate served kind", []FactKind{FactHealth, FactHealth}, SubjectTeam, a, 1)
	coverCell("boundary: threshold-1 kinds (one)", []FactKind{FactHealth}, SubjectTeam, a, 1)
	coverCell("boundary: exactly two independent", []FactKind{FactHealth, FactFlow}, SubjectTeam, a, 2)
	coverCell("boundary+1: three, two independent", []FactKind{FactHealth, FactFlow, FactOperationalDeficiencies}, SubjectTeam, a, 2)
	coverCell("canonical: the ruled collapse", []FactKind{FactOperationalDeficiencies, FactHealth}, SubjectTeam, a, 1)
	// empty and duplicate KEYS inside the assignment
	coverCell("empty key string in the assignment (dropped, cell reads unkeyed)",
		[]FactKind{FactHealth}, SubjectTeam,
		observationKeyAssignment{FactHealth: {SubjectTeam: {""}}}, 1)
	coverCell("duplicate keys in one cell (deduped, still one observation)",
		[]FactKind{FactHealth, FactFlow}, SubjectTeam,
		observationKeyAssignment{FactHealth: {SubjectTeam: {"k", "k"}}, FactFlow: {SubjectTeam: {"k"}}}, 1)

	// ---------------------------------------------------------------- surface 3
	// servedObservationCover -- the taint guard. All five states are enumerated
	// in TestEveryTaintStateIsExercised; here the DEGENERATE inputs.
	taintCell := func(cell string, ev readEvidence, want int) {
		got := servedObservationCover(ev, SubjectTeam, a)
		record("servedObservationCover", "evidence", cell, fmt.Sprint(got), fmt.Sprint(want))
		if got != want {
			t.Errorf("servedObservationCover/%s = %d, want %d", cell, got, want)
		}
	}
	taintCell("both lists absent", readEvidence{}, 0)
	taintCell("served absent, observed present", readEvidence{ObservedKinds: []FactKind{FactHealth}}, 0)
	taintCell("served present, observed absent (nothing to taint)",
		readEvidence{ServedKinds: []FactKind{FactHealth}}, 1)
	// A served kind absent from ObservedKinds is a CALLER INCONSISTENCY, and the
	// answer here is 1, not 0 -- the lane's first expectation of 0 was wrong.
	// What is served is counted; health's own observation was not tainted by the
	// lost flow read, so it covers 1. The invariant served subset-of observed is
	// guaranteed by the only producer (evaluateReadRequirement builds both lists
	// from one pass over the same coverage), so this cell is structurally
	// unreachable in production and is recorded to say what it WOULD do rather
	// than to demand a guard against a caller that cannot exist.
	taintCell("served kind not in observed (caller inconsistency, unreachable)",
		readEvidence{ObservedKinds: []FactKind{FactFlow}, ServedKinds: []FactKind{FactHealth}}, 1)
	taintCell("duplicate in both lists",
		readEvidence{ObservedKinds: []FactKind{FactHealth, FactHealth}, ServedKinds: []FactKind{FactHealth, FactHealth}}, 1)

	// ---------------------------------------------------------------- surface 4
	// ValidateObservationCoverBound -- the construction bound. Its contract: at
	// every subject kind, the number of DISTINCT fact kinds carrying at least one
	// non-empty observation key must not exceed observationCoverKindGuard.
	//
	// This surface was added after the first 28 cells: the bound guard came in
	// with a later fix and its domain was never enumerated. Enumerating it found
	// one cell the guard did not treat as its contract says -- a duplicate kind
	// was counted twice, refusing 20 distinct kinds -- now fixed and pinned here.
	keyedAt := func(kind FactKind, subject SubjectKind, keys ...ObservationKey) FactCapability {
		c := boundCapability(kind, false)
		c.ObservationKey = map[SubjectKind][]ObservationKey{subject: keys}
		return c
	}
	keyedKinds := func(n int, subject SubjectKind) []FactCapability {
		out := make([]FactCapability, 0, n)
		for _, kind := range boundKinds[:n] {
			out = append(out, keyedAt(kind, subject, "one_shared_observation"))
		}
		return out
	}
	eachKind := func(kinds []FactKind, build func(FactKind) FactCapability) []FactCapability {
		out := make([]FactCapability, 0, len(kinds))
		for _, kind := range kinds {
			out = append(out, build(kind))
		}
		return out
	}
	guard := observationCoverKindGuard
	boundCell := func(cell string, capabilities []FactCapability, want string) {
		got := "accepted"
		if err := ValidateObservationCoverBound(capabilities); err != nil {
			got = "refused"
		}
		record("ValidateObservationCoverBound", "capabilities", cell, got, want)
		if got != want {
			t.Errorf("ValidateObservationCoverBound/%s = %s, want %s", cell, got, want)
		}
	}
	boundCell("capabilities absent (nil)", nil, "accepted")
	boundCell("capabilities empty container", []FactCapability{}, "accepted")
	boundCell("zero keyed: all 22 kinds unkeyed", eachKind(boundKinds, func(k FactKind) FactCapability {
		return boundCapability(k, false)
	}), "accepted")
	boundCell("empty key map on all 22", eachKind(boundKinds, func(k FactKind) FactCapability {
		c := boundCapability(k, false)
		c.ObservationKey = map[SubjectKind][]ObservationKey{}
		return c
	}), "accepted")
	boundCell("nil key list on all 22 (reads unkeyed)", eachKind(boundKinds, func(k FactKind) FactCapability {
		return keyedAt(k, SubjectTeam)
	}), "accepted")
	boundCell("empty-string-only key on all 22 (dropped, reads unkeyed)", eachKind(boundKinds, func(k FactKind) FactCapability {
		return keyedAt(k, SubjectTeam, "")
	}), "accepted")
	// A blank key is a key to the solve (dedupe drops only ""), so 21 blank-keyed
	// kinds are 21 bits. Validate refuses a blank key before this in the
	// registry; the bound still counts what the solve would count.
	boundCell("whitespace-only key on bound+1 kinds", eachKind(boundKinds[:guard+1], func(k FactKind) FactCapability {
		return keyedAt(k, SubjectTeam, "   ")
	}), "refused")
	boundCell("boundary-1 keyed kinds", keyedKinds(guard-1, SubjectTeam), "accepted")
	boundCell("boundary: exactly the bound (canonical)", keyedKinds(guard, SubjectTeam), "accepted")
	boundCell("boundary+1 keyed kinds", keyedKinds(guard+1, SubjectTeam), "refused")
	boundCell("bound kinds, duplicate keys in every cell", eachKind(boundKinds[:guard], func(k FactKind) FactCapability {
		return keyedAt(k, SubjectTeam, "one_shared_observation", "one_shared_observation", "second")
	}), "accepted")
	boundCell("duplicate kind: bound distinct plus one repeat",
		append(keyedKinds(guard, SubjectTeam), keyedAt(boundKinds[0], SubjectTeam, "one_shared_observation")), "accepted")
	boundCell("duplicate kind: bound+1 distinct plus one repeat",
		append(keyedKinds(guard+1, SubjectTeam), keyedAt(boundKinds[0], SubjectTeam, "one_shared_observation")), "refused")
	spread := keyedKinds(guard+1, SubjectTeam)
	for i := range spread {
		if i%2 == 1 {
			spread[i] = keyedAt(boundKinds[i], SubjectProject, "one_shared_observation")
		}
	}
	boundCell("bound+1 kinds spread over two subject kinds", spread, "accepted")
	boundCell("bound+1 kinds each keyed at two subject kinds", eachKind(boundKinds[:guard+1], func(k FactKind) FactCapability {
		c := boundCapability(k, false)
		c.ObservationKey = map[SubjectKind][]ObservationKey{
			SubjectTeam: {"one_shared_observation"}, SubjectProject: {"one_shared_observation"},
		}
		return c
	}), "refused")
	oneUnkeyed := keyedKinds(guard+1, SubjectTeam)
	oneUnkeyed[0] = keyedAt(boundKinds[0], SubjectTeam)
	boundCell("bound+1 entries, one with a nil key list", oneUnkeyed, "accepted")
	boundCell("subject kind out of vocabulary, bound+1", keyedKinds(guard+1, SubjectKind("not_a_subject_kind")), "refused")

	// ---------------------------------------------------------------- surface 5
	// (*Engine).recordObservationCover -- the served-pass marker. Its contract:
	// every event is published exactly once, and Served is true iff the event's
	// Pass equals the HIGHEST Pass among the events, whatever order they arrive
	// in and whatever Served they carried in. got/want list the published
	// events as pass:served in publish order.
	markCell := func(cell string, telemetryPresent bool, events []ReadRequirementObservationCoverEvent, want string) {
		sink := &recordingTelemetry{}
		engine := &Engine{}
		if telemetryPresent {
			engine.telemetry = sink
		}
		engine.recordObservationCover(context.Background(), storage.Principal{}, events)
		parts := make([]string, 0, len(sink.readRequirementObservationCovers))
		for _, event := range sink.readRequirementObservationCovers {
			parts = append(parts, fmt.Sprintf("%d:%t", event.Pass, event.Served))
		}
		got := strings.Join(parts, ",")
		if got == "" {
			got = "none"
		}
		record("recordObservationCover", "events", cell, got, want)
		if got != want {
			t.Errorf("recordObservationCover/%s = %s, want %s", cell, got, want)
		}
	}
	passes := func(pass ...int) []ReadRequirementObservationCoverEvent {
		out := make([]ReadRequirementObservationCoverEvent, 0, len(pass))
		for _, p := range pass {
			out = append(out, ReadRequirementObservationCoverEvent{Requirement: "state", Pass: p})
		}
		return out
	}
	markCell("events absent (nil)", true, nil, "none")
	markCell("events empty container", true, passes(), "none")
	markCell("telemetry sink absent", false, passes(answerPassFirst, answerPassSecond), "none")
	markCell("canonical: one pass", true, passes(answerPassFirst), "0:true")
	markCell("two passes in order", true, passes(answerPassFirst, answerPassSecond), "0:false,1:true")
	markCell("two passes out of order", true, passes(answerPassSecond, answerPassFirst), "1:true,0:false")
	markCell("boundary: the last pass index", true, passes(answerPassFirst, answerPassSecond, answerPassThird), "0:false,1:false,2:true")
	markCell("duplicate: two rows at the final pass", true, passes(answerPassFirst, answerPassSecond, answerPassSecond), "0:false,1:true,1:true")
	markCell("duplicate: every row at one pass", true, passes(answerPassSecond, answerPassSecond), "1:true,1:true")
	stale := passes(answerPassFirst, answerPassSecond)
	stale[0].Served = true
	markCell("incoming Served=true on a discarded pass (overwritten)", true, stale, "0:false,1:true")

	// ---------------------------------------------------------------- surface 5b
	// carryObservationCover -- what a re-finalizing pass re-states for the rows
	// it CARRIES. Its contract: for each carried identity, the latest earlier
	// event for that identity, re-tagged with this pass, evaluated_pass kept,
	// served left for emit to decide; nothing for an identity no earlier pass
	// evaluated; one event per identity. got/want: requirement:pass:evaluated.
	carryEvent := func(requirement string, pass, evaluated int, served bool) ReadRequirementObservationCoverEvent {
		return ReadRequirementObservationCoverEvent{Requirement: requirement, Pass: pass, EvaluatedPass: evaluated, Served: served}
	}
	carryCell := func(cell string, prior []ReadRequirementObservationCoverEvent, carried []string, pass int, want string) {
		parts := []string{}
		for _, event := range carryObservationCover(prior, carried, pass) {
			parts = append(parts, fmt.Sprintf("%s:%d:%d:%t", event.Requirement, event.Pass, event.EvaluatedPass, event.Served))
		}
		got := strings.Join(parts, ",")
		if got == "" {
			got = "none"
		}
		record("carryObservationCover", "prior/carried/pass", cell, got, want)
		if got != want {
			t.Errorf("carryObservationCover/%s = %s, want %s", cell, got, want)
		}
	}
	carryCell("carried absent (nil)", []ReadRequirementObservationCoverEvent{carryEvent("r", 0, 0, false)}, nil, 1, "none")
	carryCell("carried empty container", []ReadRequirementObservationCoverEvent{carryEvent("r", 0, 0, false)}, []string{}, 1, "none")
	carryCell("prior absent (a row no earlier pass evaluated)", nil, []string{"r"}, 1, "none")
	carryCell("canonical: pass 0 evaluated, pass 1 carries", []ReadRequirementObservationCoverEvent{carryEvent("r", 0, 0, false)}, []string{"r"}, 1, "r:1:0:false")
	carryCell("latest earlier pass wins: 0 and 1 evaluated, pass 2 carries",
		[]ReadRequirementObservationCoverEvent{carryEvent("r", 0, 0, false), carryEvent("r", 1, 1, false)}, []string{"r"}, 2, "r:2:1:false")
	carryCell("latest earlier pass wins regardless of arrival order",
		[]ReadRequirementObservationCoverEvent{carryEvent("r", 1, 1, false), carryEvent("r", 0, 0, false)}, []string{"r"}, 2, "r:2:1:false")
	carryCell("carrying a carried line keeps the ORIGINAL evaluator",
		[]ReadRequirementObservationCoverEvent{carryEvent("r", 0, 0, false), carryEvent("r", 1, 0, false)}, []string{"r"}, 2, "r:2:0:false")
	carryCell("boundary: a prior at THIS pass is not earlier", []ReadRequirementObservationCoverEvent{carryEvent("r", 1, 1, false)}, []string{"r"}, 1, "none")
	carryCell("boundary+1: a prior at a LATER pass is not earlier", []ReadRequirementObservationCoverEvent{carryEvent("r", 2, 2, false)}, []string{"r"}, 1, "none")
	carryCell("identity out of vocabulary (prior names another requirement)", []ReadRequirementObservationCoverEvent{carryEvent("other", 0, 0, false)}, []string{"r"}, 1, "none")
	carryCell("duplicate carried identity (one line per identity)", []ReadRequirementObservationCoverEvent{carryEvent("r", 0, 0, false)}, []string{"r", "r"}, 1, "r:1:0:false")
	carryCell("incoming served=true is not carried over (emit decides served)", []ReadRequirementObservationCoverEvent{carryEvent("r", 0, 0, true)}, []string{"r"}, 1, "r:1:0:false")
	carryCell("two identities, each from its own latest pass",
		[]ReadRequirementObservationCoverEvent{carryEvent("a", 0, 0, false), carryEvent("b", 0, 0, false), carryEvent("b", 1, 1, false)}, []string{"a", "b"}, 2, "a:2:0:false,b:2:1:false")

	// ---------------------------------------------------------------- surface 6
	// readRequirementObservationCoverEvent -- the cover line's field builder,
	// driven through its only production caller, readRequirementOutcomeRow, so
	// served cover and declared come from the production formulas. Its
	// contract, field by field: kind counts are DISTINCT kinds; collapsed is
	// served kinds minus the served cover BEFORE the taint; tainted counts only
	// keys a served kind stands on; declared is the observed cover raised to the
	// threshold; meets is served cover >= threshold.
	//
	// got/want: "kinds o/s cover o/s collapsed tainted declared raised meets".
	eventRequirement := contractsv1.ContextFabricPlanRequirement{
		Requirement: "principal_drivers", Obligation: "drivers",
		Subject: contractsv1.ContextFabricSubjectKind(SubjectTeam),
	}
	eventCell := func(cell string, threshold int, ev readEvidence, want string) {
		got := "no event"
		if _, _, event := readRequirementOutcomeRow(eventRequirement, threshold, ev, readPopulationEvidence{assignment: a}); event != nil {
			got = fmt.Sprintf("kinds %d/%d cover %d/%d collapsed %d tainted %d declared %d raised %t meets %t",
				event.ObservedKinds, event.ServedKinds, event.ObservedCover, event.ServedCover,
				event.CollapsedObservations, event.TaintedObservations, event.Declared,
				event.DeclaredRaisedToStandard, event.MeetsThreshold)
		}
		record("readRequirementObservationCoverEvent", "evidence/threshold", cell, got, want)
		if got != want {
			t.Errorf("readRequirementObservationCoverEvent/%s = %s, want %s", cell, got, want)
		}
	}
	served := func(observed, servedKinds []FactKind) readEvidence {
		return readEvidence{
			Observed: len(observed), Served: len(servedKinds), Narrowed: len(observed) - len(servedKinds),
			ObservedKinds: observed, ServedKinds: servedKinds,
		}
	}
	eventCell("evidence absent (nothing observed: the measured zero)", 2, readEvidence{},
		"kinds 0/0 cover 0/0 collapsed 0 tainted 0 declared 2 raised true meets false")
	eventCell("canonical: two kinds that are one observation, nothing lost", 2,
		served([]FactKind{FactOperationalDeficiencies, FactHealth}, []FactKind{FactOperationalDeficiencies, FactHealth}),
		"kinds 2/2 cover 1/1 collapsed 1 tainted 0 declared 2 raised true meets false")
	eventCell("two independent kinds, nothing lost", 2,
		served([]FactKind{FactHealth, FactFlow}, []FactKind{FactHealth, FactFlow}),
		"kinds 2/2 cover 2/2 collapsed 0 tainted 0 declared 2 raised false meets true")
	eventCell("duplicate served and observed kind (counted once)", 1,
		served([]FactKind{FactHealth, FactHealth}, []FactKind{FactHealth, FactHealth}),
		"kinds 1/1 cover 1/1 collapsed 0 tainted 0 declared 1 raised false meets true")
	eventCell("duplicate observed kind only (counted once)", 1,
		readEvidence{Observed: 2, Served: 1, ObservedKinds: []FactKind{FactHealth, FactHealth}, ServedKinds: []FactKind{FactHealth}},
		"kinds 1/1 cover 1/1 collapsed 0 tainted 0 declared 1 raised false meets true")
	// A served kind whose ONLY observation is also lost elsewhere drops out of
	// the served cover without collapsing into anything: collapsed stays 0.
	eventCell("taint-only drop: served kind's only key is lost elsewhere", 2,
		served([]FactKind{FactHealth, FactOperationalDeficiencies}, []FactKind{FactHealth}),
		"kinds 2/1 cover 1/0 collapsed 0 tainted 1 declared 2 raised true meets false")
	// The live shape at team: a collapse AND a taint in one row.
	eventCell("collapse and taint together (the live shape)", 2,
		served([]FactKind{FactHealth, FactInvestment, FactMetrics, FactOperationalDeficiencies, FactReadiness, FactWorkload},
			[]FactKind{FactHealth, FactInvestment, FactOperationalDeficiencies, FactWorkload}),
		"kinds 6/4 cover 5/3 collapsed 1 tainted 1 declared 5 raised false meets true")
	eventCell("lost kind whose key no served kind stands on (no taint)", 2,
		served([]FactKind{FactHealth, FactFlow}, []FactKind{FactHealth}),
		"kinds 2/1 cover 2/1 collapsed 0 tainted 0 declared 2 raised false meets false")
	eventCell("unkeyed served kind (a singleton)", 1,
		served([]FactKind{FactInvestment}, []FactKind{FactInvestment}),
		"kinds 1/1 cover 1/1 collapsed 0 tainted 0 declared 1 raised false meets true")
	eventCell("served kind out of vocabulary (a singleton)", 1,
		served([]FactKind{FactKind("not_a_kind")}, []FactKind{FactKind("not_a_kind")}),
		"kinds 1/1 cover 1/1 collapsed 0 tainted 0 declared 1 raised false meets true")
	independent := served([]FactKind{FactHealth, FactFlow}, []FactKind{FactHealth, FactFlow})
	eventCell("threshold boundary-1 (served cover 2, threshold 1)", 1, independent,
		"kinds 2/2 cover 2/2 collapsed 0 tainted 0 declared 2 raised false meets true")
	eventCell("threshold boundary (served cover 2, threshold 2)", 2, independent,
		"kinds 2/2 cover 2/2 collapsed 0 tainted 0 declared 2 raised false meets true")
	eventCell("threshold boundary+1 (served cover 2, threshold 3)", 3, independent,
		"kinds 2/2 cover 2/2 collapsed 0 tainted 0 declared 3 raised true meets false")
	eventCell("threshold zero", 0, independent,
		"kinds 2/2 cover 2/2 collapsed 0 tainted 0 declared 2 raised false meets true")

	// ---------------------------------------------------------------- print
	t.Logf("%-26s %-26s %-52s %-10s %s", "SURFACE", "FIELD", "CELL", "GOT", "WANT")
	for _, r := range table {
		t.Logf("%-26s %-26s %-52s %-10s %s", r.surface, r.field, r.cell, r.got, r.want)
	}
	t.Logf("DOMAIN CELLS EXECUTED: %d", len(table))
	if len(table) < 82 {
		t.Fatalf("only %d cells executed; the domain is being sampled, not enumerated", len(table))
	}
}
