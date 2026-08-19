package contextfabric

import (
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-3900 window vocabulary. W0 (design brief v5.2 §2.1/§5.1) shipped
// WindowClass/WindowConfidence/RelativeWindowID as SHADOW-ONLY types
// defined directly in this package, with deliberately ZERO wire-contract
// surface -- see this file's own git history for W0's full rationale
// ("nothing in this file is consulted by any serving-path decision").
//
// W1 promotes them: canonicalizeEvidenceWindow (window.go) now makes a real
// decision from a window classification, and ContextFabricInterpretedQuestion/
// ContextFabricInvestigationResult now carry window fields on the wire
// (internal/contracts/v1/context_fabric_window_types.go). The three types
// below are therefore now ALIASES to their contracts/v1 counterparts --
// the SAME "type X = contractsv1.ContextFabricX" pattern model.go already
// uses for every other wire-backed domain concept -- so every W0 call site
// (graphrank.ClassifyWindow, genkitruntime's sanitizeWindowOutput, the W0
// shadow harness) keeps compiling and behaving identically, unaware that
// the underlying type gained a wire contract.

// WindowClass is contractsv1.ContextFabricWindowClass.
type WindowClass = contractsv1.ContextFabricWindowClass

const (
	WindowClassTrendAssessment      = contractsv1.ContextFabricWindowClassTrendAssessment
	WindowClassRecentActivityLookup = contractsv1.ContextFabricWindowClassRecentActivityLookup
	WindowClassStateSnapshot        = contractsv1.ContextFabricWindowClassStateSnapshot
	WindowClassExplicitWindow       = contractsv1.ContextFabricWindowClassExplicitWindow
)

// WindowClassCount is the closed vocabulary's size.
const WindowClassCount = contractsv1.ContextFabricWindowClassCount

// WindowClassVocabulary returns the closed window-class vocabulary in a
// fixed, published order.
func WindowClassVocabulary() [WindowClassCount]WindowClass {
	return contractsv1.ContextFabricWindowClassVocabulary()
}

// ValidWindowClass reports whether value is a member of the closed
// vocabulary. The EMPTY value is deliberately NOT valid here -- callers
// that treat "unset" as legal handle that case explicitly (e.g.
// SanitizeWindowClass below).
func ValidWindowClass(value WindowClass) bool {
	return contractsv1.ValidContextFabricWindowClass(value)
}

// WindowConfidence is contractsv1.ContextFabricWindowConfidence.
type WindowConfidence = contractsv1.ContextFabricWindowConfidence

const (
	WindowConfidenceHigh = contractsv1.ContextFabricWindowConfidenceHigh
	WindowConfidenceLow  = contractsv1.ContextFabricWindowConfidenceLow
)

func ValidWindowConfidence(value WindowConfidence) bool {
	return contractsv1.ValidContextFabricWindowConfidence(value)
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

// RelativeWindowID is contractsv1.ContextFabricRelativeWindowID -- the
// closed, server-owned relative-window identifier registry (design brief
// §5.1). W1's canonicalizeEvidenceWindow (window.go) is the first caller to
// derive ABSOLUTE bounds from these outside a shadow/measurement harness,
// always from the engine's own e.now(), never from a caller-supplied
// instant.
type RelativeWindowID = contractsv1.ContextFabricRelativeWindowID

const (
	RelativeWindowTrailing30D  = contractsv1.ContextFabricRelativeWindowTrailing30D
	RelativeWindowTrailing90D  = contractsv1.ContextFabricRelativeWindowTrailing90D
	RelativeWindowTrailing365D = contractsv1.ContextFabricRelativeWindowTrailing365D
	RelativeWindowAllTime      = contractsv1.ContextFabricRelativeWindowAllTime
)

// ValidRelativeWindowID reports whether value is a member of the closed
// registry.
func ValidRelativeWindowID(value RelativeWindowID) bool {
	return contractsv1.ValidContextFabricRelativeWindowID(value)
}

// WindowProvenance is contractsv1.ContextFabricWindowProvenance (CHAOS-3900
// W1, new in this slice -- W0 had no provenance concept since nothing it
// computed ever reached a served answer).
type WindowProvenance = contractsv1.ContextFabricWindowProvenance

const (
	WindowInferredDefault        = contractsv1.ContextFabricWindowInferredDefault
	WindowQuestionStated         = contractsv1.ContextFabricWindowQuestionStated
	WindowClarificationConfirmed = contractsv1.ContextFabricWindowClarificationConfirmed
)
