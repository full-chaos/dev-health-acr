package directread

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The embedded-subject gate (CHAOS-7073, design CHAOS-7036 section E.3,
// decision K2 as ruled: "remove rows and references of unseen subjects, keep
// a count, serve the labelled aggregate").
//
// The subject gate admits the ROOT subjects of a read. A fact about an
// admitted team or project can still NAME other subjects: a project's health
// fact holds one risk_breakdown row per repository its teams own, a team's
// investment table names teams, every fact carries evidence references. A
// repository-restricted caller admitted to a team through ONE granted
// repository must not receive the id, name or values of the team's other
// repositories.
//
// So before a response leaves acr-api:
//
//  1. every field and table column must be declared by the capability
//     (FactCapability.Fields); an undeclared one is removed and counted,
//     never served;
//  2. every declared subject reference, in scalars, table rows and evidence
//     references, goes through the SAME SubjectGate the roots passed (one
//     batch per read), so this gate agrees with the root gate -- including
//     the CHAOS-7080 wildcard rule -- by construction, not by a second copy
//     of the predicate;
//  3. a row naming a refused subject is removed and the table keeps a count
//     (rows_withheld); a scalar naming one is removed (fields_withheld); an
//     evidence reference naming one is removed (evidence_withheld). Ids and
//     labels of refused subjects never leave;
//  4. a reference the gate cannot resolve to a graph subject (an opaque form,
//     or a value that does not fit its declared form) is refused for a
//     repository-restricted caller and admitted for any other caller: fail
//     closed where the grant matters;
//  5. a gate that is unavailable fails the whole read (ErrEmbeddedGateUnavailable);
//     it never serves unfiltered rows.
//
// Aggregate scalars (K2) are NOT recomputed: the caller receives them
// labelled with their aggregate scope (read_facts.go).
//
// The filter itself is contextfabric.FilterEmbeddedSubjects, shared with the
// investigation engine's fact read (CHAOS-7127); this type supplies the
// direct path's decision: the root SubjectGate.

// ErrEmbeddedGateUnavailable is returned when the subject gate cannot decide
// the embedded references. The tool answers unavailable; nothing is served.
var ErrEmbeddedGateUnavailable = contextfabric.ErrEmbeddedSubjectGateUnavailable

// GatedFact is one fact after the embedded-subject gate.
type GatedFact = contextfabric.EmbeddedGatedFact

// EmbeddedReport is the gate's count summary for telemetry. It carries
// counts only, never ids.
type EmbeddedReport = contextfabric.EmbeddedSubjectReport

// EmbeddedSubjectGate filters facts through the subject gate.
type EmbeddedSubjectGate struct {
	gate *SubjectGate
}

// NewEmbeddedSubjectGate wraps the root subject gate. A nil gate makes
// every filter fail closed.
func NewEmbeddedSubjectGate(gate *SubjectGate) *EmbeddedSubjectGate {
	return &EmbeddedSubjectGate{gate: gate}
}

// Filter applies the gate to facts read for roots. capabilities are the
// registry's declarations by kind. The roots value must have been issued to
// principal by the subject gate.
func (g *EmbeddedSubjectGate) Filter(ctx context.Context, principal storage.Principal, roots AuthorizedSubjects, facts []contextfabric.CanonicalFact, capabilities map[contextfabric.FactKind]contextfabric.FactCapability) ([]GatedFact, EmbeddedReport, error) {
	if !roots.IssuedTo(principal) || roots.Len() == 0 {
		return nil, EmbeddedReport{}, ErrUngatedRead
	}
	var authorize contextfabric.EmbeddedSubjectAuthorizeFunc
	if g != nil && g.gate != nil {
		authorize = func(ctx context.Context, batch []contextfabric.SubjectRef) ([]contextfabric.SubjectRef, error) {
			granted, decision := g.gate.Authorize(ctx, principal, batch)
			if decision.Decision == DecisionUnavailable {
				return nil, fmt.Errorf("%s", decision.Reason)
			}
			return granted.Subjects(), nil
		}
	}
	restricted := ClassifyPrincipal(principal) == ClassRestricted
	return contextfabric.FilterEmbeddedSubjects(ctx, contextfabric.EmbeddedFilterOptions{Restricted: restricted, BatchSize: MaxSubjectsPerRequest}, roots.Subjects(), facts, capabilities, authorize)
}
