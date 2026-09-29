package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The engine's embedded-subject gate (CHAOS-7127; design CHAOS-7036 E.3, K2
// extended as ruled: remove rows and references of unseen subjects, keep a
// count, serve the labelled aggregate).
//
// The fact-root re-check (fact_root_recheck.go) admits a project to a
// repository-restricted caller when ONE repository the project reaches
// through current ownership is in the grant (E.2). The project's facts still
// span every repository its owning teams own: its health fact holds one
// risk_breakdown row per repository. Without this gate those rows -- the
// name, id and risk of repositories outside the grant -- reached synthesis
// and could be narrated into the answer.
//
// Every engine fact read (the turn's read, the group read, the period-delta
// prior read) goes through Engine.facts, so NewEngine wraps it ONCE in
// gatedFactReader. For a restricted caller, every declared subject reference
// in the returned facts is decided by the StoredResultGate's per-node rule
// (the same decision the root re-check takes) and filtered by the shared
// FilterEmbeddedSubjects, the same filter the direct read tools apply --
// including its rule 1: a field or table column the capability does not
// declare is removed and counted, because an undeclared column can carry a
// subject reference the gate cannot see (codex r1 P2). Engine-composed
// fields (period delta) are added after this read, so they are unaffected.
// Unrestricted and universal callers are unchanged: the shared predicate
// applies no repository check to them.

// Fields the gate adds to a fact it removed something from. They are
// engine-side labels, not provider fields, so no capability declares them.
const (
	// FactFieldRowsWithheldByGrantSuffix: "<table>" + this suffix carries the
	// number of rows removed from that table because they named a subject
	// outside the caller's grant.
	FactFieldRowsWithheldByGrantSuffix = "_rows_withheld_by_grant"
	// FactFieldReferencesWithheldByGrant carries the number of scalar fields
	// and evidence references removed for the same reason.
	FactFieldReferencesWithheldByGrant = "references_withheld_by_grant"
	// FactFieldAggregateScope labels a fact whose aggregate scalars (K2) were
	// computed over subjects the caller was not shown.
	FactFieldAggregateScope = "aggregate_scope"
	// AggregateScopeAllOwnedRepositories is the K2 label value.
	AggregateScopeAllOwnedRepositories = "all_owned_repositories"
)

// EngineFactGateDecision is one engine fact-read gate decision. Counts only;
// never the id or label of a withheld subject.
type EngineFactGateDecision struct {
	// Decision: "filtered" (something was withheld or an undeclared field
	// was removed), "clean" (nothing named an unseen subject and every field
	// was declared) or "unavailable" (the read failed closed).
	Decision          string
	Reason            string
	FactsChecked      int
	FactsUndeclared   int
	FactsWithheldFrom int
	Report            EmbeddedSubjectReport
}

// EngineFactGateRecorder receives every gate decision of a restricted
// caller's engine fact read. SlogEngineTelemetry implements it.
type EngineFactGateRecorder interface {
	RecordEngineFactGate(ctx context.Context, principal storage.Principal, decision EngineFactGateDecision)
}

// capabilitySource is the registry's declaration listing.
type capabilitySource interface {
	Capabilities() []FactCapability
}

// gatedFactReader is Engine.facts: the canonical fact reader with the
// embedded-subject gate applied to a restricted caller's facts.
type gatedFactReader struct {
	inner     CanonicalFactReader
	gate      *StoredResultGate
	telemetry EngineTelemetry
}

func newGatedFactReader(inner CanonicalFactReader, gate *StoredResultGate, telemetry EngineTelemetry) *gatedFactReader {
	return &gatedFactReader{inner: inner, gate: gate, telemetry: telemetry}
}

// Capabilities passes the inner reader's declarations through, so a caller
// that type-asserts Engine.facts for them keeps seeing them.
func (r *gatedFactReader) Capabilities() []FactCapability {
	if source, ok := r.inner.(capabilitySource); ok {
		return source.Capabilities()
	}
	return nil
}

func (r *gatedFactReader) ReadFacts(ctx context.Context, principal storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
	bundle, err := r.inner.ReadFacts(ctx, principal, request)
	if classifyStoredResultPrincipalScope(principal) != StoredResultScopeRestricted {
		return bundle, err
	}
	if err != nil {
		// The engine returns on the error; only the scope record survives
		// (it names no fact row).
		return CanonicalFactBundle{Scope: bundle.Scope}, err
	}
	gatedFacts, decision, gateErr := r.filter(ctx, principal, request, bundle.Facts)
	if recorder, ok := r.telemetry.(EngineFactGateRecorder); ok {
		recorder.RecordEngineFactGate(ctx, principal, decision)
	}
	if gateErr != nil {
		return CanonicalFactBundle{Scope: bundle.Scope}, fmt.Errorf("%w: %w", ErrUnavailable, gateErr)
	}
	bundle.Facts = gatedFacts
	return bundle, nil
}

func (r *gatedFactReader) filter(ctx context.Context, principal storage.Principal, request CanonicalFactRequest, facts []CanonicalFact) ([]CanonicalFact, EngineFactGateDecision, error) {
	decision := EngineFactGateDecision{Decision: "clean", FactsChecked: len(facts)}
	capabilities := map[FactKind]FactCapability{}
	for _, capability := range r.Capabilities() {
		capabilities[capability.Kind] = capability
	}
	// A kind no capability declares has no known subject reference to
	// decide; it passes unchanged and is counted, never silently skipped.
	var declared []CanonicalFact
	var declaredAt []int
	for index, fact := range facts {
		if capabilities[fact.Kind].DirectServable() {
			declared = append(declared, fact)
			declaredAt = append(declaredAt, index)
			continue
		}
		decision.FactsUndeclared++
	}
	if len(declared) == 0 {
		return facts, decision, nil
	}
	var authorize EmbeddedSubjectAuthorizeFunc
	if r.gate != nil {
		authorize = func(ctx context.Context, batch []SubjectRef) ([]SubjectRef, error) {
			return r.gate.authorizeEmbedded(ctx, principal, batch)
		}
	}
	gated, report, err := FilterEmbeddedSubjects(ctx, EmbeddedFilterOptions{Restricted: true}, requestRoots(request), declared, capabilities, authorize)
	decision.Report = report
	if err != nil {
		decision.Decision, decision.Reason = "unavailable", "authorization_failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			decision.Reason = "canceled"
		}
		return nil, decision, err
	}
	out := append([]CanonicalFact(nil), facts...)
	for position, item := range gated {
		fact := item.Fact
		if item.Withheld() {
			decision.FactsWithheldFrom++
			fact = labelWithheld(item, capabilities[fact.Kind])
		}
		out[declaredAt[position]] = fact
	}
	switch {
	case decision.FactsWithheldFrom > 0:
		decision.Decision, decision.Reason = "filtered", "unseen_subjects_withheld"
	case report.FieldsUndeclared > 0:
		// A declared kind emitted a field or column its capability does not
		// declare: removed, never served, and logged at Warn (a catalogue
		// defect, since the catalogue-truth test forbids it).
		decision.Decision, decision.Reason = "filtered", "undeclared_fields_removed"
	}
	return out, decision, nil
}

// requestRoots are the subjects the engine already admitted for this read:
// the request's subjects (committed roots after the fact-root re-check, or
// admitted groups) and the discovered cohort's members.
func requestRoots(request CanonicalFactRequest) []SubjectRef {
	roots := append([]SubjectRef(nil), request.Subjects...)
	if request.Cohort != nil {
		for _, member := range request.Cohort.Members {
			roots = append(roots, member.Subject)
		}
	}
	return roots
}

// labelWithheld keeps the count and labels the aggregate (K2): per table the
// rows removed, the scalars and evidence references removed, and, when the
// fact carries declared aggregate scalars, their scope.
func labelWithheld(item EmbeddedGatedFact, capability FactCapability) CanonicalFact {
	fact := item.Fact
	fields := make(map[string]FactValue, len(fact.Fields)+len(item.RowsWithheld)+2)
	for name, value := range fact.Fields {
		fields[name] = value
	}
	tables := make([]string, 0, len(item.RowsWithheld))
	for table, count := range item.RowsWithheld {
		if count > 0 {
			tables = append(tables, table)
		}
	}
	sort.Strings(tables)
	for _, table := range tables {
		fields[table+FactFieldRowsWithheldByGrantSuffix] = IntegerFactValue(int64(item.RowsWithheld[table]))
	}
	if references := len(item.FieldsWithheld) + item.EvidenceWithheld; references > 0 {
		fields[FactFieldReferencesWithheldByGrant] = IntegerFactValue(int64(references))
	}
	for name := range fact.Fields {
		if declaration, ok := capability.FieldDeclaration(name, fact.Subject.Kind); ok && declaration.Aggregate {
			fields[FactFieldAggregateScope] = StringFactValue(AggregateScopeAllOwnedRepositories)
			break
		}
	}
	fact.Fields = fields
	return fact
}
