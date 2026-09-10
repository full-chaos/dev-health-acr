package contextfabric

import (
	"context"
	"fmt"
	"strings"
	"testing"

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

	// ---------------------------------------------------------------- print
	t.Logf("%-26s %-26s %-52s %-10s %s", "SURFACE", "FIELD", "CELL", "GOT", "WANT")
	for _, r := range table {
		t.Logf("%-26s %-26s %-52s %-10s %s", r.surface, r.field, r.cell, r.got, r.want)
	}
	t.Logf("DOMAIN CELLS EXECUTED: %d", len(table))
	if len(table) < 55 {
		t.Fatalf("only %d cells executed; the domain is being sampled, not enumerated", len(table))
	}
}
