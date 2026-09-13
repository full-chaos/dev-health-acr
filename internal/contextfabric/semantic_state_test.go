package contextfabric

// The semantic-snapshot codec's input domain, executed cell by cell.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// semanticFixture is a complete, valid snapshot: a grouped frame, its role
// slots, and a non-empty set of requirement declarations derived through the
// real derivation (no seed, so every row is a declared-and-unavailable row).
func semanticFixture(t testing.TB) *PersistedSemanticState {
	t.Helper()
	return semanticFixtureWithGoals(t, GoalAssessState, GoalRankOrSurvey)
}

// semanticFixtureWithGoals is semanticFixture over a chosen goal set.
func semanticFixtureWithGoals(t testing.TB, goals ...InvestigationGoal) *PersistedSemanticState {
	t.Helper()
	result := ValidateFrame(QuestionFrame{
		Goals: goals,
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: contractsv1.ContextFabricSubjectTeam, MemberKind: contractsv1.ContextFabricSubjectRepository},
		},
		Temporal: TemporalIntentCurrent,
	}, []AnswerObligation{ObligationTrendSeries}, ShapeOpen)
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture defect: frame invalid (%v)", result.Failure.Invariant)
	}
	frame := result.Frame
	derived := DeriveRequirements(frame, ObligationSeed{}, nil)
	if len(derived) == 0 {
		t.Fatalf("fixture defect: the derivation declared nothing, so the requirement cells would be vacuous")
	}
	state := BuildSemanticState(SemanticStateInput{
		Outcome: QuestionFamilyOutcome{
			Family: QuestionFamilyGroupedCohortStatus, Source: QuestionFamilySourceModel,
			Frame: &frame, Gate: DecideFrameGate(result, true),
		},
		EmittedShape:   ShapeOpen,
		GroupKind:      contractsv1.ContextFabricSubjectTeam,
		NarrowingBasis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
		FamilyVersion:  QuestionFamilyTableVersion,
		Requirements:   derived,
	})
	if _, err := EncodeSemanticState(state); err != nil {
		t.Fatalf("fixture defect: the canonical snapshot does not validate: %v", err)
	}
	return state
}

func TestSemanticState_TheCanonicalSnapshotRoundTrips(t *testing.T) {
	t.Parallel()
	state := semanticFixture(t)
	encoded, err := EncodeSemanticState(state)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, status := DecodeSemanticState(encoded)
	t.Logf("canonical: %d bytes, %d roles, %d requirements -> status=%s", len(encoded), len(state.Roles), len(state.Requirements), status)
	if status != SemanticStateReadAvailable || !SemanticStatesEqual(decoded, state) {
		t.Fatalf("status=%s equal=%v, want available and equal", status, SemanticStatesEqual(decoded, state))
	}
	// Absent is absent, however it is spelled on the way in.
	for _, raw := range [][]byte{nil, {}, []byte("   ")} {
		if got, status := DecodeSemanticState(raw); got != nil || status != SemanticStateReadAbsent {
			t.Errorf("DecodeSemanticState(%q) = %v/%s, want nil/absent", raw, got != nil, status)
		}
	}
}

// jsonMutation is one cell: a path into the canonical document and what is
// done there.
type jsonMutation struct {
	path string
	op   string
	doc  any
}

// mutateJSON walks the canonical document and yields, for every key and every
// array, the domain cells: absent, null, zero of its own type, the wrong type,
// an out-of-vocabulary string, an empty container, a duplicated element and an
// appended invented element.
func mutateJSON(t *testing.T, canonical []byte) []jsonMutation {
	t.Helper()
	var root any
	if err := json.Unmarshal(canonical, &root); err != nil {
		t.Fatalf("decode canonical: %v", err)
	}
	var cells []jsonMutation
	var walk func(node any, path string, set func(any) any, del func() any)
	clone := func() any {
		var out any
		_ = json.Unmarshal(canonical, &out)
		return out
	}
	_ = clone
	var paths []struct {
		path string
		kind string
	}
	var collect func(node any, path string)
	collect = func(node any, path string) {
		switch typed := node.(type) {
		case map[string]any:
			paths = append(paths, struct{ path, kind string }{path, "object"})
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				collect(typed[key], path+"/"+key)
			}
		case []any:
			paths = append(paths, struct{ path, kind string }{path, "array"})
			for i, child := range typed {
				collect(child, fmt.Sprintf("%s/%d", path, i))
			}
		case string:
			paths = append(paths, struct{ path, kind string }{path, "string"})
		case bool:
			paths = append(paths, struct{ path, kind string }{path, "bool"})
		default:
			paths = append(paths, struct{ path, kind string }{path, "other"})
		}
	}
	collect(root, "")
	_ = walk
	for _, p := range paths {
		if p.path == "" {
			continue
		}
		ops := []string{"absent", "null", "wrong_type"}
		switch p.kind {
		case "string":
			ops = append(ops, "zero", "out_of_vocabulary")
		case "bool":
			ops = append(ops, "flip")
		case "array":
			ops = append(ops, "empty", "duplicate_first", "append_invented", "wrong_container")
		case "object":
			ops = append(ops, "empty", "wrong_container")
		}
		for _, op := range ops {
			doc := clone()
			if applyJSONOp(doc, strings.Split(strings.TrimPrefix(p.path, "/"), "/"), op) {
				cells = append(cells, jsonMutation{path: p.path, op: op, doc: doc})
			}
		}
	}
	return cells
}

// applyJSONOp applies op at path, reporting false when the op is a no-op
// (the value already is what the op would set).
func applyJSONOp(doc any, path []string, op string) bool {
	parent := doc
	for _, segment := range path[:len(path)-1] {
		switch typed := parent.(type) {
		case map[string]any:
			parent = typed[segment]
		case []any:
			var i int
			fmt.Sscanf(segment, "%d", &i)
			parent = typed[i]
		}
	}
	last := path[len(path)-1]
	get := func() any {
		switch typed := parent.(type) {
		case map[string]any:
			return typed[last]
		case []any:
			var i int
			fmt.Sscanf(last, "%d", &i)
			return typed[i]
		}
		return nil
	}
	set := func(value any) {
		switch typed := parent.(type) {
		case map[string]any:
			typed[last] = value
		case []any:
			var i int
			fmt.Sscanf(last, "%d", &i)
			typed[i] = value
		}
	}
	current := get()
	switch op {
	case "absent":
		m, ok := parent.(map[string]any)
		if !ok {
			return false
		}
		delete(m, last)
	case "null":
		set(nil)
	case "wrong_type":
		switch current.(type) {
		case string:
			set(12345)
		default:
			set("wrong-type")
		}
	case "zero":
		if current == "" {
			return false
		}
		set("")
	case "out_of_vocabulary":
		set("invented-value")
	case "flip":
		b, _ := current.(bool)
		set(!b)
	case "empty":
		switch typed := current.(type) {
		case []any:
			if len(typed) == 0 {
				return false
			}
			set([]any{})
		case map[string]any:
			if len(typed) == 0 {
				return false
			}
			set(map[string]any{})
		}
	case "duplicate_first":
		typed := current.([]any)
		if len(typed) == 0 {
			return false
		}
		set(append(append([]any{}, typed...), typed[0]))
	case "append_invented":
		typed := current.([]any)
		set(append(append([]any{}, typed...), "invented-element"))
	case "wrong_container":
		switch current.(type) {
		case []any:
			set(map[string]any{})
		case map[string]any:
			set([]any{})
		}
	}
	return true
}

// validVariantCells are the mutations that yield ANOTHER VALID snapshot, each
// with why. Every other mutation must read back unavailable.
var validVariantCells = map[string]string{
	"/family_table_version out_of_vocabulary":           "a version string is data; admission refuses a table not in force (context_version_mismatch)",
	"/requirement_derivation_version out_of_vocabulary": "a version string is data; admission refuses a derivation not in force (context_version_mismatch)",
	"/narrowing_basis zero":                             "an empty narrowing basis is a legitimate plan value",
	"/requirements empty":                               "a derivation may declare nothing (requirements_declared stays true)",
	"/frame/widened_obligations absent":                 "a frame with no model widening is a valid frame",
	"/validation/gate_outcome zero":                     "the empty gate outcome IS not_evaluated, consistent with a present frame",
	"/scope_anchor/term out_of_vocabulary":              "an anchor term is a retrieval pointer, free text by design",
	"/request_identity/digest out_of_vocabulary":        "a digest is data: the codec does not decide whether it MATCHES, admission does -- a different digest is a valid snapshot of a different turn-one request (request_identity_changed)",
	"/request_identity/version out_of_vocabulary":       "a recipe version is data, like every other version string here: a snapshot stamped by a recipe not in force decodes and is refused at admission, never comparable-by-accident",
}

// TestSemanticState_EveryMutationOfTheStoredDocumentIsUnavailable is the read
// side's input domain: every key and array of the canonical document, times
// absent / null / zero / wrong type / out of vocabulary / empty / duplicate /
// appended element / wrong container / flipped boolean. The canonical
// document is the only one the codec writes, and a stored document that
// differs from it in any of these ways must never read back as an available
// reading.
func TestSemanticState_EveryMutationOfTheStoredDocumentIsUnavailable(t *testing.T) {
	t.Parallel()
	original := semanticFixture(t)
	canonical, err := EncodeSemanticState(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	cells := mutateJSON(t, canonical)
	if len(cells) < 100 {
		t.Fatalf("only %d cells generated -- the walk is not reaching the document", len(cells))
	}
	counts := map[SemanticStateReadStatus]int{}
	for _, cell := range cells {
		raw, err := json.Marshal(cell.doc)
		if err != nil {
			t.Fatalf("%s %s: marshal: %v", cell.path, cell.op, err)
		}
		state, status := DecodeSemanticState(raw)
		counts[status]++
		key := cell.path + " " + cell.op
		t.Logf("cell %-60s %-18s -> %s", cell.path, cell.op, status)
		if reason, variant := validVariantCells[key]; variant {
			// A DIFFERENT VALID snapshot: decodes, and differs from the
			// original, so replay equality and admission see it.
			if status != SemanticStateReadAvailable || SemanticStatesEqual(state, original) {
				t.Errorf("valid-variant cell %s -> %s equal=%v, want available and DIFFERENT (%s)", key, status, SemanticStatesEqual(state, original), reason)
			}
			continue
		}
		if status == SemanticStateReadAvailable {
			t.Errorf("cell %s decoded as AVAILABLE -- a stored document the codec did not write read back as a reading", key)
		}
		if state != nil && status != SemanticStateReadAvailable {
			t.Errorf("cell %s %s returned a snapshot beside status %s", cell.path, cell.op, status)
		}
		if cell.path == "/format_version" {
			want := SemanticStateReadMalformed
			if cell.op == "out_of_vocabulary" {
				want = SemanticStateReadUnsupportedVersion
			}
			if status != want {
				t.Errorf("format_version %s -> %s, want %s", cell.op, status, want)
			}
		}
	}
	t.Logf("%d cells: %v", len(cells), counts)
	// A future format is UNSUPPORTED, not malformed, even when its shape moved.
	if _, status := DecodeSemanticState([]byte(`{"format_version":"semantic-state.v2","anything":[1,2]}`)); status != SemanticStateReadUnsupportedVersion {
		t.Errorf("a v2 document with a new shape -> %s, want unsupported_version", status)
	}
	// A FIELD THE CODEC NEVER WROTE is malformed, whichever guard gets there
	// first: the decoder refuses unknown fields, and the canonical re-encode
	// refuses any document whose bytes the codec would not have produced.
	// Executed as a stored document, not as a struct.
	surplus := append(append([]byte(nil), canonical[:len(canonical)-1]...), []byte(`,"surplus_key":{"a":[1,2]}}`)...)
	for _, raw := range []string{`[]`, `"x"`, `null`, `{}`, `{"format_version":null}`, `{"format_version":7}`, `{"format_version":""}`, `not json`, string(canonical) + `{}`, string(surplus)} {
		if _, status := DecodeSemanticState([]byte(raw)); status != SemanticStateReadMalformed {
			t.Errorf("DecodeSemanticState(%.40q) -> %s, want malformed", raw, status)
		}
	}
}

// TestSemanticState_TheEncoderRefusesAFormatItDidNotWrite pins the ENCODE side
// of the format version. A snapshot handed to the codec carrying another
// format is refused at the boundary, rather than stored and read back later as
// an unsupported version by a turn that can no longer do anything about it.
func TestSemanticState_TheEncoderRefusesAFormatItDidNotWrite(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"", "semantic-state.v0", "semantic-state.v2", "SEMANTIC-STATE.V1", " semantic-state.v1"} {
		state := sizedSemanticState(t, 4000)
		state.FormatVersion = version
		_, err := EncodeSemanticState(state)
		t.Logf("format_version %-22q -> %v", version, err)
		if err == nil || !errors.Is(err, ErrSemanticStateRejected) {
			t.Errorf("EncodeSemanticState accepted format_version %q: %v", version, err)
		}
		// And the write argument carrying it is refused for the same reason,
		// so no store sees it.
		if err := SemanticStateOf(state).Validate(); err == nil || !errors.Is(err, ErrSemanticStateRejected) {
			t.Errorf("SemanticStateWrite.Validate accepted format_version %q: %v", version, err)
		}
	}
	control := sizedSemanticState(t, 4000)
	if _, err := EncodeSemanticState(control); err != nil {
		t.Fatalf("the control snapshot (format %q) was refused: %v", control.FormatVersion, err)
	}
}

// TestSemanticState_EachCrossFieldAgreementIsCheckedOnItsOwn breaks ONE
// agreement at a time. The stored-document domain table cannot separate these:
// flipping frame_present in the document breaks the requirements_declared
// agreement too, so either guard alone still refuses it and neither is pinned
// by that cell. Built at the struct level, each cell leaves every OTHER
// agreement intact.
func TestSemanticState_EachCrossFieldAgreementIsCheckedOnItsOwn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// break makes exactly one agreement false.
		mutate func(*PersistedSemanticState)
		want   string
	}{
		{"a frame is present while frame_present says it is not", func(s *PersistedSemanticState) {
			s.FramePresent = false
			s.RequirementsDeclared = false
			s.Requirements = []SemanticRequirement{}
			s.Roles = []SemanticRoleSlot{}
			s.Validation = SemanticStateValidation{}
		}, "frame_present=false disagrees with the frame's presence"},
		{"frame_present says a frame is there and none is", func(s *PersistedSemanticState) {
			s.Frame = nil
		}, "frame_present=true disagrees with the frame's presence"},
		{"declarations are claimed without a frame to derive them from", func(s *PersistedSemanticState) {
			s.RequirementsDeclared = false
		}, "requirements_declared=false disagrees with frame_present=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := sizedSemanticState(t, 4000)
			tc.mutate(state)
			_, err := EncodeSemanticState(state)
			t.Logf("%s -> %v", tc.name, err)
			if err == nil || !errors.Is(err, ErrSemanticStateRejected) {
				t.Fatalf("accepted a snapshot whose cross-field agreement is broken: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("rejected for the wrong reason:\n got:  %v\n want: %q", err, tc.want)
			}
		})
	}
}

// sizedSemanticState builds a valid snapshot whose canonical encoding is EXACTLY
// target bytes, by filling an explicit set's operands with retrieval terms.
// versionPaddedSemanticState builds a snapshot of EXACTLY target encoded bytes
// by padding the family table stamp -- a string no collection bound covers.
//
// The retrieval-term padding sizedSemanticState uses cannot reach the byte cap
// any more: the total term-bytes bound now stops well short of it, which is the
// whole point of that bound. The cap's own boundary cells still have to be
// driven at the boundary, so they are driven through the one field the
// collection bounds leave open, and they measure the cap rather than a
// collection.
func versionPaddedSemanticState(t testing.TB, target int) *PersistedSemanticState {
	t.Helper()
	state := sizedSemanticState(t, 4000)
	size := func(s *PersistedSemanticState) int {
		encoded, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return len(encoded)
	}
	base := state.FamilyTableVersion
	delta := target - size(state)
	if delta < 0 {
		t.Fatalf("fixture defect: the base snapshot is already %d bytes, over the %d-byte target", size(state), target)
	}
	state.FamilyTableVersion = base + strings.Repeat("v", delta)
	if got := size(state); got != target {
		t.Fatalf("fixture defect: padded to %d bytes, want %d", got, target)
	}
	return state
}

func sizedSemanticState(t testing.TB, target int) *PersistedSemanticState {
	t.Helper()
	build := func(terms [][]string) *PersistedSemanticState {
		operands := make([]SubjectOperand, 0, len(terms))
		kind := contractsv1.ContextFabricSubjectRepository
		for _, list := range terms {
			operands = append(operands, SubjectOperand{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: list, ExpectedKind: &kind}})
		}
		frame := QuestionFrame{
			Goals:             []InvestigationGoal{GoalCompare},
			SubjectExpression: SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: operands}},
			Temporal:          TemporalIntentCurrent,
			Obligations:       []AnswerObligation{ObligationState},
			Version:           QuestionFrameVersion,
		}
		return BuildSemanticState(SemanticStateInput{
			Outcome:       QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation, Source: QuestionFamilySourceModel, Frame: &frame, Gate: FrameGate{Outcome: FrameGatePassed}},
			EmittedShape:  ShapeExplicitCohort,
			FamilyVersion: QuestionFamilyTableVersion,
		})
	}
	size := func(state *PersistedSemanticState) int {
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return len(encoded)
	}
	terms := make([][]string, SemanticStateMaxOperands)
	for i := range terms {
		terms[i] = []string{}
	}
	// Fill 500-byte terms until the document is within 400 bytes of the target.
	for op := 0; op < SemanticStateMaxOperands; op++ {
		for len(terms[op]) < SemanticStateMaxTerms {
			terms[op] = append(terms[op], strings.Repeat("a", 500))
			if size(build(terms)) > target-400 {
				break
			}
		}
		if size(build(terms)) > target-400 {
			break
		}
	}
	// Then trim or pad the last term to land exactly on the target: one ASCII
	// letter is one encoded byte.
	for op := SemanticStateMaxOperands - 1; op >= 0; op-- {
		if n := len(terms[op]); n > 0 {
			last := &terms[op][n-1]
			delta := target - size(build(terms))
			if len(*last)+delta < 1 || len(*last)+delta > SemanticStateMaxTermBytes {
				t.Fatalf("fixture defect: cannot hit %d bytes by adjusting one term (delta %d)", target, delta)
			}
			*last = strings.Repeat("a", len(*last)+delta)
			break
		}
	}
	state := build(terms)
	if got := size(state); got != target {
		t.Fatalf("fixture defect: built %d bytes, want %d", got, target)
	}
	return state
}

// TestSemanticState_TheEncodedCapIsExactly65536Bytes executes the byte cap at
// 65,535 / 65,536 / 65,537 on the write side, and on the read side for a
// stored document of the same sizes.
func TestSemanticState_TheEncodedCapIsExactly65536Bytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		bytes  int
		accept bool
	}{{65535, true}, {65536, true}, {65537, false}} {
		t.Run(fmt.Sprint(tc.bytes), func(t *testing.T) {
			state := versionPaddedSemanticState(t, tc.bytes)
			encoded, err := EncodeSemanticState(state)
			raw, _ := json.Marshal(state)
			_, status := DecodeSemanticState(raw)
			write := SemanticStateOf(state).Validate()
			t.Logf("%d bytes -> encode_err=%v write_err=%v read=%s", len(raw), err, write, status)
			if tc.accept {
				if err != nil || len(encoded) != tc.bytes || write != nil || status != SemanticStateReadAvailable {
					t.Fatalf("a %d-byte snapshot must be accepted on write and read", tc.bytes)
				}
				return
			}
			if err == nil || !errors.Is(err, ErrSemanticStateRejected) || !errors.Is(err, errSemanticStateOversized) {
				t.Fatalf("encode err=%v, want the typed oversized rejection", err)
			}
			if write == nil || !errors.Is(write, ErrSemanticStateRejected) {
				t.Fatalf("write err=%v, want ErrSemanticStateRejected", write)
			}
			if status != SemanticStateReadOversized {
				t.Fatalf("read status=%s, want oversized", status)
			}
		})
	}
}

// TestSemanticState_EveryCollectionBoundIsExact executes each explicit
// cardinality bound at its limit and one past it.
func TestSemanticState_EveryCollectionBoundIsExact(t *testing.T) {
	t.Parallel()
	oversized := func(err error) bool { return errors.Is(err, errSemanticStateOversized) }
	for _, tc := range []struct {
		name    string
		atLimit func(*PersistedSemanticState)
		past    func(*PersistedSemanticState)
	}{
		{
			name: "requirements",
			// 200 DISTINCT coordinates, each a copy of a derived row with its
			// role and subject varied, so the at-limit cell is a valid
			// snapshot and only the count differs from the past-limit one.
			atLimit: func(s *PersistedSemanticState) {
				s.Requirements = distinctRequirements(s.Requirements, SemanticStateMaxRequirements)
			},
			past: func(s *PersistedSemanticState) {
				s.Requirements = distinctRequirements(s.Requirements, SemanticStateMaxRequirements+1)
			},
		},
		{
			name:    "goals beyond the vocabulary",
			atLimit: func(s *PersistedSemanticState) {},
			past: func(s *PersistedSemanticState) {
				for len(s.Frame.Goals) <= InvestigationGoalCount {
					s.Frame.Goals = append(s.Frame.Goals, GoalAssessState)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Every goal, so the derivation declares enough distinct
			// obligations to reach the requirement bound with distinct rows.
			goals := richestValidGoalSet()
			at, past := semanticFixtureWithGoals(t, goals...), semanticFixtureWithGoals(t, goals...)
			tc.atLimit(at)
			tc.past(past)
			_, atErr := EncodeSemanticState(at)
			_, pastErr := EncodeSemanticState(past)
			t.Logf("at limit (%d requirements, %d roles, %d goals) -> %v | past (%d requirements, %d roles, %d goals) -> %v",
				len(at.Requirements), len(at.Roles), len(at.Frame.Goals), atErr, len(past.Requirements), len(past.Roles), len(past.Frame.Goals), pastErr)
			if oversized(atErr) {
				t.Errorf("the at-limit cell was refused as OVERSIZED: %v", atErr)
			}
			if tc.name != "roles" && atErr != nil {
				t.Errorf("the at-limit cell must be a VALID snapshot, got %v", atErr)
			}
			if !oversized(pastErr) || !errors.Is(pastErr, ErrSemanticStateRejected) {
				t.Errorf("one past the limit -> %v, want the typed oversized rejection", pastErr)
			}
		})
	}
	// Operands, terms and term bytes, each at the limit and one past.
	explicit := func(operands, terms, termBytes int) error {
		kind := contractsv1.ContextFabricSubjectRepository
		list := make([]SubjectOperand, 0, operands)
		for i := 0; i < operands; i++ {
			words := make([]string, 0, terms)
			for j := 0; j < terms; j++ {
				words = append(words, strings.Repeat("b", termBytes))
			}
			list = append(list, SubjectOperand{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: words, ExpectedKind: &kind}})
		}
		frame := QuestionFrame{
			Goals: []InvestigationGoal{GoalCompare}, Temporal: TemporalIntentCurrent,
			SubjectExpression: SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: list}},
			Obligations:       []AnswerObligation{ObligationState}, Version: QuestionFrameVersion,
		}
		_, err := EncodeSemanticState(BuildSemanticState(SemanticStateInput{
			Outcome:      QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation, Source: QuestionFamilySourceModel, Frame: &frame, Gate: FrameGate{Outcome: FrameGatePassed}},
			EmittedShape: ShapeExplicitCohort, FamilyVersion: QuestionFamilyTableVersion,
		}))
		return err
	}
	for _, tc := range []struct {
		name                       string
		operands, terms, termBytes int
		want                       bool // oversized
	}{
		{"operands at limit", SemanticStateMaxOperands, 1, 1, false},
		{"operands past", SemanticStateMaxOperands + 1, 1, 1, true},
		{"terms at limit", 1, SemanticStateMaxTerms, 1, false},
		{"terms past", 1, SemanticStateMaxTerms + 1, 1, true},
		{"term bytes at limit", 1, 1, SemanticStateMaxTermBytes, false},
		{"term bytes past", 1, 1, SemanticStateMaxTermBytes + 1, true},
	} {
		err := explicit(tc.operands, tc.terms, tc.termBytes)
		t.Logf("%s (%d operands x %d terms x %d bytes) -> %v", tc.name, tc.operands, tc.terms, tc.termBytes, err)
		if oversized(err) != tc.want {
			t.Errorf("%s: oversized=%v, want %v (err %v)", tc.name, oversized(err), tc.want, err)
		}
		if !tc.want && err != nil {
			t.Errorf("%s: an at-limit snapshot was refused: %v", tc.name, err)
		}
	}
}

// TestSemanticState_TheWriteArgumentIsExactlyOneHalf is Save's argument domain.
func TestSemanticState_TheWriteArgumentIsExactlyOneHalf(t *testing.T) {
	t.Parallel()
	valid := semanticFixture(t)
	invalid := semanticFixture(t)
	invalid.Family = QuestionFamily("invented-family")
	cells := []struct {
		name  string
		write SemanticStateWrite
		ok    bool
	}{
		{"zero value", SemanticStateWrite{}, false},
		{"both halves", SemanticStateWrite{State: valid, Absence: SemanticStateAbsenceContinuationRefused}, false},
		{"absence out of vocabulary", SemanticStateAbsent("invented-absence"), false},
		{"absence case-variant", SemanticStateAbsent("Continuation_Refused"), false},
		{"invalid snapshot", SemanticStateOf(invalid), false},
		{"valid snapshot", SemanticStateOf(valid), true},
	}
	for _, member := range semanticStateAbsences() {
		cells = append(cells, struct {
			name  string
			write SemanticStateWrite
			ok    bool
		}{"absence " + string(member), SemanticStateAbsent(member), true})
	}
	for _, cell := range cells {
		err := cell.write.Validate()
		column, colErr := cell.write.EncodedColumn()
		t.Logf("%-40s -> validate=%v column_bytes=%d", cell.name, err, len(column))
		if (err == nil) != cell.ok || (colErr == nil) != cell.ok {
			t.Errorf("%s: validate=%v column=%v, want ok=%v", cell.name, err, colErr, cell.ok)
		}
		if err != nil && !errors.Is(err, ErrSemanticStateRejected) {
			t.Errorf("%s: refusal %v does not wrap ErrSemanticStateRejected", cell.name, err)
		}
		if cell.ok && (cell.write.State == nil) != (column == nil) {
			t.Errorf("%s: an absence must encode as a NULL column and a snapshot as bytes", cell.name)
		}
	}
}

// TestSemanticState_ReplayEqualityIncludesPresenceAndEveryComponent is the
// equality domain the stores' replay rule rests on.
func TestSemanticState_ReplayEqualityIncludesPresenceAndEveryComponent(t *testing.T) {
	t.Parallel()
	base := semanticFixture(t)
	if !SemanticStatesEqual(nil, nil) || SemanticStatesEqual(nil, base) || SemanticStatesEqual(base, nil) {
		t.Fatalf("presence is not part of equality")
	}
	if !SemanticStatesEqual(base, semanticFixture(t)) {
		t.Fatalf("two identical snapshots are unequal")
	}
	for _, change := range []struct {
		name string
		edit func(*PersistedSemanticState)
	}{
		{"family", func(s *PersistedSemanticState) { s.Family = QuestionFamilyDiscoveredCohortRanking }},
		{"group axis", func(s *PersistedSemanticState) { s.GroupKind = contractsv1.ContextFabricSubjectProject }},
		{"narrowing basis", func(s *PersistedSemanticState) { s.NarrowingBasis = "" }},
		{"format version", func(s *PersistedSemanticState) { s.FormatVersion = "semantic-state.v0" }},
		{"frame goals", func(s *PersistedSemanticState) { s.Frame.Goals = s.Frame.Goals[:1] }},
		{"gate", func(s *PersistedSemanticState) { s.Validation.GateOutcome = FrameGateNotEvaluated }},
		{"a role", func(s *PersistedSemanticState) { s.Roles[0].SlotID = "member:9" }},
		{"a requirement's requiredness", func(s *PersistedSemanticState) { s.Requirements[0].Requiredness = RequirednessAdvisory }},
		{"requirement order", func(s *PersistedSemanticState) {
			s.Requirements[0], s.Requirements[len(s.Requirements)-1] = s.Requirements[len(s.Requirements)-1], s.Requirements[0]
		}},
	} {
		other := semanticFixture(t)
		change.edit(other)
		if SemanticStatesEqual(base, other) {
			t.Errorf("a change to %s is invisible to replay equality", change.name)
		}
	}
}

// TestSemanticState_CaptureRejectsExplicitlyAndNeverTruncates: a reading whose
// snapshot breaches a bound or fails validation is saved with the closed
// absence that names why, and its measured size is kept for the trace.
func TestSemanticState_CaptureRejectsExplicitlyAndNeverTruncates(t *testing.T) {
	t.Parallel()
	// Padded through the version stamp, not through terms: the total
	// retrieval-term bound now stops term padding long before the cap, so a
	// term-padded fixture would measure that bound instead of this one.
	over := versionPaddedSemanticState(t, SemanticStateMaxEncodedBytes+1)
	in := SemanticStateInput{
		Outcome:       QuestionFamilyOutcome{Family: over.Family, Source: over.FamilySource, Frame: over.Frame, Gate: FrameGate{Outcome: FrameGatePassed}},
		EmittedShape:  over.Validation.EmittedShape,
		FamilyVersion: over.FamilyTableVersion,
	}
	capture := captureSemanticState(in)
	t.Logf("oversized capture -> absence=%s state=%v encoded_bytes=%d", capture.Write.Absence, capture.Write.State != nil, capture.EncodedBytes)
	if capture.Write.State != nil || capture.Write.Absence != SemanticStateAbsenceSnapshotOversized || capture.EncodedBytes != SemanticStateMaxEncodedBytes+1 {
		t.Errorf("capture = %+v, want absence snapshot_oversized with the measured %d bytes", capture, SemanticStateMaxEncodedBytes+1)
	}
	in.Outcome.Family = QuestionFamily("invented-family")
	invalid := captureSemanticState(in)
	if invalid.Write.State != nil || invalid.Write.Absence != SemanticStateAbsenceSnapshotInvalid {
		t.Errorf("invalid capture = %+v, want absence snapshot_invalid", invalid)
	}
	fit := versionPaddedSemanticState(t, SemanticStateMaxEncodedBytes)
	in = SemanticStateInput{
		Outcome:       QuestionFamilyOutcome{Family: fit.Family, Source: fit.FamilySource, Frame: fit.Frame, Gate: FrameGate{Outcome: FrameGatePassed}},
		EmittedShape:  fit.Validation.EmittedShape,
		FamilyVersion: fit.FamilyTableVersion,
	}
	if got := captureSemanticState(in); got.Write.State == nil || got.EncodedBytes != SemanticStateMaxEncodedBytes {
		t.Errorf("an at-cap reading was not captured whole: %+v", got)
	}
}

// distinctRequirements expands a derived set to n rows with distinct
// coordinates by varying role and subject kind over their vocabularies.
func distinctRequirements(template []SemanticRequirement, n int) []SemanticRequirement {
	out := make([]SemanticRequirement, 0, n)
	seen := map[RequirementCoordinate]bool{}
	roles := []SubjectRole{SubjectRoleSubject, SubjectRoleMember, SubjectRoleGroup, SubjectRoleOperand}
	for _, base := range template {
		for _, role := range roles {
			for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
				if len(out) == n {
					return out
				}
				coordinate := RequirementCoordinate{Obligation: base.Obligation, Role: role, Subject: kind}
				if seen[coordinate] {
					continue
				}
				seen[coordinate] = true
				row := base
				row.Role, row.Subject = role, kind
				row.FactKinds = append([]FactKind{}, base.FactKinds...)
				row.Dimensions = append([]HealthDimension{}, base.Dimensions...)
				row.InputFactKinds = append([]FactKind{}, base.InputFactKinds...)
				out = append(out, row)
			}
		}
	}
	return out
}

// richestValidGoalSet adds goals in vocabulary order, keeping each only if the
// fixture's grouped frame still validates with it.
func richestValidGoalSet() []InvestigationGoal {
	var goals []InvestigationGoal
	for _, goal := range InvestigationGoalVocabulary() {
		candidate := append(append([]InvestigationGoal{}, goals...), goal)
		result := ValidateFrame(QuestionFrame{
			Goals: candidate,
			SubjectExpression: SubjectExpression{
				Kind:    SubjectExpressionGroupedMembers,
				Grouped: &GroupedSetExpression{GroupKind: contractsv1.ContextFabricSubjectTeam, MemberKind: contractsv1.ContextFabricSubjectRepository},
			},
			Temporal: TemporalIntentCurrent,
		}, []AnswerObligation{ObligationTrendSeries}, ShapeOpen)
		if result.Outcome == FrameValidationOutcomeValid {
			goals = candidate
		}
	}
	return goals
}

// TestSemanticState_ANULCharacterIsRefusedBeforeTheStore: PostgreSQL jsonb
// cannot hold a NUL, so a snapshot carrying one is refused by the codec (the
// capture then saves the closed absence), while a string that merely SPELLS
// \u0000 is ordinary text.
func TestSemanticState_ANULCharacterIsRefusedBeforeTheStore(t *testing.T) {
	t.Parallel()
	withTerm := func(term string) *PersistedSemanticState {
		state := sizedSemanticState(t, 4000)
		state.Frame.SubjectExpression.Explicit.Operands[0].Named.Terms[0] = term
		return state
	}
	nul := withTerm("team\x00alpha")
	_, err := EncodeSemanticState(nul)
	spelled := withTerm(`team\u0000alpha`)
	_, spelledErr := EncodeSemanticState(spelled)
	capture := captureSemanticState(SemanticStateInput{
		Outcome:      QuestionFamilyOutcome{Family: nul.Family, Source: nul.FamilySource, Frame: nul.Frame, Gate: FrameGate{Outcome: FrameGatePassed}},
		EmittedShape: nul.Validation.EmittedShape, FamilyVersion: nul.FamilyTableVersion,
	})
	t.Logf("NUL term -> %v | spelled term -> %v | capture -> absence=%s", err, spelledErr, capture.Write.Absence)
	if err == nil || !errors.Is(err, ErrSemanticStateRejected) {
		t.Errorf("a NUL term was accepted: %v", err)
	}
	if spelledErr != nil {
		t.Errorf("a term spelling \\u0000 was refused: %v", spelledErr)
	}
	if capture.Write.State != nil || capture.Write.Absence != SemanticStateAbsenceSnapshotInvalid {
		t.Errorf("capture of a NUL reading = %+v, want the snapshot_invalid absence", capture.Write)
	}
}

// TestSemanticState_AnOversizedCaptureNamesItsBoundOnTheLine drives EVERY bound
// member through the production capture and the engine's one Save site, and
// reads the bound off the persistence line the shipped sink emits. A snapshot
// over a collection bound measures far under the byte cap, so without the bound
// the line would contradict itself.
func TestSemanticState_AnOversizedCaptureNamesItsBoundOnTheLine(t *testing.T) {
	t.Parallel()
	inputOf := func(s *PersistedSemanticState) SemanticStateInput {
		return SemanticStateInput{
			Outcome:       QuestionFamilyOutcome{Family: s.Family, Source: s.FamilySource, Frame: s.Frame, Gate: FrameGate{Outcome: FrameGatePassed}},
			EmittedShape:  s.Validation.EmittedShape,
			FamilyVersion: s.FamilyTableVersion,
			GroupKind:     s.GroupKind,
			Requirements:  s.DerivedRequirements(),
		}
	}
	kind := contractsv1.ContextFabricSubjectRepository
	explicit := func(t *testing.T, operands int, terms []string) SemanticStateInput {
		s := sizedSemanticState(t, 4000)
		ops := make([]SubjectOperand, 0, operands)
		for i := 0; i < operands; i++ {
			list := append([]string{}, terms...)
			list[0] = fmt.Sprintf("%s-%02d", list[0], i)
			ops = append(ops, SubjectOperand{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: list, ExpectedKind: &kind}})
		}
		s.Frame.SubjectExpression = SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: ops}}
		return inputOf(s)
	}
	manyTerms := make([]string, SemanticStateMaxTerms+1)
	for i := range manyTerms {
		manyTerms[i] = fmt.Sprintf("term-%02d", i)
	}
	drivers := map[SemanticStateBound]func(t *testing.T) SemanticStateInput{
		SemanticStateBoundEncodedBytes: func(t *testing.T) SemanticStateInput {
			// THE CAP IS THE LAST-RESORT GUARD, and after the total
			// retrieval-term bound it can no longer be reached through the
			// free text the collection bounds cover -- padding with terms now
			// breaches term_bytes_total first, which is the point of that
			// bound. It is still reachable through a field NO collection bound
			// names, and this cell drives exactly that: an over-long version
			// stamp. Production writes a constant there, so this is the guard
			// standing behind a field that cannot breach it today rather than
			// a live failure mode -- and a cell proving the guard still fires
			// is what keeps it from rotting into a member nothing can produce.
			in := inputOf(sizedSemanticState(t, 4000))
			in.FamilyVersion = strings.Repeat("v", SemanticStateMaxEncodedBytes+1)
			return in
		},
		SemanticStateBoundRequirements: func(t *testing.T) SemanticStateInput {
			goals := richestValidGoalSet()
			s := semanticFixtureWithGoals(t, goals...)
			in := inputOf(s)
			s.Requirements = distinctRequirements(s.Requirements, SemanticStateMaxRequirements+1)
			in.Requirements = s.DerivedRequirements()
			return in
		},
		SemanticStateBoundOperands: func(t *testing.T) SemanticStateInput { return explicit(t, SemanticStateMaxOperands+1, []string{"a"}) },
		SemanticStateBoundTerms:    func(t *testing.T) SemanticStateInput { return explicit(t, 2, manyTerms) },
		SemanticStateBoundTermBytes: func(t *testing.T) SemanticStateInput {
			return explicit(t, 2, []string{strings.Repeat("x", SemanticStateMaxTermBytes+1)})
		},
		SemanticStateBoundTermBytesTotal: func(t *testing.T) SemanticStateInput {
			// Every term inside the per-term bound, and one byte past the
			// total across them: the cell the per-collection bounds all pass
			// and the cap used to catch far too late.
			// Half the per-term bound each, so the helper's own "-00" suffix
			// on the first term cannot breach term_bytes and mask this cell,
			// and one term more than the total allows.
			each := SemanticStateMaxTermBytes / 2
			count := SemanticStateMaxTermBytesTotal/each + 1
			list := make([]string, 0, count)
			for i := 0; i < count; i++ {
				list = append(list, strings.Repeat("x", each))
			}
			return explicit(t, 1, list)
		},
		SemanticStateBoundRequirementFactKinds: func(t *testing.T) SemanticStateInput {
			s := semanticFixture(t)
			in := inputOf(s)
			rows := s.DerivedRequirements()
			for len(rows[0].FactKinds) <= contractsv1.ContextFabricFactKindCount {
				rows[0].FactKinds = append(rows[0].FactKinds, contractsv1.ContextFabricFactHealth)
			}
			in.Requirements = rows
			return in
		},
		SemanticStateBoundRequirementDimensions: func(t *testing.T) SemanticStateInput {
			s := semanticFixture(t)
			in := inputOf(s)
			rows := s.DerivedRequirements()
			for len(rows[0].Dimensions) <= HealthDimensionCount {
				rows[0].Dimensions = append(rows[0].Dimensions, HealthDimensionDeliveryFlow)
			}
			in.Requirements = rows
			return in
		},
		SemanticStateBoundFrameSet: func(t *testing.T) SemanticStateInput {
			in := inputOf(sizedSemanticState(t, 4000))
			frame := cloneFrame(*in.Outcome.Frame)
			for len(frame.Goals) <= InvestigationGoalCount {
				frame.Goals = append(frame.Goals, GoalCompare)
			}
			in.Outcome.Frame = &frame
			return in
		},
	}
	for _, bound := range semanticStateBounds() {
		t.Run(string(bound), func(t *testing.T) {
			t.Parallel()
			driver, ok := drivers[bound]
			if !ok {
				t.Fatalf("bound %q has no driver -- every member needs one", bound)
			}
			capture := captureSemanticState(driver(t))
			var buf strings.Builder
			engine := mustReuseTestEngine(t, EngineDependencies{
				Results:   &staticResultStore{results: map[string]InvestigationResult{}},
				Telemetry: SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))},
			})
			result := validInvestigationResult()
			if err := engine.saveResult(context.Background(), acceptancePrincipal(), BudgetAssertDecisive, result, nil, nil, TimeAxisKeyFor(TimeContext{Axis: TemporalCurrent}), 0, "", capture); err != nil {
				t.Fatalf("saveResult: %v", err)
			}
			var line map[string]any
			for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
				var candidate map[string]any
				if json.Unmarshal([]byte(raw), &candidate) == nil && candidate["msg"] == "context fabric semantic state persistence" {
					line = candidate
				}
			}
			if line == nil {
				t.Fatalf("no persistence line:\n%s", buf.String())
			}
			t.Logf("%s -> absence=%v oversized_bound=%v encoded_bytes=%v encoded_cap=%v", bound, line["absence"], line["oversized_bound"], line["encoded_bytes"], line["encoded_cap"])
			if line["absence"] != string(SemanticStateAbsenceSnapshotOversized) || line["oversized_bound"] != string(bound) {
				t.Errorf("line absence=%v oversized_bound=%v, want snapshot_oversized naming %q", line["absence"], line["oversized_bound"], bound)
			}
		})
	}
	t.Run("a snapshot within every bound names none", func(t *testing.T) {
		t.Parallel()
		capture := captureSemanticState(inputOf(sizedSemanticState(t, 4000)))
		if capture.Write.State == nil || capture.Bound != "" {
			t.Errorf("an in-bounds capture = %+v, want a snapshot and no bound", capture)
		}
	})
}

// TestSemanticState_TheScopeAnchorTermIsBoundedLikeEveryTerm: the anchor term
// is a retrieval term stored beside the frame, bounded at the same per-term
// limit and reported under the same bound.
func TestSemanticState_TheScopeAnchorTermIsBoundedLikeEveryTerm(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		bytes  int
		accept bool
	}{{SemanticStateMaxTermBytes - 1, true}, {SemanticStateMaxTermBytes, true}, {SemanticStateMaxTermBytes + 1, false}} {
		state := sizedSemanticState(t, 4000)
		state.ScopeAnchor = SemanticScopeAnchor{Kind: SubjectTeam, Term: strings.Repeat("a", tc.bytes)}
		_, err := EncodeSemanticState(state)
		t.Logf("scope_anchor.term %d bytes -> err=%v bound=%q", tc.bytes, err, breachedSemanticStateBound(err))
		if tc.accept != (err == nil) {
			t.Errorf("scope_anchor.term %d bytes accepted=%v, want %v", tc.bytes, err == nil, tc.accept)
		}
		if !tc.accept && breachedSemanticStateBound(err) != SemanticStateBoundTermBytes {
			t.Errorf("scope_anchor.term %d bytes bound=%q, want %q", tc.bytes, breachedSemanticStateBound(err), SemanticStateBoundTermBytes)
		}
	}
}
