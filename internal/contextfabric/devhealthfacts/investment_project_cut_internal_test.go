package devhealthfacts

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

func cutTestSubject(id string) contextfabric.SubjectRef {
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{"linear", id}, nil)
	if err != nil || omitted {
		panic("cutTestSubject: " + id)
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonicalID, Label: id}
}

func cutTestFact(subject contextfabric.SubjectRef, source string) contextfabric.CanonicalFact {
	fields := map[string]contextfabric.FactValue{}
	if source != "" {
		fields[contextfabric.FactFieldInvestmentMixSource] = contextfabric.StringFactValue(source)
	}
	return contextfabric.CanonicalFact{Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields}
}

func TestSplitProjectsCutByRowCapNamesARollupProjectBeyondTheNativeCut(t *testing.T) {
	t.Parallel()
	served, rollupBeyond, nativeBeyond, noFactBefore, noFactBeyond := cutTestSubject("a"), cutTestSubject("m"), cutTestSubject("n"), cutTestSubject("b"), cutTestSubject("z")
	if projectSpanKey(served) == "" {
		t.Fatalf("test subject has no project key")
	}
	facts := []contextfabric.CanonicalFact{
		cutTestFact(served, contextfabric.InvestmentMixSourceProjectNative),
		cutTestFact(rollupBeyond, contextfabric.InvestmentMixSourceOwningTeamRollup),
		cutTestFact(nativeBeyond, contextfabric.InvestmentMixSourceProjectNative),
	}
	subjects := []contextfabric.SubjectRef{served, rollupBeyond, nativeBeyond, noFactBefore, noFactBeyond}
	noMix, cut := splitProjectsCutByRowCap(subjects, facts, "", "linear:g")
	cutSet := map[string]bool{}
	for _, s := range cut {
		cutSet[s.Label] = true
	}
	if !cutSet["m"] || !cutSet["z"] || len(cut) != 2 {
		t.Fatalf("cut = %v, want the roll-up project and the no-fact project beyond the native cut", cut)
	}
	if len(noMix) != 1 || noMix[0].Label != "b" {
		t.Fatalf("noMix = %v, want only the no-fact project the cut read did reach", noMix)
	}
}

func TestSplitProjectsCutByRowCapNamesACountOnlyFactBeyondTheRollupCut(t *testing.T) {
	t.Parallel()
	countOnly, before := cutTestSubject("n"), cutTestSubject("b")
	facts := []contextfabric.CanonicalFact{cutTestFact(countOnly, ""), cutTestFact(before, "")}
	noMix, cut := splitProjectsCutByRowCap([]contextfabric.SubjectRef{countOnly, before}, facts, "linear:g", "")
	if len(cut) != 1 || cut[0].Label != "n" || len(noMix) != 0 {
		t.Fatalf("cut = %v noMix = %v, want only the count-only fact past the roll-up cut", cut, noMix)
	}
}
