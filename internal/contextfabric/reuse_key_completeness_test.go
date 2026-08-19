package contextfabric

import (
	"reflect"
	"testing"
)

// This file is the CHAOS-3862 sol-round-2 CLASS ORACLE: a durable close on
// "version authority missing from ReuseKey," a defect class that has now
// hit this codebase three times independently -- CHAOS-3833/3834 (embed
// retrieval identity / retrieval policy version), CHAOS-3862 round 1
// (interpretation / synthesis prompt version), and CHAOS-3862 round 2 (query
// version, canonical service version, model-output schema version). Each
// prior round was found by a human or a review pass reading the code by
// hand. This test removes the human from that loop: it reflects over every
// EXPORTED field of VersionSet and ModelExecutionReceipt -- the two places a
// fresh InvestigationResult's version identity is actually recorded -- and
// requires each one to be explicitly classified below, either as (a) a
// conjunctive ReuseKey member (and this test verifies that named ReuseKey
// field genuinely exists) or (b) an explicit exclusion carrying a reason a
// reviewer can judge. A field with NEITHER classification fails the test
// immediately, by name. Adding a new version-shaped field to either struct
// without updating this file is therefore a compile-clean, red test, not a
// silent gap a future review has to rediscover by hand.
//
// Classifying something as "excluded" is a real decision, not a shrug: the
// reason must say why binding this field to reuse would be wrong or
// pointless, not just that it currently seems unimportant. Reasons here
// were verified against the actual composition code (grepped for who sets
// each field), not assumed from the field's doc comment alone.

// versionAuthority is one field's classification.
type versionAuthority struct {
	// reuseKeyField is the ReuseKey field this authority binds to, or ""
	// when excluded.
	reuseKeyField string
	// reason is the exclusion rationale. Required (and only meaningful)
	// when reuseKeyField == "".
	reason string
}

// versionSetAuthorities classifies every ContextFabricVersionSet (aliased
// here as VersionSet) field -- the version identity a fresh
// InvestigationResult carries on the wire.
var versionSetAuthorities = map[string]versionAuthority{
	// ServiceVersion is the acr-api BUILD/deploy identity (e.g.
	// "acr-1.2.3"). It moves on every deploy, including deploys with zero
	// content-shape change -- binding reuse to it would defeat reuse
	// almost entirely. The authorities that actually track content shape
	// (ProjectionVersion, QueryVersion, ModelOutputSchemaVersion, the two
	// prompt versions, ModelIdentity) are the real dimensions; this is
	// just the binary's own label.
	"ServiceVersion":  {reason: "binary build/deploy identity, not a content-shape authority -- moves on every deploy including no-op ones; the specific content-shape authorities below are what should (and do) gate reuse"},
	"ContractVersion": {reuseKeyField: "ContractVersion"},
	// Backend is the single hardcoded literal "graph"
	// (contextFabricSynthesizerOptions, hosted/open.go) across this whole
	// deployment -- grepped, no other call site sets it. It carries zero
	// discriminating power today because nothing varies it.
	"Backend": {reason: "single hardcoded literal (\"graph\") in every result this deployment produces (verified: no other composition site sets it) -- carries no discriminating power today; revisit alongside BackendVersion if a second backend implementation is ever introduced"},
	// BackendVersion is never set by any production composition site
	// (grepped internal/runtime/hosted and internal/contextfabric/falkorgraph
	// -- zero hits). An always-empty field cannot meaningfully discriminate.
	"BackendVersion":    {reason: "never populated by any production composition site (grep confirms no call site sets it) -- an always-empty field cannot be a meaningful reuse discriminator; revisit if a future backend variant starts stamping it"},
	"ProjectionVersion": {reuseKeyField: "ProjectionVersion"},
	"QueryVersion":      {reuseKeyField: "QueryVersion"},
	// InterpretationVersion is confusingly named: it is NOT the
	// interpretation PROMPT version (that's the receipt's own
	// PromptVersion, covered below via InterpretationPromptVersion/
	// SynthesisPromptVersion). It is populated from
	// receipt.SchemaVersion -- the genkit MODEL-OUTPUT JSON SCHEMA
	// version, the SAME genkitruntime.Config.SchemaVersion value shared
	// by both the interpret and synthesize calls (genkitruntime has only
	// one SchemaVersion field, not a per-operation pair) -- see
	// model_runtime.go's Synthesize() composer. ModelOutputSchemaVersion
	// is that shared authority.
	"InterpretationVersion": {reuseKeyField: "ModelOutputSchemaVersion"},
	// SynthesisVersion is receipt.PromptVersion + "+" + receipt.ModelVersion
	// -- a composite of two authorities. The PromptVersion half is
	// SynthesisPromptVersion (covered). The ModelVersion half is NOT
	// separately bound: genkitruntime defaults ModelVersion to Model
	// whenever it is left unset (newWithGenerator), and grepped, no
	// composition site in modelprovider or pgmodelconfig ever sets
	// ModelVersion independently of Model -- so today ModelVersion always
	// equals the Model half of ModelIdentity (already a ReuseKey member).
	// Binding it separately would be provably redundant AS LONG AS that
	// invariant holds; if a future BYO/org config ever configures
	// ModelVersion independently, this exclusion must be revisited.
	"SynthesisVersion":        {reuseKeyField: "SynthesisPromptVersion"},
	"CanonicalServiceVersion": {reuseKeyField: "CanonicalServiceVersion"},
	// ModelIdentity is already the documented CHAOS-3782/3786 dimension:
	// ReuseKey.ModelIdentities tests chain MEMBERSHIP against it (a
	// disjunctive test, not equality against a single value), which is
	// why it is not a 1:1 "reuseKeyField" name match here but is still a
	// full member of the key.
	"ModelIdentity": {reuseKeyField: "ModelIdentities"},
}

// modelExecutionReceiptAuthorities classifies every ModelExecutionReceipt
// field. Several of these feed into VersionSet fields already classified
// above (PromptVersion -> Interpretation/SynthesisPromptVersion,
// SchemaVersion -> ModelOutputSchemaVersion, Provider/Model ->
// ModelIdentities) and are cross-referenced rather than re-justified.
var modelExecutionReceiptAuthorities = map[string]versionAuthority{
	// Operation is an enum discriminator (interpret vs synthesize), not a
	// version identity.
	"Operation": {reason: "operation-kind discriminator (interpret vs synthesize), not a version identity"},
	"Provider":  {reuseKeyField: "ModelIdentities"},
	"Model":     {reuseKeyField: "ModelIdentities"},
	// See SynthesisVersion's entry above: ModelVersion always equals
	// Model today (genkitruntime's own defaulting, and no composition
	// site configures it independently), so it rides along with
	// ModelIdentities rather than needing its own dimension.
	"ModelVersion": {reuseKeyField: "ModelIdentities"},
	// PromptVersion is interpretation- or synthesis-scoped depending on
	// Operation; both are covered, just not by ONE field name.
	"PromptVersion": {reason: "covered by BOTH InterpretationPromptVersion and SynthesisPromptVersion depending on receipt.Operation -- not a single 1:1 field mapping, but fully covered; see ReuseKey's CHAOS-3862 doc comment"},
	"SchemaVersion": {reuseKeyField: "ModelOutputSchemaVersion"},
	// EvaluatorVersion governs how a receipt's OWN grounding/classification
	// is scored for observability at write time -- it never changes what
	// content is actually served to a caller, so it has no bearing on
	// whether a STORED answer is still a valid answer to reuse.
	"EvaluatorVersion": {reason: "governs how a receipt is scored for observability at write time only -- does not change the served content's shape, so it has no bearing on reuse validity"},
	"StartedAt":        {reason: "timestamp, not a version identity"},
	"CompletedAt":      {reason: "timestamp, not a version identity"},
	"Attempts":         {reason: "retry count, not a version identity"},
	// InputDigest/OutputDigest are PER-CALL content hashes -- by
	// construction they differ across almost every distinct question, so
	// binding reuse to them would defeat reuse rather than protect it;
	// QuestionHash is the deliberate, canonicalized analogue that already
	// serves this role.
	"InputDigest":  {reason: "per-call content digest, not a deployment-wide version authority -- differs by design across almost every question; QuestionHash already serves the analogous role in ReuseKey"},
	"OutputDigest": {reason: "per-call content digest of the MODEL'S OWN raw output, not a deployment-wide version authority -- same reasoning as InputDigest"},
	"Usage":        {reason: "token accounting, not a version identity"},
	// FallbackUsed says WHETHER a fallback fired for this one call; WHICH
	// identity actually produced the result is already captured via
	// ModelIdentity (CHAOS-3786 fixed exactly this: a fallback-produced
	// receipt now carries the FALLBACK's own identity, not the primary's).
	"FallbackUsed": {reason: "per-call boolean; which identity actually produced the result is already captured via ModelIdentity/ModelIdentities (CHAOS-3786), not a separate version dimension"},
	"Outcome":      {reason: "success/failure classification, not a version identity"},
	// RequestID (CHAOS-3889) is a per-call CORRELATION id -- it names WHICH
	// InvestigationRequest produced this one receipt, the opposite of a
	// version/content-shape authority: two calls for the exact same
	// question, contract, projection, and model identity get two different
	// RequestIDs every time, so binding reuse to it would defeat reuse
	// entirely (same failure mode InputDigest/OutputDigest are excluded
	// for, just for an id instead of a content hash).
	"RequestID": {reason: "per-call correlation id, not a version/content-shape authority -- two calls with identical content-shape authorities still get distinct RequestIDs, so binding reuse to it would defeat reuse entirely"},
	// WindowClass/WindowConfidence/WindowClassUnrecognized (CHAOS-3900):
	// a per-call CLASSIFICATION OUTCOME, not a version/content-shape
	// authority -- these three ModelExecutionReceipt fields are a
	// telemetry-only echo of the model's own raw pick (W0 shadow capture,
	// chaos3900_window_vocab.go); the wire-carrying, decision-shaping copy
	// is ContextFabricInterpretedQuestion.WindowClass/WindowConfidence
	// (a DIFFERENT struct, out of this test's scope), which
	// composeEffectiveWindow (window.go) consumes as of W1. Binding THIS
	// receipt-level echo to reuse would still be pointless: it is a
	// per-call outcome, never a deployment-wide version constant. The
	// version authority that actually gates reuse for an inferred window
	// is ReuseKey.WindowInferenceVersion (CHAOS-3900 W1, ports.go) -- a
	// single deployment-current constant (contextfabric.WindowInferenceVersion),
	// not any per-call field on this struct.
	"WindowClass":             {reason: "per-call classification-outcome echo (telemetry only), not a version identity -- the decision-shaping copy lives on ContextFabricInterpretedQuestion, not this receipt; ReuseKey.WindowInferenceVersion (CHAOS-3900 W1) is the real version authority for an inferred window, and it is a deployment-wide constant, not a per-call field"},
	"WindowConfidence":        {reason: "per-call classification-outcome echo (telemetry only), not a version identity -- same reasoning as WindowClass"},
	"WindowClassUnrecognized": {reason: "per-call sanitize-outcome boolean (telemetry only), not a version identity -- same reasoning as WindowClass"},
}

// TestReuseKeyClassifiesEveryVersionSetField is the class-oracle sweep over
// VersionSet: every exported field must be classified above, and every
// "member" classification must name a field that genuinely exists on
// ReuseKey (catching a rename or removal on either side, not just an
// addition).
func TestReuseKeyClassifiesEveryVersionSetField(t *testing.T) {
	assertEveryFieldClassified(t, reflect.TypeOf(VersionSet{}), versionSetAuthorities, "VersionSet")
}

// TestReuseKeyClassifiesEveryModelExecutionReceiptField is the same sweep
// over ModelExecutionReceipt.
func TestReuseKeyClassifiesEveryModelExecutionReceiptField(t *testing.T) {
	assertEveryFieldClassified(t, reflect.TypeOf(ModelExecutionReceipt{}), modelExecutionReceiptAuthorities, "ModelExecutionReceipt")
}

func assertEveryFieldClassified(t *testing.T, structType reflect.Type, classification map[string]versionAuthority, structName string) {
	t.Helper()
	reuseKeyType := reflect.TypeOf(ReuseKey{})
	seen := map[string]bool{}
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}
		seen[field.Name] = true
		authority, classified := classification[field.Name]
		if !classified {
			t.Errorf("%s.%s has no reuse-key classification in this file -- is it a version-shaped authority a fresh InvestigationResult carries? If yes, add it to ReuseKey (see CHAOS-3833/3862 for the pattern) and classify it here as a member. If no, add an exclusion entry here with a reason (see the other entries for the bar a reason must clear).", structName, field.Name)
			continue
		}
		if authority.reuseKeyField == "" {
			if authority.reason == "" {
				t.Errorf("%s.%s is classified as excluded but carries no reason", structName, field.Name)
			}
			continue
		}
		if _, ok := reuseKeyType.FieldByName(authority.reuseKeyField); !ok {
			t.Errorf("%s.%s claims ReuseKey member %q, but ReuseKey has no such field -- the key field was renamed or removed without updating this classification", structName, field.Name, authority.reuseKeyField)
		}
	}
	// The reverse direction: an entry in the classification map for a
	// field that no longer exists on the struct is stale, not wrong, but
	// stale documentation about a defunct field actively misleads the
	// next reader -- fail loudly rather than let it rot.
	for name := range classification {
		if !seen[name] {
			t.Errorf("%s classification entry %q names a field that no longer exists on %s -- remove the stale entry", structName, name, structName)
		}
	}
}
