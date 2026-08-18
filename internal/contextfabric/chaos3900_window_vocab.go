package contextfabric

import "strings"

// CHAOS-3900 W0 (design brief ../.remember/chaos3900-design-brief.md (relative to the dev-health/acr repo root) v5.2,
// §2.1/§5.1): the closed, shadow-only window-classification vocabulary.
//
// SHADOW ONLY: nothing in this file is consulted by any serving-path
// decision. WindowClass/WindowConfidence/RelativeWindowID exist purely so
// the interpretation layer can CAPTURE a classification for measurement
// (traced via ModelExecutionReceipt and read back by the D2(b)/W0 shadow
// harness, internal/runtime/hosted/chaos3900_w0_window_shadow_test.go) --
// no engine code branches on these values, no answer, census, or reuse
// decision changes because of them (W0 acceptance criterion: scorecard
// byte-identical). W1+ is where any of this would gain real authority, per
// the brief's own slice table (§8).
//
// These types live in package contextfabric (NOT internal/contracts/v1)
// deliberately: ContextFabricInterpretedQuestion is a published wire type
// (contracts/jsonschema, contracts/openapi), and W0 is explicitly
// measurement-only with zero wire-contract surface -- adding a field there
// would trigger the CONTRACT-FIRST rule (AGENTS.md) for a slice that ships
// no consumer of it yet. Carrying the sanitized classification on
// ModelExecutionReceipt instead (internal/contextfabric/model_runtime.go)
// stays fully internal: the receipt is persisted as an opaque JSON blob
// (internal/contextfabric/pgmodelreceipts/store.go: `json.Marshal(receipt)`
// into a single column), so additive omitempty fields here need no
// migration and are invisible to every existing reader.

// WindowClass is the closed, growable slice-1 class vocabulary (design
// brief §2.1's "class" column). The model proposes one of these four
// values (or none); the engine-side post-pass (graphrank.ClassifyWindow)
// validates the proposal against structural signals and owns the
// class-to-default table -- the model's only latitude is this one enum
// pick, never a timestamp (§2, closing paragraph).
type WindowClass string

const (
	WindowClassTrendAssessment      WindowClass = "trend_assessment"
	WindowClassRecentActivityLookup WindowClass = "recent_activity_lookup"
	WindowClassStateSnapshot        WindowClass = "state_snapshot"
	WindowClassExplicitWindow       WindowClass = "explicit_window"
)

// windowClasses is the backing array for WindowClassVocabulary/ValidWindowClass
// -- the three-part "unexported array + copy-returning accessor + closed
// membership check" pattern ContextFabricFactKindVocabulary/validFactKind
// already establishes in contracts/v1 (context_fabric_types.go).
var windowClasses = [...]WindowClass{
	WindowClassTrendAssessment,
	WindowClassRecentActivityLookup,
	WindowClassStateSnapshot,
	WindowClassExplicitWindow,
}

// WindowClassCount is the closed vocabulary's size.
const WindowClassCount = len(windowClasses)

// WindowClassVocabulary returns the closed window-class vocabulary in a
// fixed, published order. An array return (not a slice) so a caller cannot
// mutate the package's own backing storage through the returned value --
// see ContextFabricFactKindVocabulary's own doc comment for why that
// distinction matters.
func WindowClassVocabulary() [WindowClassCount]WindowClass {
	return windowClasses
}

// ValidWindowClass reports whether value is a member of the closed
// vocabulary. The EMPTY value is deliberately NOT valid here -- callers
// that treat "unset" as legal handle that case explicitly (e.g.
// SanitizeWindowClass below), mirroring how a genuinely optional closed-
// vocab field is validated elsewhere in this codebase (the field's zero
// value is a distinct "absent" state from any member).
func ValidWindowClass(value WindowClass) bool {
	for _, class := range windowClasses {
		if value == class {
			return true
		}
	}
	return false
}

// WindowConfidence is the closed "blasé detection" vocabulary (design
// brief §2.1's `window_confidence` field): "high" is the model's own
// assertion; "low" covers everything the deterministic post-pass
// downgrades to (fallback-derived class, class/shape mismatch, multi-class
// ambiguity) as well as a model-asserted low.
type WindowConfidence string

const (
	WindowConfidenceHigh WindowConfidence = "high"
	WindowConfidenceLow  WindowConfidence = "low"
)

func ValidWindowConfidence(value WindowConfidence) bool {
	return value == WindowConfidenceHigh || value == WindowConfidenceLow
}

// SanitizeWindowClass implements the F5 "sanitize-before-validate" control
// flow (design brief §2, control-flow order): the raw model string is
// trimmed and checked against the closed vocabulary HERE, before it can
// ever reach a Validate() call that would reject the whole interpretation
// for an out-of-vocab pick. An unrecognized or empty value sanitizes to
// the empty WindowClass ("unset") plus unrecognized=true whenever the raw
// value was non-empty but not a vocabulary member -- the caller (genkitruntime.
// Runtime.InterpretQuestion) turns that bool into the counted
// `window_class_unrecognized` telemetry event; it is NEVER a validation
// failure.
func SanitizeWindowClass(raw string) (class WindowClass, unrecognized bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	candidate := WindowClass(trimmed)
	if !ValidWindowClass(candidate) {
		return "", true
	}
	return candidate, false
}

// SanitizeWindowConfidence mirrors SanitizeWindowClass for the confidence
// field: an out-of-vocab or empty value sanitizes to "" (unset), never a
// validation failure. Confidence has no fallback/default derivation of its
// own -- an unset confidence is simply not traced as high or low.
func SanitizeWindowConfidence(raw string) WindowConfidence {
	trimmed := strings.TrimSpace(raw)
	candidate := WindowConfidence(trimmed)
	if !ValidWindowConfidence(candidate) {
		return ""
	}
	return candidate
}

// RelativeWindowID is the closed, server-owned relative-window identifier
// registry (design brief §5.1) -- the SAME notion the eventual reuse-key
// `w:rel:<relative_id>` namespace and the W0 class-to-default table both
// name. W0 never computes absolute bounds from these outside the shadow
// harness (no `e.now()`-derived engine step exists yet); the D2(b) rerun
// derives trailing bounds itself, once, per case (§7).
type RelativeWindowID string

const (
	RelativeWindowTrailing30D  RelativeWindowID = "trailing_30d"
	RelativeWindowTrailing90D  RelativeWindowID = "trailing_90d"
	RelativeWindowTrailing365D RelativeWindowID = "trailing_365d"
	RelativeWindowAllTime      RelativeWindowID = "all_time"
)
