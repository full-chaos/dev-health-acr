package genkitruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4632 receipt-only capture tests -- the direct counterpart of
// chaos3900_window_class_capture_test.go, because this slice follows the
// CHAOS-3900 W0 precedent exactly.
//
// RED ON origin/main by compile failure: interpretationOutput has no
// question_family/group_kind/scope_anchor_term/requested_subject_kind
// fields there, and ModelExecutionReceipt has nowhere to put them.

// TestRuntimeCapturesFamilySignalsOnReceiptOnly is THE shadow pin, and the
// most important assertion in this file.
//
// It asserts BOTH halves: the signals reach the receipt sanitized, AND the
// InterpretedQuestion is untouched. The second half is what keeps this
// slice off the wire. InterpretedQuestion is a type ALIAS to
// contractsv1.ContextFabricInterpretedQuestion, so if these fields ever
// appeared on it, every investigation result would carry them, ask-dev's
// additionalProperties:false validator would fail closed to
// acr_contract_violation, and the shared :18090 rig would break -- which
// lane-rig-refresh already proved with #336's render_shape field
// (CHAOS-4623). That is not a hypothetical; it happened.
func TestRuntimeCapturesFamilySignalsOnReceiptOnly(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.QuestionFamily = string(contextfabric.QuestionFamilyGroupedCohortStatus)
	output.GroupKind = string(contractsv1.ContextFabricSubjectTeam)
	output.ScopeAnchorTerm = "fullchaos"
	output.ScopeAnchorKind = string(contractsv1.ContextFabricSubjectTeam)
	output.RequestedSubjectKind = string(contractsv1.ContextFabricSubjectProject)

	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	interpreted, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}

	if receipt.QuestionFamily != contextfabric.QuestionFamilyGroupedCohortStatus {
		t.Errorf("receipt.QuestionFamily = %q, want grouped_cohort_status", receipt.QuestionFamily)
	}
	if receipt.GroupKind != contractsv1.ContextFabricSubjectTeam {
		t.Errorf("receipt.GroupKind = %q, want team", receipt.GroupKind)
	}
	if receipt.ScopeAnchorTerm != "fullchaos" {
		t.Errorf("receipt.ScopeAnchorTerm = %q, want fullchaos", receipt.ScopeAnchorTerm)
	}
	if receipt.ScopeAnchorKind != contractsv1.ContextFabricSubjectTeam {
		t.Errorf("receipt.ScopeAnchorKind = %q, want team", receipt.ScopeAnchorKind)
	}
	if receipt.RequestedSubjectKind != contractsv1.ContextFabricSubjectProject {
		t.Errorf("receipt.RequestedSubjectKind = %q, want project", receipt.RequestedSubjectKind)
	}

	// THE OFF-THE-WIRE HALF. The interpretation must still validate and
	// must carry no trace of any of it.
	if err := interpreted.Validate(); err != nil {
		t.Fatalf("returned InterpretedQuestion failed Validate(): %v", err)
	}
	assertInterpretationCarriesNoFamilyFields(t, interpreted)
}

// assertInterpretationCarriesNoFamilyFields serializes the interpretation
// and checks for the four field names.
//
// A JSON check rather than a field-by-field one, deliberately: a
// field-by-field check can only test for fields that EXIST, so it would go
// on passing on the very day someone adds a fifth one. The wire is what
// ask-dev validates, so the wire is what is asserted.
func assertInterpretationCarriesNoFamilyFields(t *testing.T, interpreted contextfabric.InterpretedQuestion) {
	t.Helper()
	encoded, err := marshalInterpretation(interpreted)
	if err != nil {
		t.Fatalf("marshal interpretation: %v", err)
	}
	for _, forbidden := range []string{"question_family", "group_kind", "scope_anchor_term", "scope_anchor_kind", "requested_subject_kind"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("the wire interpretation carries %q: %s\n\nThis slice is SHADOW ONLY. A field here is a v1 contract widening and a two-step deploy (CHAOS-4623) -- ask-dev validates with additionalProperties:false and fails closed, which is how #336's render_shape field broke the shared rig.", forbidden, encoded)
		}
	}
}

// TestRuntimeSanitizesOutOfVocabularyFamilySignalsRatherThanFailing is the
// F5 control-flow pin, the same one the W0 window capture holds.
//
// An out-of-vocabulary group kind or family name must NEVER reject the
// whole interpretation. interpreted.Validate() runs inside toDomain,
// strictly BEFORE this sanitize step, so a shadow capture is structurally
// incapable of becoming a new way for a sound interpretation to fail --
// and that is the property being asserted, not merely that the values are
// discarded.
func TestRuntimeSanitizesOutOfVocabularyFamilySignalsRatherThanFailing(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.QuestionFamily = "not_a_family"
	output.GroupKind = "not_a_kind"
	output.ScopeAnchorKind = "also_not_a_kind"
	output.RequestedSubjectKind = "still_not_a_kind"

	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	interpreted, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v, want success -- an out-of-vocabulary family signal must never fail interpretation", err)
	}
	if err := interpreted.Validate(); err != nil {
		t.Fatalf("returned InterpretedQuestion failed Validate(): %v", err)
	}
	if receipt.QuestionFamily != "" || receipt.GroupKind != "" || receipt.ScopeAnchorKind != "" || receipt.RequestedSubjectKind != "" {
		t.Fatalf("out-of-vocabulary values survived sanitization: family=%q group=%q anchorKind=%q requested=%q",
			receipt.QuestionFamily, receipt.GroupKind, receipt.ScopeAnchorKind, receipt.RequestedSubjectKind)
	}
	// The UNRECOGNIZED flags are what make a model inventing names
	// countable rather than invisible. Discarding silently would leave
	// "the model never emits a family" and "the model always emits a
	// wrong one" indistinguishable -- and those are opposite conclusions
	// for the gating measurement.
	if !receipt.QuestionFamilyUnrecognized {
		t.Error("receipt.QuestionFamilyUnrecognized = false, want true")
	}
	if !receipt.GroupKindUnrecognized {
		t.Error("receipt.GroupKindUnrecognized = false, want true")
	}
	// Codex round 2, P2: an earlier revision DISCARDED these two flags,
	// on the theory that an unrecognized qualifier is not a signal in its
	// own right. That reasoning is wrong for the one number this slice
	// exists to produce. The gate counts FALSE EMISSION, so a model
	// emitting requested_subject_kind="still_not_a_kind" must not be
	// recorded identically to one correctly emitting NOTHING -- otherwise
	// a model inventing kind names scores as a model behaving perfectly,
	// and the correctness number is too high by exactly the amount that
	// matters. The original test asserted the values were discarded and
	// checked flags only for family and group, so it passed throughout.
	if !receipt.ScopeAnchorKindUnrecognized {
		t.Error("receipt.ScopeAnchorKindUnrecognized = false, want true -- an invented anchor kind must be COUNTABLE, not silently identical to a correct omission")
	}
	if !receipt.RequestedSubjectKindUnrecognized {
		t.Error("receipt.RequestedSubjectKindUnrecognized = false, want true -- same reasoning")
	}
}

// TestOmissionIsDistinguishableFromAnInventedKind states the property the
// two flags above exist for, as a property rather than as two booleans:
// for EVERY new signal, "the model said nothing" and "the model said
// something invalid" must produce different receipts.
//
// This is the shape of the gating measurement's central risk. Both cases
// leave the value empty; only the flags tell them apart; and they are
// opposite conclusions about the model.
func TestOmissionIsDistinguishableFromAnInventedKind(t *testing.T) {
	t.Parallel()
	omitted := validInterpretationOutput()

	invented := validInterpretationOutput()
	invented.QuestionFamily = "invented_family"
	invented.GroupKind = "invented_kind"
	invented.ScopeAnchorKind = "invented_kind"
	invented.RequestedSubjectKind = "invented_kind"

	runtime := mustRuntime(t, &generatorStub{interpretation: omitted}, Config{})
	_, omittedReceipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	inventedRuntime := mustRuntime(t, &generatorStub{interpretation: invented}, Config{})
	_, inventedReceipt, err := inventedRuntime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}

	for _, flag := range []struct {
		name              string
		omitted, invented bool
	}{
		{"QuestionFamilyUnrecognized", omittedReceipt.QuestionFamilyUnrecognized, inventedReceipt.QuestionFamilyUnrecognized},
		{"GroupKindUnrecognized", omittedReceipt.GroupKindUnrecognized, inventedReceipt.GroupKindUnrecognized},
		{"ScopeAnchorKindUnrecognized", omittedReceipt.ScopeAnchorKindUnrecognized, inventedReceipt.ScopeAnchorKindUnrecognized},
		{"RequestedSubjectKindUnrecognized", omittedReceipt.RequestedSubjectKindUnrecognized, inventedReceipt.RequestedSubjectKindUnrecognized},
	} {
		if flag.omitted {
			t.Errorf("%s = true for an OMITTING model; omission is not an error and counting it as one would pin false emission at 100%%", flag.name)
		}
		if !flag.invented {
			t.Errorf("%s = false for a model that INVENTED a value; the two cases are then indistinguishable on the receipt, and the gate would score an inventing model as a perfect one", flag.name)
		}
	}
}

// TestRuntimeLeavesFamilyFieldsUnsetWhenModelOmitsThem pins the
// backward-compatible default and, more importantly, the NEGATIVE case
// that the gating measurement is built on.
//
// Omission must produce a wholly empty capture with NO false unrecognized
// flag. If omission were reported as "unrecognized", the false-emission
// rate the design's gate measures would be permanently pinned at 100% and
// the measurement would be worthless.
func TestRuntimeLeavesFamilyFieldsUnsetWhenModelOmitsThem(t *testing.T) {
	t.Parallel()
	runtime := mustRuntime(t, &generatorStub{interpretation: validInterpretationOutput()}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if receipt.QuestionFamily != "" || receipt.GroupKind != "" || receipt.ScopeAnchorTerm != "" ||
		receipt.ScopeAnchorKind != "" || receipt.RequestedSubjectKind != "" {
		t.Errorf("an omitting model produced a non-empty capture: %+v", receipt)
	}
	if receipt.QuestionFamilyUnrecognized || receipt.GroupKindUnrecognized || receipt.ScopeAnchorTermTruncated {
		t.Error("an omitting model raised a sanitize flag; omission is not an error and must not be counted as one")
	}
}

// TestScopeAnchorTermIsBoundedAndTruncationIsCounted pins the one free
// string in this capture.
//
// Truncated rather than rejected, for the same reason an out-of-vocabulary
// kind sanitizes to unset: this runs after Validate() has already
// succeeded, and a shadow capture must never become a new failure mode.
// Truncation is REPORTED so it is countable rather than silent.
func TestScopeAnchorTermIsBoundedAndTruncationIsCounted(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.ScopeAnchorTerm = strings.Repeat("x", contextfabric.ScopeAnchorTermMaxBytes+50)

	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v, want success", err)
	}
	if len(receipt.ScopeAnchorTerm) > contextfabric.ScopeAnchorTermMaxBytes {
		t.Errorf("ScopeAnchorTerm is %d bytes, over the %d bound", len(receipt.ScopeAnchorTerm), contextfabric.ScopeAnchorTermMaxBytes)
	}
	if !receipt.ScopeAnchorTermTruncated {
		t.Error("ScopeAnchorTermTruncated = false after truncation; a silent truncation is exactly the kind of loss that cannot be diagnosed later")
	}
}

// TestScopeAnchorTruncationNeverSplitsARune pins that truncation happens
// on a rune boundary.
//
// A byte-sliced multi-byte rune would put invalid UTF-8 into a telemetry
// field and a DURABLE receipt row, which is a data-corruption bug in a
// shadow feature -- the worst possible trade.
func TestScopeAnchorTruncationNeverSplitsARune(t *testing.T) {
	t.Parallel()
	// Each "é" is two bytes. A single leading ASCII byte shifts every
	// subsequent rune to an ODD offset, so the cut at
	// ScopeAnchorTermMaxBytes (even) lands strictly INSIDE a rune -- which
	// is the case a naive byte slice corrupts and this test exists to
	// catch. Without the leading byte every rune would start on an even
	// offset and the test would pass while proving nothing.
	raw := "a" + strings.Repeat("é", contextfabric.ScopeAnchorTermMaxBytes)
	if utf8RuneStartsAt(raw, contextfabric.ScopeAnchorTermMaxBytes) {
		t.Fatalf("fixture does not put a rune boundary mid-cut; the test would prove nothing")
	}
	term, truncated := contextfabric.SanitizeScopeAnchorTerm(raw)
	if !truncated {
		t.Fatalf("expected truncation for a %d-byte term", len(raw))
	}
	if !utf8Valid(term) {
		t.Fatalf("truncation produced invalid UTF-8: %q", term)
	}
	if len(term) > contextfabric.ScopeAnchorTermMaxBytes {
		t.Fatalf("truncated term is %d bytes, over the %d bound", len(term), contextfabric.ScopeAnchorTermMaxBytes)
	}
}

// TestExchangeTransportCapturesIdenticallyToGenkit pins the parity that
// keeps the gating measurement trustworthy.
//
// The file-exchange transport and the genkit path must produce a
// byte-identical capture from identical raw output. A transport-specific
// reimplementation would diverge silently, and the divergence would land
// inside the labelled semantic-correctness measurement, where it would be
// indistinguishable from the MODEL behaving differently -- corrupting the
// exact number this slice exists to produce.
func TestExchangeTransportCapturesIdenticallyToGenkit(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.QuestionFamily = string(contextfabric.QuestionFamilyScopedCohortStatus)
	output.GroupKind = string(contractsv1.ContextFabricSubjectTeam)
	output.ScopeAnchorTerm = "fullchaos"
	output.ScopeAnchorKind = string(contractsv1.ContextFabricSubjectTeam)
	output.RequestedSubjectKind = string(contractsv1.ContextFabricSubjectProject)

	raw, err := marshalInterpretationOutput(output)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	_, exchange, err := ParseInterpretationOutputFamily([]byte(raw), validRequest().TimeContext)
	if err != nil {
		t.Fatalf("ParseInterpretationOutputFamily() error = %v", err)
	}

	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}

	if exchange.Family != receipt.QuestionFamily ||
		exchange.GroupKind != receipt.GroupKind ||
		exchange.ScopeAnchorTerm != receipt.ScopeAnchorTerm ||
		exchange.ScopeAnchorKind != receipt.ScopeAnchorKind ||
		exchange.RequestedKind != receipt.RequestedSubjectKind ||
		exchange.FamilyUnrecognized != receipt.QuestionFamilyUnrecognized ||
		exchange.GroupKindUnrecognized != receipt.GroupKindUnrecognized ||
		exchange.ScopeAnchorTermTruncated != receipt.ScopeAnchorTermTruncated {
		t.Fatalf("transports diverged:\n exchange = %+v\n genkit receipt = family=%q group=%q anchor=%q anchorKind=%q requested=%q",
			exchange, receipt.QuestionFamily, receipt.GroupKind, receipt.ScopeAnchorTerm, receipt.ScopeAnchorKind, receipt.RequestedSubjectKind)
	}
}

func marshalInterpretation(interpreted contextfabric.InterpretedQuestion) (string, error) {
	encoded, err := json.Marshal(interpreted)
	return string(encoded), err
}

func marshalInterpretationOutput(output interpretationOutput) (string, error) {
	encoded, err := json.Marshal(output)
	return string(encoded), err
}

func utf8Valid(s string) bool { return utf8.ValidString(s) }

// utf8RuneStartsAt reports whether index is the first byte of a rune.
func utf8RuneStartsAt(s string, index int) bool {
	return index >= len(s) || s[index]&0xC0 != 0x80
}
