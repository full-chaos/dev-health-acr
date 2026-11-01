package v1

import (
	"strings"
	"testing"
)

func validProjectScopeWithheld(mutate func(*ContextFabricCoverageDetail)) ContextFabricCoverageDetail {
	detail := ContextFabricCoverageDetail{
		DetailID: "cov-01", Source: "canonical_fact:health", Code: ContextFabricCoverageDetailProjectScopeWithheld,
		Degrading: true, FactKind: ContextFabricFactHealth, SourceState: ContextFabricSourceUnauthorized,
		Label: "withheld", Raw: "health: withheld",
	}
	if mutate != nil {
		mutate(&detail)
	}
	return detail
}

// CHAOS-12330: exactly one of fact_kind and kind says what was withheld; the
// state is always unauthorized and the detail always degrades, so a refusal
// can never read as an ordinary empty read.
func TestAProjectScopeWithheldDetailNamesExactlyOneThingAndAlwaysDegrades(t *testing.T) {
	t.Parallel()
	byKind := func(d *ContextFabricCoverageDetail) { d.FactKind, d.Kind = "", ContextFabricSubjectTeam }
	for name, tc := range map[string]struct {
		detail ContextFabricCoverageDetail
		want   string
	}{
		"a fact kind":     {validProjectScopeWithheld(nil), ""},
		"a subject kind":  {validProjectScopeWithheld(byKind), ""},
		"both":            {validProjectScopeWithheld(func(d *ContextFabricCoverageDetail) { d.Kind = ContextFabricSubjectTeam }), "exactly one of fact_kind and kind"},
		"neither":         {validProjectScopeWithheld(func(d *ContextFabricCoverageDetail) { d.FactKind = "" }), "exactly one of fact_kind and kind"},
		"no source state": {validProjectScopeWithheld(func(d *ContextFabricCoverageDetail) { d.SourceState = "" }), "requires source_state"},
		"another state":   {validProjectScopeWithheld(func(d *ContextFabricCoverageDetail) { d.SourceState = ContextFabricSourceNoData }), "source_state unauthorized"},
		"not degrading":   {validProjectScopeWithheld(func(d *ContextFabricCoverageDetail) { d.Degrading = false }), "must degrade"},
		"a count":         {validProjectScopeWithheld(func(d *ContextFabricCoverageDetail) { d.Count = intPtr(1) }), "forbids count"},
		"supported kinds": {validProjectScopeWithheld(func(d *ContextFabricCoverageDetail) {
			d.SupportedKinds = []ContextFabricSubjectKind{ContextFabricSubjectTeam}
		}), "forbids supported_kinds"},
	} {
		err := tc.detail.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: Validate() = %v, want nil", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: Validate() = %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// Kind stays forbidden on every other code that does not require it: the new
// clause must not open it.
func TestKindStaysForbiddenOnACodeThatDoesNotRequireIt(t *testing.T) {
	t.Parallel()
	detail := validDetailForCode(ContextFabricCoverageDetailFactScoreWithheld)
	detail.Kind = ContextFabricSubjectTeam
	if err := detail.Validate(); err == nil || !strings.Contains(err.Error(), "forbids kind") {
		t.Fatalf("Validate() = %v, want forbids kind", err)
	}
}

// The reason of a withheld detail is the code plus fact_kind or kind: every
// kind of the closed vocabulary has its own server-composed sentence, inside
// the label bound, so no kind falls to the generic label.
func TestEveryFactKindHasAProjectScopeWithheldLabel(t *testing.T) {
	t.Parallel()
	generic := ComposeCoverageDetailLabel(ContextFabricCoverageDetail{Code: "unknown"})
	for _, kind := range ContextFabricFactKindVocabulary() {
		if _, ok := contextFabricProjectScopeWithheldFactReasons[kind]; !ok {
			t.Errorf("fact kind %q has no project scope withheld reason", kind)
		}
		label := ContextFabricProjectScopeWithheldFactLabel(kind)
		if label == "" || label == generic || len(label) > ContextFabricCoverageDetailLabelMaxLength {
			t.Errorf("fact kind %q has label %q", kind, label)
		}
		detail := validProjectScopeWithheld(func(d *ContextFabricCoverageDetail) { d.FactKind = kind })
		if got := ComposeCoverageDetailLabel(detail); got != label {
			t.Errorf("fact kind %q: ComposeCoverageDetailLabel() = %q, want %q", kind, got, label)
		}
	}
}

func TestProjectScopeWithheldLabelsStateTheReason(t *testing.T) {
	t.Parallel()
	for kind, want := range map[ContextFabricFactKind]string{
		ContextFabricFactHealth:       "Health facts are built from owning teams, which a project grant does not cover",
		ContextFabricFactInvestment:   "Investment facts are placed by repository or span the organization, which a project grant does not cover",
		ContextFabricFactSourceHealth: "Source health facts are organization-wide figures, which a project grant does not cover",
		ContextFabricFactPullRequests: "Pull request facts need pull requests, which are not in a project grant yet",
		ContextFabricFactBlockers:     "Blockers facts need exact project membership, which is not proven yet",
		ContextFabricFactDeployments:  "Deployment facts belong to repositories, which a project grant does not cover",
	} {
		if got := ContextFabricProjectScopeWithheldFactLabel(kind); got != want {
			t.Errorf("%s: label = %q, want %q", kind, got, want)
		}
	}
	for kind, want := range map[ContextFabricSubjectKind]string{
		ContextFabricSubjectProject:     ContextFabricRequestedProjectNarrowingUnavailableLimitation,
		ContextFabricSubjectTeam:        "Teams are not in a project grant yet",
		ContextFabricSubjectPullRequest: "Pull requests are not in a project grant yet",
		ContextFabricSubjectRepository:  "Repositories are not in a project grant",
	} {
		detail := validProjectScopeWithheld(func(d *ContextFabricCoverageDetail) { d.FactKind, d.Kind = "", kind })
		if got := ComposeCoverageDetailLabel(detail); got != want {
			t.Errorf("%s: label = %q, want %q", kind, got, want)
		}
	}
	if !strings.EqualFold(ContextFabricRequestedProjectNarrowingUnavailableLimitation, "requested project narrowing not available yet") {
		t.Errorf("limitation = %q", ContextFabricRequestedProjectNarrowingUnavailableLimitation)
	}
}
