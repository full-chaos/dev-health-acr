package contextfabric

// The Info rendering of a semantic snapshot, shared by every line that carries
// one: the persistence line (what a result was saved with) and the continuation
// decision line (what was carried, what was proposed).
//
// VALUES, NOT DIGESTS. Every closed value the snapshot carries is on the line
// -- the goal set, the obligations, each role slot, each requirement
// declaration -- so the decision can be rebuilt from the trace alone. The
// ONE thing never rendered is a retrieval term: terms are corpus strings, and
// the line carries only how many there were and which structural slot they
// belong to.

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// semanticStateLogGroup renders a snapshot as one slog group. An absent
// snapshot renders `present=false` and nothing else -- explicit, never an
// omitted group that reads like a line nobody wrote.
func semanticStateLogGroup(key string, state *PersistedSemanticState) slog.Attr {
	// The GROUP KEY is sanitized at its own construction site like every other
	// value on the line. It is a caller-chosen string, and the instrument reads
	// the whole group as untraceable while it is not.
	key = SanitizeLogAttr(key)
	if state == nil {
		return slog.Group(key, slog.Bool("present", false))
	}
	frame := QuestionFrame{}
	if state.Frame != nil {
		frame = *state.Frame
	}
	expression := frame.SubjectExpression
	var memberKind, groupKind, expectedKind SubjectKind
	operands := []string{}
	terms := 0
	if expression.Named != nil {
		terms += len(expression.Named.Terms)
		if expression.Named.ExpectedKind != nil {
			expectedKind = *expression.Named.ExpectedKind
		}
	}
	if expression.Discovered != nil {
		memberKind = expression.Discovered.MemberKind
	}
	if expression.Scoped != nil {
		memberKind = expression.Scoped.MemberKind
		terms += len(expression.Scoped.AnchorTerms)
	}
	if expression.Grouped != nil {
		memberKind, groupKind = expression.Grouped.MemberKind, expression.Grouped.GroupKind
	}
	if expression.Org != nil && expression.Org.MemberKind != nil {
		memberKind = *expression.Org.MemberKind
	}
	if expression.Explicit != nil {
		for i, operand := range expression.Explicit.Operands {
			token := strconv.Itoa(i) + ":" + string(operand.Kind)
			if operand.Named != nil {
				terms += len(operand.Named.Terms)
				if operand.Named.ExpectedKind != nil {
					token += ":" + string(*operand.Named.ExpectedKind)
				}
			}
			if operand.Scoped != nil {
				terms += len(operand.Scoped.AnchorTerms)
				token += ":" + string(operand.Scoped.MemberKind)
			}
			operands = append(operands, token)
		}
	}
	roles := make([]string, 0, len(state.Roles))
	for _, role := range state.Roles {
		roles = append(roles, role.SlotID+"="+string(role.Role)+":"+string(role.Subject))
	}
	requirements := make([]string, 0, len(state.Requirements))
	for _, r := range state.Requirements {
		requirements = append(requirements, semanticRequirementToken(r))
	}
	return slog.Group(key,
		slog.Bool("present", true),
		slog.String("format_version", SanitizeLogAttr(state.FormatVersion)),
		slog.String("family", SanitizeLogAttr(string(state.Family))),
		slog.String("family_source", SanitizeLogAttr(string(state.FamilySource))),
		slog.String("family_table_version", SanitizeLogAttr(state.FamilyTableVersion)),
		slog.String("group_kind", SanitizeLogAttr(string(state.GroupKind))),
		slog.String("narrowing_basis", SanitizeLogAttr(string(state.NarrowingBasis))),
		// The anchor's KIND is a closed value; its term is corpus text, so
		// only its presence is published.
		slog.String("scope_anchor_kind", SanitizeLogAttr(string(state.ScopeAnchor.Kind))),
		slog.Bool("scope_anchor_term_present", state.ScopeAnchor.Term != ""),
		slog.Bool("frame_present", state.FramePresent),
		slog.String("frame_version", SanitizeLogAttr(state.FrameVersion)),
		slog.String("subject_expression_kind", SanitizeLogAttr(string(expression.Kind))),
		slog.String("subject_member_kind", SanitizeLogAttr(string(memberKind))),
		slog.String("subject_group_kind", SanitizeLogAttr(string(groupKind))),
		slog.String("subject_expected_kind", SanitizeLogAttr(string(expectedKind))),
		slog.Any("operands", SanitizeLogStrings(operands)),
		slog.Int("retrieval_term_count", terms),
		slog.Any("goals", SanitizeLogStrings(stringsOf(frame.Goals))),
		slog.String("temporal", SanitizeLogAttr(string(frame.Temporal))),
		slog.Any("emphasis", SanitizeLogStrings(stringsOf(frame.Emphasis))),
		slog.Any("dimensions", SanitizeLogStrings(stringsOf(frame.Dimensions))),
		slog.Any("obligations", SanitizeLogStrings(stringsOf(frame.Obligations))),
		slog.Any("widened_obligations", SanitizeLogStrings(stringsOf(frame.WidenedObligations))),
		slog.String("emitted_shape", SanitizeLogAttr(string(state.Validation.EmittedShape))),
		slog.String("gate", SanitizeLogAttr((FrameGate{
			Outcome: state.Validation.GateOutcome, FailedInvariant: state.Validation.FailedInvariant,
			RefuseBasis: state.Validation.RefuseBasis, DeclaredMemberKind: state.Validation.DeclaredMemberKind,
		}).Observable())),
		slog.Any("roles", SanitizeLogStrings(roles)),
		slog.Bool("requirements_declared", state.RequirementsDeclared),
		slog.Int("requirement_count", len(state.Requirements)),
		slog.Any("requirements", SanitizeLogStrings(requirements)),
		slog.String("requirement_derivation_version", SanitizeLogAttr(state.RequirementDerivationVersion)),
		// THE REQUEST IDENTITY, PUBLISHED. It was the one component of the
		// reading that reached the store and never the trace, so a turn that
		// took the fresh path because a RECIPE moved and one that took it
		// because a CALLER changed an option emitted byte-identical lines. The
		// digest is a hash, not the text it was taken over -- no conversation
		// content, scope or option value reaches the line through it.
		slog.String("request_identity_version", SanitizeLogAttr(state.RequestIdentity.Version)),
		slog.String("request_identity_digest", SanitizeLogAttr(state.RequestIdentity.Digest)),
	)
}

// semanticRequirementToken renders one declaration as one closed token:
// obligation|role|subject|requiredness|kind|facts|dims|step|input_class|
// inputs|execution|scope|quantifier|unavailable, lists joined with "+".
func semanticRequirementToken(r SemanticRequirement) string {
	return strings.Join([]string{
		string(r.Obligation), string(r.Role), string(r.Subject), string(r.Requiredness), string(r.Kind),
		strings.Join(stringsOf(r.FactKinds), "+"), strings.Join(stringsOf(r.Dimensions), "+"),
		string(r.Step), string(r.InputClass), strings.Join(stringsOf(r.InputFactKinds), "+"),
		string(r.StepExecution), string(r.Scope), string(r.Quantifier), string(r.Unavailable),
	}, "|")
}

func stringsOf[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

// RecordSemanticStatePersistence logs one Save's semantic-state decision at
// Info. Explicit tokens everywhere a value can be absent: `absence=none` beside
// a snapshot, `state.present=false` beside an absence.
func (t SlogEngineTelemetry) RecordSemanticStatePersistence(ctx context.Context, principal storage.Principal, event SemanticStatePersistenceEvent) {
	decision := string(event.Decision)
	if !ValidSemanticStatePersistenceDecision(event.Decision) {
		decision = continuationTelemetryUnrecognised
	}
	site := string(event.Site)
	if !ValidBudgetAssertStage(event.Site) {
		site = continuationTelemetryUnrecognised
	}
	absence := "none"
	if event.Absence != "" {
		absence = string(event.Absence)
		if !ValidSemanticStateAbsence(event.Absence) {
			absence = continuationTelemetryUnrecognised
		}
	}
	// The bound a snapshot_oversized capture breached; "none" for every other
	// write, so an oversized absence can never be read without its bound.
	bound := "none"
	if event.Bound != "" {
		bound = string(event.Bound)
		if !ValidSemanticStateBound(event.Bound) {
			bound = continuationTelemetryUnrecognised
		}
	}
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"result_id", SanitizeLogAttr(event.ResultID),
		"parent_result_id", SanitizeLogAttr(event.ParentResultID),
		"site", SanitizeLogAttr(site),
		"decision", SanitizeLogAttr(decision),
		"absence", SanitizeLogAttr(absence),
		"oversized_bound", SanitizeLogAttr(bound),
		"encoded_bytes", event.EncodedBytes,
		"encoded_cap", SemanticStateMaxEncodedBytes,
		semanticStateLogGroup("state", event.State),
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric semantic state persistence", args...)
}
