package contextfabric

// The internal semantic snapshot a result persists beside its public payload.
//
// WHY IT EXISTS. A window-only continuation must continue the prior turn's
// WHOLE validated reading -- its accepted family, its normalized frame, the
// gate verdict on that frame, the roles its subject expression offers, and the
// requirement declarations planning consumed. The public result carries none
// of that except the plan's family and group axis, and both result schemas
// are closed, so the reading could not survive the turn boundary. This type is
// what does: it is written in the SAME row, in the SAME insert, as the result
// it describes, and read back through the same org-scoped Get.
//
// WHAT IT IS NOT. It is not a wire field and never reaches a client. It is not
// re-derivable: a row saved without one (every row written before this
// column, and every turn that ended before interpretation) reads back
// UNAVAILABLE, and nothing reconstructs a reading from the payload, a family
// label or a newer registry. A snapshot that fails to decode, names an
// unsupported format, or exceeds the cap is unavailable in the same way --
// never a fresh reading reported as carried.
//
// HOW IT IS BOUNDED. One encoded snapshot is at most
// SemanticStateMaxEncodedBytes, and every collection it carries has its own
// explicit cardinality bound. A snapshot over either is REJECTED -- at capture
// (the row is saved with the closed absence reason instead) and at
// persistence (Save refuses it) -- never truncated, and never replaced with a
// pointer to an older result.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// SemanticStateFormatVersion is the internal format this build writes and the
// only one it reads. A stored snapshot naming any other format is unavailable.
const SemanticStateFormatVersion = "semantic-state.v1"

// SemanticStateMaxEncodedBytes is the cap on one encoded snapshot, in bytes
// of this package's canonical encoding.
const SemanticStateMaxEncodedBytes = 65536

// The collection bounds. Each is checked explicitly, so an over-long
// collection is rejected by name rather than only by the byte cap.
const (
	// SemanticStateMaxRoles bounds the role slots. A frame's topology offers
	// one slot per operand plus at most two more.
	SemanticStateMaxRoles = 64
	// SemanticStateMaxRequirements bounds the requirement declarations at the
	// public plan's own requirement-row bound, so a snapshot can never carry
	// more declarations than the plan they were given to could publish.
	SemanticStateMaxRequirements = contractsv1.ContextFabricPlanRequirementsMaxCount
	// SemanticStateMaxOperands bounds an explicit set's operands.
	SemanticStateMaxOperands = 16
	// SemanticStateMaxTerms bounds each retrieval-term list in the frame.
	SemanticStateMaxTerms = 32
	// SemanticStateMaxTermBytes bounds one retrieval term.
	SemanticStateMaxTermBytes = 1024
)

// PersistedSemanticState is the accepted semantic state of one result.
//
// Every field is REQUIRED in format v1 and every array is present (never
// null) -- "absent" and "empty" are told apart by the explicit presence
// booleans, never by a missing key.
type PersistedSemanticState struct {
	// FormatVersion is SemanticStateFormatVersion at capture.
	FormatVersion string `json:"format_version"`

	// Family, FamilySource and FamilyTableVersion are the ACCEPTED family,
	// its provenance, and the family definition table version in force when
	// it was accepted.
	Family             QuestionFamily       `json:"family"`
	FamilySource       QuestionFamilySource `json:"family_source"`
	FamilyTableVersion string               `json:"family_table_version"`
	// GroupKind is the effective group axis planning executed under, empty
	// when the reading groups nothing.
	GroupKind SubjectKind `json:"group_kind"`
	// NarrowingBasis is the plan's declared narrowing basis.
	NarrowingBasis contractsv1.ContextFabricNarrowingBasis `json:"narrowing_basis"`

	// FramePresent says whether a validated frame was accepted. Frame is
	// non-nil exactly when it is true.
	FramePresent bool           `json:"frame_present"`
	Frame        *QuestionFrame `json:"frame"`
	// FrameVersion is the frame derivation-table version in force at
	// capture. A present frame's own Version must equal it.
	FrameVersion string `json:"frame_version"`
	// Validation is the validation and gate verdict on the accepted frame,
	// with the one validation input the frame does not carry itself.
	Validation SemanticStateValidation `json:"validation"`

	// Roles are the explicit operand/role identities the frame's subject
	// expression offers, in derivation order, each with a stable slot id.
	Roles []SemanticRoleSlot `json:"roles"`

	// RequirementsDeclared says whether requirement derivation ran. It is
	// true exactly when a frame is present: no frame, no declarations. An
	// empty Requirements with RequirementsDeclared=true is a derivation that
	// declared nothing, which is a different fact from one that never ran.
	RequirementsDeclared bool                  `json:"requirements_declared"`
	Requirements         []SemanticRequirement `json:"requirements"`
	// RequirementDerivationVersion is the requirement derivation version in
	// force when the declarations were produced.
	RequirementDerivationVersion string `json:"requirement_derivation_version"`
}

// SemanticStateValidation is the frame's validation and gate verdict.
type SemanticStateValidation struct {
	// EmittedShape is the interpretation shape the frame was validated
	// against (phase A2 reads it). Recorded so the frame can be revalidated
	// under the same inputs rather than a guessed one.
	EmittedShape InvestigationShape `json:"emitted_shape"`
	// GateOutcome, FailedInvariant, RefuseBasis and DeclaredMemberKind are the
	// FrameGate fields, verbatim.
	GateOutcome        FrameGateOutcome      `json:"gate_outcome"`
	FailedInvariant    FrameInvariant        `json:"failed_invariant"`
	RefuseBasis        CohortDiscoverability `json:"refuse_basis"`
	DeclaredMemberKind SubjectKind           `json:"declared_member_kind"`
}

// SemanticRoleSlot is one role the frame's subject expression offers.
type SemanticRoleSlot struct {
	// SlotID is "<role>:<ordinal>", the ordinal counting that role's slots in
	// derivation order. Server-generated and content-free: it names a
	// position, never a subject.
	SlotID  string      `json:"slot_id"`
	Role    SubjectRole `json:"role"`
	Subject SubjectKind `json:"subject"`
}

// SemanticRequirement is one requirement declaration, as given to planning.
type SemanticRequirement struct {
	Obligation     AnswerObligation             `json:"obligation"`
	Role           SubjectRole                  `json:"role"`
	Subject        SubjectKind                  `json:"subject"`
	Requiredness   ObligationRequiredness       `json:"requiredness"`
	Kind           AnswerObligationKind         `json:"kind"`
	FactKinds      []FactKind                   `json:"fact_kinds"`
	Dimensions     []HealthDimension            `json:"dimensions"`
	Step           ComputedObligationStep       `json:"step"`
	InputClass     ComputedStepInputClass       `json:"input_class"`
	InputFactKinds []FactKind                   `json:"input_fact_kinds"`
	StepExecution  ComputedStepExecution        `json:"step_execution"`
	Scope          CompletionScope              `json:"scope"`
	Quantifier     CompletionQuantifier         `json:"quantifier"`
	Unavailable    RequirementUnavailableReason `json:"unavailable"`
}

// SemanticStateAbsence is the CLOSED reason a Save carries no snapshot.
type SemanticStateAbsence string

const (
	// SemanticStateAbsenceTurnEndedBeforeInterpretation: the turn ended
	// before interpretation produced a reading (a pre-interpretation window
	// or structure veto, or the unconfirmed explicit-window gate).
	SemanticStateAbsenceTurnEndedBeforeInterpretation SemanticStateAbsence = "turn_ended_before_interpretation"
	// SemanticStateAbsenceInterpretedTimeUnanswerable: the interpreted time
	// bound refused the turn before planning.
	SemanticStateAbsenceInterpretedTimeUnanswerable SemanticStateAbsence = "interpreted_time_unanswerable"
	// SemanticStateAbsenceContinuationRefused: the continuation refusal ended
	// the turn before planning; there is no accepted reading to record.
	SemanticStateAbsenceContinuationRefused SemanticStateAbsence = "continuation_refused"
	// SemanticStateAbsenceSnapshotOversized: a reading was accepted and its
	// snapshot exceeded a bound, so it was rejected rather than truncated.
	SemanticStateAbsenceSnapshotOversized SemanticStateAbsence = "snapshot_oversized"
	// SemanticStateAbsenceSnapshotInvalid: a reading was accepted and its
	// snapshot failed its own validation, so it was rejected.
	SemanticStateAbsenceSnapshotInvalid SemanticStateAbsence = "snapshot_invalid"
)

func semanticStateAbsences() []SemanticStateAbsence {
	return []SemanticStateAbsence{
		SemanticStateAbsenceTurnEndedBeforeInterpretation,
		SemanticStateAbsenceInterpretedTimeUnanswerable,
		SemanticStateAbsenceContinuationRefused,
		SemanticStateAbsenceSnapshotOversized,
		SemanticStateAbsenceSnapshotInvalid,
	}
}

// ValidSemanticStateAbsence reports membership. The empty value is not a
// member.
func ValidSemanticStateAbsence(value SemanticStateAbsence) bool {
	for _, member := range semanticStateAbsences() {
		if member == value {
			return true
		}
	}
	return false
}

// SemanticStateWrite is Save's explicit semantic-state argument: EXACTLY ONE of
// a snapshot or a closed absence reason. A caller cannot express "I did not
// decide" -- the zero value is refused by Validate.
type SemanticStateWrite struct {
	State   *PersistedSemanticState
	Absence SemanticStateAbsence
}

// SemanticStateOf is the write that persists a snapshot.
func SemanticStateOf(state *PersistedSemanticState) SemanticStateWrite {
	return SemanticStateWrite{State: state}
}

// SemanticStateAbsent is the write that persists no snapshot, for a reason.
func SemanticStateAbsent(reason SemanticStateAbsence) SemanticStateWrite {
	return SemanticStateWrite{Absence: reason}
}

// ErrSemanticStateRejected is the typed refusal of a semantic-state write: the
// argument named neither or both halves, an absence outside the vocabulary, or
// a snapshot that fails validation or a bound. Nothing is persisted.
var ErrSemanticStateRejected = errors.New("context fabric: semantic state rejected")

// ErrSemanticStateReplayConflict is the typed refusal of a replay whose public
// payload is identical to the stored row's and whose semantic state -- its
// presence, its format, or any value -- is not.
var ErrSemanticStateReplayConflict = errors.New("context fabric: semantic state replay conflict")

// Validate refuses a write that is not exactly one of its two halves.
func (w SemanticStateWrite) Validate() error {
	switch {
	case w.State != nil && w.Absence != "":
		return fmt.Errorf("%w: both a snapshot and an absence reason %q", ErrSemanticStateRejected, w.Absence)
	case w.State == nil && w.Absence == "":
		return fmt.Errorf("%w: neither a snapshot nor an absence reason", ErrSemanticStateRejected)
	case w.State == nil && !ValidSemanticStateAbsence(w.Absence):
		return fmt.Errorf("%w: absence reason %q is not a vocabulary member", ErrSemanticStateRejected, w.Absence)
	case w.State != nil:
		if _, err := EncodeSemanticState(w.State); err != nil {
			return err
		}
	}
	return nil
}

// EncodedColumn is the value a store writes to its semantic-state column: the
// canonical encoding, or nil for an absence. It validates first.
func (w SemanticStateWrite) EncodedColumn() ([]byte, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	if w.State == nil {
		return nil, nil
	}
	return EncodeSemanticState(w.State)
}

// SemanticStateReadStatus is the CLOSED status of a stored snapshot on read.
type SemanticStateReadStatus string

const (
	// SemanticStateReadAvailable: decoded, validated, and returned.
	SemanticStateReadAvailable SemanticStateReadStatus = "available"
	// SemanticStateReadAbsent: the row carries no snapshot (NULL).
	SemanticStateReadAbsent SemanticStateReadStatus = "absent"
	// SemanticStateReadUnsupportedVersion: the snapshot names a format this
	// build does not read.
	SemanticStateReadUnsupportedVersion SemanticStateReadStatus = "unsupported_version"
	// SemanticStateReadMalformed: the snapshot did not decode or failed
	// validation.
	SemanticStateReadMalformed SemanticStateReadStatus = "malformed"
	// SemanticStateReadOversized: the snapshot exceeds a bound.
	SemanticStateReadOversized SemanticStateReadStatus = "oversized"
)

func semanticStateReadStatuses() []SemanticStateReadStatus {
	return []SemanticStateReadStatus{
		SemanticStateReadAvailable,
		SemanticStateReadAbsent,
		SemanticStateReadUnsupportedVersion,
		SemanticStateReadMalformed,
		SemanticStateReadOversized,
	}
}

// ValidSemanticStateReadStatus reports membership.
func ValidSemanticStateReadStatus(value SemanticStateReadStatus) bool {
	for _, member := range semanticStateReadStatuses() {
		if member == value {
			return true
		}
	}
	return false
}

// errSemanticStateOversized distinguishes a bound breach from any other
// validation failure, so the two map to different closed reasons.
var errSemanticStateOversized = errors.New("semantic state exceeds a bound")

// EncodeSemanticState validates a snapshot and returns its canonical encoding.
//
// CANONICAL: the encoding is a pure function of the value (struct field order,
// no maps, arrays in the order carried), so two equal snapshots encode to the
// same bytes and replay equality is a byte comparison of two canonical
// encodings. A store whose own serialization reorders keys (PostgreSQL jsonb
// does) compares by DECODING and re-encoding, never by its own bytes.
func EncodeSemanticState(state *PersistedSemanticState) ([]byte, error) {
	if state == nil {
		return nil, fmt.Errorf("%w: nil snapshot", ErrSemanticStateRejected)
	}
	if err := validateSemanticState(*state); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %v", ErrSemanticStateRejected, err)
	}
	if len(encoded) > SemanticStateMaxEncodedBytes {
		return nil, fmt.Errorf("%w: %w: %d encoded bytes exceeds the %d-byte cap", ErrSemanticStateRejected, errSemanticStateOversized, len(encoded), SemanticStateMaxEncodedBytes)
	}
	return encoded, nil
}

// DecodeSemanticState reads a stored column value. nil or empty input is an
// absent snapshot. Every other failure returns a nil snapshot with the status
// that names it; nothing is ever repaired.
func DecodeSemanticState(raw []byte) (*PersistedSemanticState, SemanticStateReadStatus) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, SemanticStateReadAbsent
	}
	// The format is read FIRST and alone, so a future format that also
	// changed its shape reads as unsupported rather than as malformed.
	var probe struct {
		FormatVersion *string `json:"format_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.FormatVersion == nil || *probe.FormatVersion == "" {
		return nil, SemanticStateReadMalformed
	}
	if *probe.FormatVersion != SemanticStateFormatVersion {
		return nil, SemanticStateReadUnsupportedVersion
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var state PersistedSemanticState
	if err := decoder.Decode(&state); err != nil {
		return nil, SemanticStateReadMalformed
	}
	if decoder.More() {
		return nil, SemanticStateReadMalformed
	}
	// Re-encoding validates AND measures the canonical size -- the size a
	// store's own rendering happens to have (jsonb adds whitespace) is not
	// the size this package bounds.
	canonical, err := EncodeSemanticState(&state)
	if err != nil {
		if errors.Is(err, errSemanticStateOversized) {
			return nil, SemanticStateReadOversized
		}
		return nil, SemanticStateReadMalformed
	}
	// THE STORED DOCUMENT MUST BE THE CANONICAL ONE, compared as JSON values
	// (a store may reorder keys). A missing key decodes to its zero value and
	// an explicit null to an empty value, and both would otherwise read back as
	// a snapshot this codec never wrote -- MISSING IS NOT ZERO.
	if !sameJSONDocument(raw, canonical) {
		return nil, SemanticStateReadMalformed
	}
	return &state, SemanticStateReadAvailable
}

// sameJSONDocument compares two JSON documents as values.
func sameJSONDocument(a, b []byte) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// SemanticStatesEqual is replay equality: presence first, then the canonical
// encodings. Two absent snapshots are equal; absent and present never are.
func SemanticStatesEqual(a, b *PersistedSemanticState) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ae, aerr := json.Marshal(a)
	be, berr := json.Marshal(b)
	return aerr == nil && berr == nil && bytes.Equal(ae, be)
}

// cloneSemanticState returns an independent copy through the canonical
// encoding, so a carrier never aliases a store's or another carrier's memory.
func cloneSemanticState(state *PersistedSemanticState) *PersistedSemanticState {
	if state == nil {
		return nil
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil
	}
	var out PersistedSemanticState
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil
	}
	return &out
}

// validateSemanticState is the structural and cross-field check every encode
// and every decode runs. It checks shape, closed vocabularies, bounds and
// internal consistency; it does NOT run today's derivation tables -- whether
// the recorded frame still validates under today's rules is the composition
// boundary's question, answered there with its own reason.
func validateSemanticState(s PersistedSemanticState) error {
	reject := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrSemanticStateRejected, fmt.Sprintf(format, args...))
	}
	oversized := func(format string, args ...any) error {
		return fmt.Errorf("%w: %w: %s", ErrSemanticStateRejected, errSemanticStateOversized, fmt.Sprintf(format, args...))
	}
	if s.FormatVersion != SemanticStateFormatVersion {
		return reject("format_version %q is not %q", s.FormatVersion, SemanticStateFormatVersion)
	}
	if !ValidQuestionFamily(s.Family) {
		return reject("family %q is not a vocabulary member", s.Family)
	}
	if !contractsv1.ValidContextFabricQuestionFamilySource(s.FamilySource) {
		return reject("family_source %q is not a vocabulary member", s.FamilySource)
	}
	if strings.TrimSpace(s.FamilyTableVersion) == "" {
		return reject("family_table_version is empty")
	}
	if s.GroupKind != "" && !contractsv1.ValidContextFabricSubjectKind(s.GroupKind) {
		return reject("group_kind %q is not a vocabulary member", s.GroupKind)
	}
	if s.NarrowingBasis != "" && !contractsv1.ValidContextFabricNarrowingBasis(s.NarrowingBasis) {
		return reject("narrowing_basis %q is not a vocabulary member", s.NarrowingBasis)
	}
	if strings.TrimSpace(s.FrameVersion) == "" || strings.TrimSpace(s.RequirementDerivationVersion) == "" {
		return reject("frame_version and requirement_derivation_version are required")
	}
	if s.FramePresent != (s.Frame != nil) {
		return reject("frame_present=%v disagrees with the frame's presence", s.FramePresent)
	}
	if s.RequirementsDeclared != s.FramePresent {
		return reject("requirements_declared=%v disagrees with frame_present=%v -- declarations are derived from a frame or not at all", s.RequirementsDeclared, s.FramePresent)
	}
	if s.Roles == nil || s.Requirements == nil {
		return reject("roles and requirements must be arrays, never null")
	}
	if !s.RequirementsDeclared && len(s.Requirements) != 0 {
		return reject("%d requirement(s) carried with requirements_declared=false", len(s.Requirements))
	}
	if len(s.Roles) > SemanticStateMaxRoles {
		return oversized("%d roles exceeds %d", len(s.Roles), SemanticStateMaxRoles)
	}
	if len(s.Requirements) > SemanticStateMaxRequirements {
		return oversized("%d requirements exceeds %d", len(s.Requirements), SemanticStateMaxRequirements)
	}
	if err := validateSemanticValidation(s.Validation, s.FramePresent); err != nil {
		return reject("validation: %v", err)
	}
	var expectedRoles []SemanticRoleSlot
	if s.Frame != nil {
		if err := validateSemanticFrameBounds(*s.Frame); err != nil {
			if errors.Is(err, errSemanticStateOversized) {
				return fmt.Errorf("%w: %w", ErrSemanticStateRejected, err)
			}
			return reject("frame: %v", err)
		}
		if s.Frame.Version != s.FrameVersion {
			return reject("the frame's own version %q disagrees with frame_version %q", s.Frame.Version, s.FrameVersion)
		}
		if grouped, ok := s.Frame.SubjectExpression.GroupKind(); ok && s.GroupKind != grouped {
			return reject("group_kind %q disagrees with the frame's grouped axis %q", s.GroupKind, grouped)
		}
		expectedRoles = semanticRoleSlots(s.Frame.SubjectExpression)
	}
	if len(expectedRoles) != len(s.Roles) {
		return reject("%d role slot(s) carried, the frame offers %d", len(s.Roles), len(expectedRoles))
	}
	for i := range expectedRoles {
		if expectedRoles[i] != s.Roles[i] {
			return reject("role slot %d is %+v, the frame offers %+v", i, s.Roles[i], expectedRoles[i])
		}
	}
	coordinates := map[RequirementCoordinate]bool{}
	for i, requirement := range s.Requirements {
		if err := validateSemanticRequirement(requirement, s.Frame); err != nil {
			return reject("requirement %d: %v", i, err)
		}
		// One declaration per coordinate: the derivation emits each once, and
		// a repeated coordinate would plan the same requirement twice.
		coordinate := RequirementCoordinate{Obligation: requirement.Obligation, Role: requirement.Role, Subject: requirement.Subject}
		if coordinates[coordinate] {
			return reject("requirement %d repeats the coordinate %+v", i, coordinate)
		}
		coordinates[coordinate] = true
	}
	return nil
}

func validateSemanticValidation(v SemanticStateValidation, framePresent bool) error {
	if !ValidFrameGateOutcome(v.GateOutcome) {
		return fmt.Errorf("gate_outcome %q is not a vocabulary member", v.GateOutcome)
	}
	if v.EmittedShape != "" && !validInvestigationShapeMember(v.EmittedShape) {
		return fmt.Errorf("emitted_shape %q is not a vocabulary member", v.EmittedShape)
	}
	if v.FailedInvariant != "" && !ValidFrameInvariant(v.FailedInvariant) {
		return fmt.Errorf("failed_invariant %q is not a vocabulary member", v.FailedInvariant)
	}
	if v.RefuseBasis != "" && !ValidCohortDiscoverability(v.RefuseBasis) {
		return fmt.Errorf("refuse_basis %q is not a vocabulary member", v.RefuseBasis)
	}
	if v.DeclaredMemberKind != "" && !contractsv1.ValidContextFabricSubjectKind(v.DeclaredMemberKind) {
		return fmt.Errorf("declared_member_kind %q is not a vocabulary member", v.DeclaredMemberKind)
	}
	// Frame presence must agree with what the gate saw: a PASSED gate
	// certified a frame, a REJECTED or NOT-PROPOSED one had none. The
	// not-evaluated and refused-basis members are consistent with either.
	switch v.GateOutcome {
	case FrameGatePassed:
		if !framePresent {
			return fmt.Errorf("gate_outcome %q with no frame", v.GateOutcome)
		}
	case FrameGateRejectedInvalid, FrameGateNotProposed:
		if framePresent {
			return fmt.Errorf("a frame is present with gate_outcome %q", v.GateOutcome)
		}
	}
	// A present frame must be revalidatable under the inputs it was
	// validated with, and phase A2 reads the emitted shape.
	if framePresent && v.EmittedShape == "" {
		return errors.New("a frame is present with no emitted_shape to revalidate against")
	}
	return nil
}

func validInvestigationShapeMember(shape InvestigationShape) bool {
	switch shape {
	case ShapeSingleSubject, ShapeExplicitCohort, ShapeDiscoveredCohort, ShapeOpen:
		return true
	default:
		return false
	}
}

func validateSemanticFrameBounds(frame QuestionFrame) error {
	over := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", errSemanticStateOversized, fmt.Sprintf(format, args...))
	}
	if len(frame.Goals) > InvestigationGoalCount || len(frame.Emphasis) > AnswerEmphasisCount ||
		len(frame.Dimensions) > HealthDimensionCount || len(frame.Obligations) > AnswerObligationCount ||
		len(frame.WidenedObligations) > AnswerObligationCount {
		return over("a frame set exceeds its vocabulary's size")
	}
	terms := func(list []string) error {
		if len(list) > SemanticStateMaxTerms {
			return over("%d retrieval terms exceeds %d", len(list), SemanticStateMaxTerms)
		}
		for _, term := range list {
			if len(term) > SemanticStateMaxTermBytes {
				return over("a %d-byte retrieval term exceeds %d", len(term), SemanticStateMaxTermBytes)
			}
		}
		return nil
	}
	expression := frame.SubjectExpression
	if !ValidSubjectExpressionKind(expression.Kind) {
		return fmt.Errorf("subject_expression kind %q is not a vocabulary member", expression.Kind)
	}
	if expression.Named != nil {
		if err := terms(expression.Named.Terms); err != nil {
			return err
		}
	}
	if expression.Scoped != nil {
		if err := terms(expression.Scoped.AnchorTerms); err != nil {
			return err
		}
	}
	if expression.Explicit != nil {
		if len(expression.Explicit.Operands) > SemanticStateMaxOperands {
			return over("%d operands exceeds %d", len(expression.Explicit.Operands), SemanticStateMaxOperands)
		}
		for _, operand := range expression.Explicit.Operands {
			if operand.Named != nil {
				if err := terms(operand.Named.Terms); err != nil {
					return err
				}
			}
			if operand.Scoped != nil {
				if err := terms(operand.Scoped.AnchorTerms); err != nil {
					return err
				}
			}
		}
	}
	// A frame's goal and obligation sets are non-empty by construction (I15,
	// and the derivation always yields at least one obligation); an empty one
	// is not a frame this codec wrote.
	if len(frame.Goals) == 0 || len(frame.Obligations) == 0 {
		return errors.New("a frame with an empty goal or obligation set")
	}
	if duplicate := firstDuplicate(stringsOf(frame.Goals), stringsOf(frame.Emphasis), stringsOf(frame.Dimensions), stringsOf(frame.Obligations), stringsOf(frame.WidenedObligations)); duplicate != "" {
		return fmt.Errorf("a frame set carries %q twice", duplicate)
	}
	for _, goal := range frame.Goals {
		if !ValidInvestigationGoal(goal) {
			return fmt.Errorf("goal %q is not a vocabulary member", goal)
		}
	}
	if !ValidTemporalIntent(frame.Temporal) {
		return fmt.Errorf("temporal %q is not a vocabulary member", frame.Temporal)
	}
	for _, emphasis := range frame.Emphasis {
		if !ValidAnswerEmphasis(emphasis) {
			return fmt.Errorf("emphasis %q is not a vocabulary member", emphasis)
		}
	}
	for _, dimension := range frame.Dimensions {
		if !ValidHealthDimension(dimension) {
			return fmt.Errorf("dimension %q is not a vocabulary member", dimension)
		}
	}
	for _, obligation := range append(append([]AnswerObligation(nil), frame.Obligations...), frame.WidenedObligations...) {
		if !ValidAnswerObligation(obligation) {
			return fmt.Errorf("obligation %q is not a vocabulary member", obligation)
		}
	}
	return nil
}

func validateSemanticRequirement(r SemanticRequirement, frame *QuestionFrame) error {
	if r.FactKinds == nil || r.Dimensions == nil || r.InputFactKinds == nil {
		return errors.New("fact_kinds, dimensions and input_fact_kinds must be arrays, never null")
	}
	// The fields the public plan row also carries are checked by THAT row's
	// own validator, so the snapshot and the wire cannot disagree about what
	// a well-formed declaration is.
	row := planRequirement(r.derived())
	if err := row.Validate(); err != nil {
		return err
	}
	for _, dimension := range r.Dimensions {
		if !ValidHealthDimension(dimension) {
			return fmt.Errorf("dimension %q is not a vocabulary member", dimension)
		}
	}
	if r.Unavailable != "" && !contractsv1.ValidContextFabricRequirementUnavailableReason(string(r.Unavailable)) {
		return fmt.Errorf("unavailable %q is not a vocabulary member", r.Unavailable)
	}
	if frame == nil {
		return errors.New("a requirement declaration with no frame")
	}
	want, ok := frame.Requiredness(r.Obligation)
	if !ok {
		return fmt.Errorf("obligation %q is in neither the frame's derived nor its widened set", r.Obligation)
	}
	if r.Requiredness != want {
		return fmt.Errorf("requiredness %q disagrees with the frame's %q for %q", r.Requiredness, want, r.Obligation)
	}
	return nil
}

// derived converts a declaration back to the planner's row type.
func (r SemanticRequirement) derived() DerivedRequirement {
	return DerivedRequirement{
		RequirementCoordinate: RequirementCoordinate{Obligation: r.Obligation, Role: r.Role, Subject: r.Subject},
		Kind:                  r.Kind,
		FactKinds:             append([]FactKind(nil), r.FactKinds...),
		Dimensions:            append([]HealthDimension(nil), r.Dimensions...),
		Step:                  r.Step,
		InputClass:            r.InputClass,
		InputFactKinds:        append([]FactKind(nil), r.InputFactKinds...),
		StepExecution:         r.StepExecution,
		Scope:                 r.Scope,
		Quantifier:            r.Quantifier,
		Unavailable:           r.Unavailable,
	}
}

// DerivedRequirements returns the declarations as the planner's rows -- the
// carried declarations, never a re-derivation.
func (s *PersistedSemanticState) DerivedRequirements() []DerivedRequirement {
	if s == nil || !s.RequirementsDeclared {
		return nil
	}
	out := make([]DerivedRequirement, 0, len(s.Requirements))
	for _, requirement := range s.Requirements {
		out = append(out, requirement.derived())
	}
	return out
}

// semanticRoleSlots gives each of the frame's role slots a stable id.
func semanticRoleSlots(expression SubjectExpression) []SemanticRoleSlot {
	slots := frameRoleSlots(expression)
	out := make([]SemanticRoleSlot, 0, len(slots))
	ordinal := map[SubjectRole]int{}
	for _, slot := range slots {
		out = append(out, SemanticRoleSlot{
			SlotID:  string(slot.Role) + ":" + strconv.Itoa(ordinal[slot.Role]),
			Role:    slot.Role,
			Subject: slot.Subject,
		})
		ordinal[slot.Role]++
	}
	return out
}

func semanticRequirements(derived []DerivedRequirement, frame *QuestionFrame) []SemanticRequirement {
	out := make([]SemanticRequirement, 0, len(derived))
	for _, row := range derived {
		requiredness := ObligationRequiredness("")
		if frame != nil {
			requiredness, _ = frame.Requiredness(row.Obligation)
		}
		out = append(out, SemanticRequirement{
			Obligation:     row.Obligation,
			Role:           row.Role,
			Subject:        row.Subject,
			Requiredness:   requiredness,
			Kind:           row.Kind,
			FactKinds:      append([]FactKind{}, row.FactKinds...),
			Dimensions:     append([]HealthDimension{}, row.Dimensions...),
			Step:           row.Step,
			InputClass:     row.InputClass,
			InputFactKinds: append([]FactKind{}, row.InputFactKinds...),
			StepExecution:  row.StepExecution,
			Scope:          row.Scope,
			Quantifier:     row.Quantifier,
			Unavailable:    row.Unavailable,
		})
	}
	return out
}

// SemanticStateInput is everything capture reads, as the values planning
// consumed them.
type SemanticStateInput struct {
	Outcome      QuestionFamilyOutcome
	EmittedShape InvestigationShape
	// GroupKind and NarrowingBasis come from the PLAN, which is where the
	// effective values are decided.
	GroupKind      SubjectKind
	NarrowingBasis contractsv1.ContextFabricNarrowingBasis
	FamilyVersion  string
	// Requirements are the declarations given to planning -- carried ones on
	// an applied continuation, derived ones otherwise.
	Requirements []DerivedRequirement
}

// BuildSemanticState assembles a snapshot from the accepted values. It does
// not validate; captureSemanticState does.
func BuildSemanticState(in SemanticStateInput) *PersistedSemanticState {
	state := &PersistedSemanticState{
		FormatVersion:                SemanticStateFormatVersion,
		Family:                       in.Outcome.Family,
		FamilySource:                 in.Outcome.Source,
		FamilyTableVersion:           in.FamilyVersion,
		GroupKind:                    in.GroupKind,
		NarrowingBasis:               in.NarrowingBasis,
		FrameVersion:                 QuestionFrameVersion,
		Roles:                        []SemanticRoleSlot{},
		Requirements:                 []SemanticRequirement{},
		RequirementDerivationVersion: RequirementDerivationVersion,
		Validation: SemanticStateValidation{
			EmittedShape:       in.EmittedShape,
			GateOutcome:        in.Outcome.Gate.Outcome,
			FailedInvariant:    in.Outcome.Gate.FailedInvariant,
			RefuseBasis:        in.Outcome.Gate.RefuseBasis,
			DeclaredMemberKind: in.Outcome.Gate.DeclaredMemberKind,
		},
	}
	if in.Outcome.Frame != nil {
		frame := cloneFrame(*in.Outcome.Frame)
		state.FramePresent = true
		state.Frame = &frame
		state.Roles = semanticRoleSlots(frame.SubjectExpression)
		state.RequirementsDeclared = true
		state.Requirements = semanticRequirements(in.Requirements, &frame)
	}
	return state
}

// cloneFrame deep-copies a frame through its own encoding.
func cloneFrame(frame QuestionFrame) QuestionFrame {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return frame
	}
	var out QuestionFrame
	if err := json.Unmarshal(encoded, &out); err != nil {
		return frame
	}
	return out
}

// semanticStateCapture is one capture decision: the write Save receives and
// the measured size the persistence event reports.
type semanticStateCapture struct {
	Write        SemanticStateWrite
	EncodedBytes int
}

// captureSemanticState builds, validates and measures the snapshot. A snapshot
// over a bound or failing validation is REJECTED: the write becomes the closed
// absence reason that names why, and the result is saved without one.
func captureSemanticState(in SemanticStateInput) semanticStateCapture {
	state := BuildSemanticState(in)
	encoded, err := EncodeSemanticState(state)
	if err != nil {
		measured, _ := json.Marshal(state)
		if errors.Is(err, errSemanticStateOversized) {
			return semanticStateCapture{Write: SemanticStateAbsent(SemanticStateAbsenceSnapshotOversized), EncodedBytes: len(measured)}
		}
		return semanticStateCapture{Write: SemanticStateAbsent(SemanticStateAbsenceSnapshotInvalid), EncodedBytes: len(measured)}
	}
	return semanticStateCapture{Write: SemanticStateOf(state), EncodedBytes: len(encoded)}
}

// absentSemanticState is the capture for a turn that has no reading.
func absentSemanticState(reason SemanticStateAbsence) semanticStateCapture {
	return semanticStateCapture{Write: SemanticStateAbsent(reason)}
}

// firstDuplicate returns the first value repeated within any one of the lists.
func firstDuplicate(lists ...[]string) string {
	for _, list := range lists {
		seen := map[string]bool{}
		for _, value := range list {
			if seen[value] {
				return value
			}
			seen[value] = true
		}
	}
	return ""
}
