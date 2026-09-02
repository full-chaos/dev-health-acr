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
	// CHAOS-4632's six shadow-capture fields are classified for EXACTLY
	// the same reason as the three CHAOS-3900 W0 fields above, and the
	// parallel is worth stating rather than assumed: each is a PER-CALL
	// outcome echoing what one model call emitted, never a
	// deployment-wide version constant.
	//
	// There is an important extra reason here, though, UPDATED as of
	// CHAOS-4634 (S4): the family now DOES gate offer composition
	// (GateOffersByFamily, chaos4579_cohort_structure_gate.go), and reuse
	// IS fenced on it -- but the fence is ReuseKey.QuestionFamilyVersion,
	// bound to QuestionFamilyTableVersion (chaos4632_question_family_
	// registry.go), a single DEPLOYMENT-WIDE constant -- never to this
	// receipt's own per-call QuestionFamily pick. QuestionFamily here
	// stays correctly excluded: it is what ONE model call happened to
	// name, not a version authority a stored answer's shape could be
	// reused against (two calls resolving the identical family from
	// identical structure signals still each carry their own
	// QuestionFamily echo). See ReuseKey.QuestionFamilyVersion's own field
	// doc comment (ports.go) for the fence itself.
	"QuestionFamily":                   {reason: "per-call model pick echoed for the shadow capture (telemetry only), not a version identity -- the deployment-wide authority that fences reuse as of CHAOS-4634 is QuestionFamilyTableVersion (ReuseKey.QuestionFamilyVersion), a package constant this receipt field never carries"},
	"QuestionFamilyUnrecognized":       {reason: "per-call sanitize-outcome boolean (telemetry only), not a version identity -- same reasoning as QuestionFamily"},
	"GroupKind":                        {reason: "per-call model-emitted structure signal captured receipt-only (shadow), not a version identity -- same reasoning as QuestionFamily"},
	"GroupKindUnrecognized":            {reason: "per-call sanitize-outcome boolean (telemetry only), not a version identity -- same reasoning as QuestionFamily"},
	"ScopeAnchorTerm":                  {reason: "per-call model-emitted retrieval pointer captured receipt-only (shadow), not a version identity -- and free text, which could never be a reuse dimension in any case"},
	"ScopeAnchorTermTruncated":         {reason: "per-call sanitize-outcome boolean (telemetry only), not a version identity -- same reasoning as ScopeAnchorTerm"},
	"ScopeAnchorKind":                  {reason: "per-call model-emitted structure signal captured receipt-only (shadow), not a version identity -- same reasoning as QuestionFamily"},
	"RequestedSubjectKind":             {reason: "per-call model-emitted structure signal captured receipt-only (shadow), not a version identity -- same reasoning as QuestionFamily"},
	"ScopeAnchorKindUnrecognized":      {reason: "per-call sanitize-outcome boolean (telemetry only), not a version identity -- same reasoning as QuestionFamily"},
	"RequestedSubjectKindUnrecognized": {reason: "per-call sanitize-outcome boolean (telemetry only), not a version identity -- same reasoning as QuestionFamily"},
	// CHAOS-4452 stage 2's frame capture is classified on exactly the
	// CHAOS-4632 precedent above, and for one shared reason worth stating
	// once: in phase 1 the frame is SHADOW. It is derived, validated,
	// persisted on this receipt and telemetered, and NOTHING DOWNSTREAM
	// READS IT -- the shipped §4.2 precedence table still decides the
	// family. A field that cannot change an answer cannot make a reused
	// answer wrong, so binding reuse to any of these would cost reuse
	// hit-rate and buy nothing. Verified by grep, not assumed: no
	// non-test site reads any of these eight fields.
	//
	// THE ONE THAT WILL CHANGE, named here so the change is not
	// rediscovered by hand -- which is the entire point of this file.
	// QuestionFrame.Version carries QuestionFrameVersion, the
	// derivation-table version, and design §13.2 says it "joins ReuseKey".
	// It does not join it YET because it fences nothing yet. When the
	// frame is promoted (phase 2: an optional wire field behind the
	// consumer pin) and the derivation replaces the precedence table, a
	// ReuseKey.QuestionFrameVersion member is OWED -- exactly the way
	// QuestionFamilyTableVersion became ReuseKey.QuestionFamilyVersion
	// once the family began fencing anything. That promotion must
	// reclassify this entry as a member, and this test is what will stop
	// it shipping without one.
	"QuestionFrame":         {reason: "per-call shadow capture of the compositional frame (receipt-only, phase 1), not a version identity -- nothing downstream reads it, so it cannot make a reused answer wrong. Its embedded Version field is a package constant (QuestionFrameVersion), and design §13.2 makes it a ReuseKey member only at promotion, when the derivation replaces the precedence table; until then there is nothing for it to fence"},
	"FrameOutcome":          {reason: "per-call validation-outcome echo (telemetry only), not a version identity -- same reasoning as QuestionFamily"},
	"FrameFailedInvariant":  {reason: "per-call validation-outcome echo (telemetry only), not a version identity -- same reasoning as QuestionFamily"},
	"FrameGoalsDropped":     {reason: "per-call sanitize-outcome count (telemetry only), not a version identity -- same reasoning as QuestionFamily"},
	"FrameTermsTruncated":   {reason: "per-call sanitize-outcome count (telemetry only), not a version identity -- same reasoning as QuestionFamily"},
	"FrameKindUnrecognized": {reason: "per-call sanitize-outcome boolean (telemetry only), not a version identity -- same reasoning as QuestionFamily"},
	// The five entries below close the same countability gap the two
	// above already closed for Goals/Terms/Kind -- found by merge-gate
	// round 3: sanitizeFrameOutput discarded the unrecognized/dropped
	// signal for Temporal, Emphasis, Dimensions, and every MemberKind/
	// GroupKind site. Same exclusion reasoning as their siblings: per-call
	// sanitize-outcome telemetry, not a version identity.
	// The obligation -> requirement layer's per-call measurement. The two
	// counts are per-call outcomes on the same footing as the sanitize
	// counts above. RequirementDerivationVersion IS a version authority --
	// it names the role/completion table the rows were derived under --
	// and it is excluded on exactly the ground QuestionFrame's embedded
	// Version is: in the shadow phase nothing downstream reads the
	// requirement rows, so a table change cannot make a reused answer
	// wrong. A ReuseKey.RequirementDerivationVersion member is OWED at
	// promotion, when the rows start driving assembly, and it is named
	// here so the debt is a written classification rather than a gap a
	// future review has to rediscover.
	"RequirementCellsDerived":      {reason: "per-call cell count (telemetry and replay only), not a version identity -- same reasoning as FrameGoalsDropped"},
	"RequirementCellsUnserved":     {reason: "per-call cell count (telemetry and replay only), not a version identity -- same reasoning as FrameGoalsDropped"},
	"RequirementDerivationVersion": {reason: "package constant (RequirementDerivationVersion) naming the role/completion table the rows were derived under. A real version authority, but the rows are shadow-only in this phase -- nothing downstream reads them, so a table change cannot make a reused answer wrong. A ReuseKey member is OWED at promotion, on the same schedule as QuestionFrame's embedded Version"},

	"FrameTemporalUnrecognized":   {reason: "per-call sanitize-outcome boolean (telemetry only), not a version identity -- same reasoning as FrameKindUnrecognized"},
	"FrameEmphasisDropped":        {reason: "per-call sanitize-outcome count (telemetry only), not a version identity -- same reasoning as FrameGoalsDropped"},
	"FrameDimensionsDropped":      {reason: "per-call sanitize-outcome count (telemetry only), not a version identity -- same reasoning as FrameGoalsDropped"},
	"FrameMemberKindUnrecognized": {reason: "per-call sanitize-outcome boolean (telemetry only), not a version identity -- same reasoning as FrameKindUnrecognized"},
	"FrameGroupKindUnrecognized":  {reason: "per-call sanitize-outcome boolean (telemetry only), not a version identity -- same reasoning as FrameKindUnrecognized"},
	// InterpretationRejectionReason names WHICH validator rule rejected one
	// interpretation. It is a per-call diagnostic, not a version authority,
	// and the exclusion is stronger than "it merely seems unimportant": a
	// receipt carrying this field describes a call that produced NO
	// interpretation and therefore no answer, so no InvestigationResult was
	// ever saved from it and there is nothing for reuse to key on. Verified
	// against the composition code rather than assumed from the field's doc
	// comment: genkitruntime.Runtime.InterpretQuestionForSample sets it only
	// on the rejecting return path, which returns a zero InterpretedQuestion
	// and an error, and pginvestigation.Store.Save is never reached for that
	// request at all.
	//
	// The version authority that DOES fence reuse when the rules themselves
	// change is the model-output schema version (ReuseKey.ModelOutputSchemaVersion,
	// already a member via InterpretationVersion) together with
	// InterpretationPromptVersion -- both deployment-wide constants this
	// per-call field never carries.
	"InterpretationRejectionReason": {reason: "per-call rejection classification (telemetry only), not a version identity -- and a receipt carrying it describes a call that produced no interpretation and therefore no saved result, so there is nothing for reuse to key on; the real authorities for a rules change are ModelOutputSchemaVersion and InterpretationPromptVersion, both already members"},
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
