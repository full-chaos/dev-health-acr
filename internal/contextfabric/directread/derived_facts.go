package directread

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// ReachedFrom names a requested subject a derived fact was reached from by
// scope expansion, and the basis of that path.
type ReachedFrom struct {
	Kind        string `json:"kind"`
	CanonicalID string `json:"canonical_id"`
	Basis       string `json:"basis"`
}

// derivedExpansion is one (kind, origin kind) scope decision.
type derivedExpansion struct {
	policy     string
	targetKind contextfabric.SubjectKind
	outcome    contextfabric.FactScopeExpansionOutcome
}

// derivedIndex holds, per fact kind, the targets the resolver derived from
// gate-admitted roots. A derivation whose root the gate did not admit is
// never indexed, so a fact on its target is never served.
type derivedIndex struct {
	targets      map[contextfabric.FactKind]map[string][]ReachedFrom
	byRoot       map[contextfabric.FactKind]map[string][]contextfabric.SubjectRef
	expansions   map[contextfabric.FactKind]map[contextfabric.SubjectKind]derivedExpansion
	unauthorized int
}

func newDerivedIndex(scope *contextfabric.FactReadScope, admitted []contextfabric.SubjectRef) derivedIndex {
	index := derivedIndex{
		targets:    map[contextfabric.FactKind]map[string][]ReachedFrom{},
		byRoot:     map[contextfabric.FactKind]map[string][]contextfabric.SubjectRef{},
		expansions: map[contextfabric.FactKind]map[contextfabric.SubjectKind]derivedExpansion{},
	}
	if scope == nil {
		return index
	}
	roots := map[string]contextfabric.SubjectRef{}
	for _, root := range admitted {
		roots[contextfabric.FactSubjectKey(root)] = root
	}
	kindOfPolicy := map[contextfabric.FactScopePolicy]contextfabric.FactKind{}
	for _, event := range scope.Events {
		index.unauthorized += event.AuthorizationDroppedCount
		if event.Policy == contextfabric.FactScopePolicyNone {
			continue
		}
		kindOfPolicy[event.Policy] = event.RequirementKind
		if index.expansions[event.RequirementKind] == nil {
			index.expansions[event.RequirementKind] = map[contextfabric.SubjectKind]derivedExpansion{}
		}
		index.expansions[event.RequirementKind][event.OriginKind] = derivedExpansion{
			policy: string(event.Policy), targetKind: event.TargetKind, outcome: event.Outcome,
		}
	}
	for _, derivation := range scope.Derivations {
		kind, known := kindOfPolicy[derivation.Policy]
		if !known {
			continue
		}
		root, admittedRoot := roots[contextfabric.FactSubjectKey(derivation.Root)]
		if !admittedRoot || !slices.ContainsFunc(scope.DerivedSubjects[kind], func(subject contextfabric.SubjectRef) bool {
			return contextfabric.FactSubjectKey(subject) == contextfabric.FactSubjectKey(derivation.Target)
		}) {
			continue
		}
		if index.targets[kind] == nil {
			index.targets[kind] = map[string][]ReachedFrom{}
			index.byRoot[kind] = map[string][]contextfabric.SubjectRef{}
		}
		targetKey, rootKey := contextfabric.FactSubjectKey(derivation.Target), contextfabric.FactSubjectKey(root)
		reach := ReachedFrom{Kind: string(root.Kind), CanonicalID: root.CanonicalID, Basis: string(derivation.Basis)}
		if !slices.Contains(index.targets[kind][targetKey], reach) {
			index.targets[kind][targetKey] = append(index.targets[kind][targetKey], reach)
		}
		if !slices.ContainsFunc(index.byRoot[kind][rootKey], func(subject contextfabric.SubjectRef) bool {
			return contextfabric.FactSubjectKey(subject) == targetKey
		}) {
			index.byRoot[kind][rootKey] = append(index.byRoot[kind][rootKey], derivation.Target)
		}
	}
	for _, byTarget := range index.targets {
		for key := range byTarget {
			sort.Slice(byTarget[key], func(i, j int) bool {
				a, b := byTarget[key][i], byTarget[key][j]
				if a.Kind != b.Kind {
					return a.Kind < b.Kind
				}
				return a.CanonicalID < b.CanonicalID
			})
		}
	}
	return index
}

// reachedFrom returns the admitted roots a fact of kind on subject was
// derived from; nil when the subject is not a derived target.
func (d derivedIndex) reachedFrom(kind contextfabric.FactKind, subject contextfabric.SubjectRef) []ReachedFrom {
	return d.targets[kind][contextfabric.FactSubjectKey(subject)]
}

// guardDerivedFacts keeps a fact only when its subject is a gate-admitted
// root or a target derived from one for the fact's own kind. It returns the
// kept facts and how many it withheld.
func guardDerivedFacts(facts []contextfabric.CanonicalFact, admitted []contextfabric.SubjectRef, index derivedIndex) ([]contextfabric.CanonicalFact, int) {
	roots := make(map[string]struct{}, len(admitted))
	for _, root := range admitted {
		roots[contextfabric.FactSubjectKey(root)] = struct{}{}
	}
	kept := make([]contextfabric.CanonicalFact, 0, len(facts))
	withheld := 0
	for _, fact := range facts {
		if _, root := roots[contextfabric.FactSubjectKey(fact.Subject)]; root || len(index.reachedFrom(fact.Kind, fact.Subject)) > 0 {
			kept = append(kept, fact)
			continue
		}
		withheld++
	}
	return kept, withheld
}

// derivedCoverage reports one (kind, root) row for a root whose facts are
// reached through scope expansion. ok is false when the kind has no
// expansion decision for the root's kind.
func (d derivedIndex) derivedCoverage(kind contextfabric.FactKind, root contextfabric.SubjectRef, servedKeys map[string]bool) (targets, served int, expansion derivedExpansion, ok bool) {
	expansion, ok = d.expansions[kind][root.Kind]
	if !ok {
		return 0, 0, expansion, false
	}
	for _, target := range d.byRoot[kind][contextfabric.FactSubjectKey(root)] {
		targets++
		if servedKeys[string(kind)+"\x00"+string(target.Kind)+"\x00"+target.CanonicalID] {
			served++
		}
	}
	return targets, served, expansion, true
}

func derivedReason(targets, served int, expansion derivedExpansion) string {
	plural := subjectKindPlural(expansion.targetKind)
	if targets == 0 {
		return fmt.Sprintf("scope expansion reached no %s from this subject (expansion: %s; policy: %s)", plural, expansion.outcome, expansion.policy)
	}
	return fmt.Sprintf("read through %d %s reached by scope expansion, %d with a fact (expansion: %s; policy: %s)", targets, plural, served, expansion.outcome, expansion.policy)
}

// expansionCut reports an expansion outcome that may have left targets out.
func expansionCut(outcome contextfabric.FactScopeExpansionOutcome) bool {
	switch outcome {
	case contextfabric.FactScopeExpanded, contextfabric.FactScopeAttemptedEmpty, contextfabric.FactScopeNotNeeded:
		return false
	}
	return true
}

func joinReason(base, extra string) string {
	if strings.TrimSpace(base) == "" {
		return extra
	}
	return base + "; " + extra
}
