package devhealthfacts_test

// CHAOS-4549 (chris, RULED 2026-08-29 "26"): raising acr's ClickHouse
// fixture pin from 24.8 to the 26 line removes the only CI gate that ever
// happened to reject a non-portable JOIN ON shape. CHAOS-4521b shipped a
// multi-arm `JOIN ... ON (... OR ...)` that is valid on prod's 26.7
// analyzer and rejected on 24.8 under every analyzer setting (`Code: 403
// Unsupported JOIN ON conditions`) -- caught only because acr's pinned
// fixtures ran it, after four review rounds of "executed against a real
// ClickHouse" proofs that had all run against 26.7. The equality-only-join
// rule that fix established is a STANDING RULE (cf-standing-rules.md),
// enforced from here on statically instead of by an old-engine fixture
// that only rejected the shape as a side effect.
//
// This is the acr twin of dev-health-go's own guard
// (readers/chaos4521b_project_work_scope_test.go) and of this package's
// sibling in devhealthsource
// (chaos4549_join_on_shape_test.go), widened to walk EVERY producer this
// package registers rather than pinning one reader's statement by hand.
//
// MECHANISM: build every registered devhealthfacts.FactProvider
// (devhealthfacts.NewProviders) against a fakeClient that captures every
// statement it is asked to run (helpers_test.go's existing
// fakeClient/capturedQuery -- no code changes needed there), call
// ReadFacts for the baseline subject kind every provider answers for
// (same mapping TestLiveSchemaParityAcrossEveryFactProvider uses), PLUS
// the project-subject branch for the seven producers that answer for a
// project by a real team_project_ownership/project-identity join
// (FactMetrics/Health/Workload/Investment/Readiness/Flow/Landscape --
// chaos4099_capability_kinds_test.go's realProjectJoinKinds) and the
// repository-subject branch for Flow/Landscape's second shape. Those
// project/repo branches matter here specifically because they are where
// projectIdentityJoinSQL/projectIdentityMatchSQL/projectOwnershipJoinSQL
// (shared.go) compose an ON clause at runtime -- the baseline subject
// alone never reaches them, and CHAOS-4521b's defect lived in exactly
// that composition.
//
// RULE: internal/chfixture.JoinONViolations requires every ON clause to be
// a top-level AND-conjunction of conjuncts, each EXACTLY "<operand> =
// <operand>", where an operand is a qualified column, a literal, or a
// function call of any arity of portable operands (recursively) -- so a
// literal conjunct (m.is_active = 1) and a multi-argument cast
// (ifNull(a.team_id, '')) are both portable; only OR, a comparator other
// than a single "=" per conjunct, and a bare boolean-function conjunct
// with no "=" fail. See chfixture.go's doc comment for the full rationale
// and team-lead's 2026-08-29 scope correction that relaxed this from an
// earlier, stricter draft.
import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestChaos4549AllJoinOnClausesArePortable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const orgID = "org-4549"
	principal := storage.Principal{OrgID: orgID}
	client := &fakeClient{}
	providers := devhealthfacts.NewProviders(client)
	if len(providers) == 0 {
		t.Fatal("no providers registered")
	}

	// Baseline: one subject per FactKind, the same mapping
	// TestLiveSchemaParityAcrossEveryFactProvider (schema_parity_integration_test.go)
	// uses, so every producer's default dispatch path is exercised.
	baseline := map[contextfabric.FactKind]contextfabric.SubjectRef{
		contextfabric.FactIdentity:                repoSubject("repo-1"),
		contextfabric.FactMembership:              repoSubject("repo-1"),
		contextfabric.FactStatus:                  workItemSubject("repo-1", "WI-1"),
		contextfabric.FactWork:                    workItemSubject("repo-1", "WI-1"),
		contextfabric.FactActualCompletion:        workItemSubject("repo-1", "WI-1"),
		contextfabric.FactBlockers:                workItemSubject("repo-1", "WI-2"),
		contextfabric.FactRequiredChildren:        workItemSubject("repo-1", "WI-1"),
		contextfabric.FactPullRequests:            pullRequestSubject("repo-1", "1"),
		contextfabric.FactReviews:                 reviewSubject("repo-1", "review-1"),
		contextfabric.FactContinuousIntegration:   ciRunSubject("repo-1", "run-1"),
		contextfabric.FactDeployments:             deploymentSubject("repo-1", "deploy-1"),
		contextfabric.FactIncidents:               incidentSubject("incident-1"),
		contextfabric.FactMetrics:                 repoSubject("repo-1"),
		contextfabric.FactHealth:                  repoSubject("repo-1"),
		contextfabric.FactWorkload:                teamSubject("CHAOS"),
		contextfabric.FactInvestment:              teamSubject("CHAOS"),
		contextfabric.FactReadiness:               teamSubject("CHAOS"),
		contextfabric.FactOperationalDeficiencies: teamSubject("CHAOS"),
		contextfabric.FactSourceHealth:            organizationSubject(orgID),
		contextfabric.FactFlow:                    teamSubject("CHAOS"),
		contextfabric.FactLandscape:               teamSubject("CHAOS"),
	}

	// The project-rollup producers' SECOND dispatch path -- a project
	// subject -- is where the identity/ownership join fragments actually
	// compose an ON clause; the baseline map above never reaches it.
	projectBranch := map[contextfabric.FactKind]contextfabric.SubjectRef{
		contextfabric.FactMetrics:    projectSubject("github", "proj-4549"),
		contextfabric.FactHealth:     projectSubject("github", "proj-4549"),
		contextfabric.FactWorkload:   projectSubject("github", "proj-4549"),
		contextfabric.FactInvestment: projectSubject("github", "proj-4549"),
		contextfabric.FactReadiness:  projectSubject("github", "proj-4549"),
		contextfabric.FactFlow:       projectSubject("github", "proj-4549"),
		contextfabric.FactLandscape:  projectSubject("github", "proj-4549"),
	}
	// Flow/Landscape's THIRD shape: a repository subject reads
	// repo_metrics_daily's own columns directly (CHAOS-4364 codex R2/R3
	// P2), never exercised by the team-subject baseline above.
	repoBranch := map[contextfabric.FactKind]contextfabric.SubjectRef{
		contextfabric.FactFlow:      repoSubject("repo-1"),
		contextfabric.FactLandscape: repoSubject("repo-1"),
	}

	for _, provider := range providers {
		capability := provider.Capability()
		subject, ok := baseline[capability.Kind]
		if !ok {
			t.Fatalf("fact kind %q has no CHAOS-4549 baseline subject; add one so a new producer is not silently unswept", capability.Kind)
		}
		readFactsForJoinShapeCheck(ctx, provider, principal, capability.Kind, subject)
		if subject, ok := projectBranch[capability.Kind]; ok {
			readFactsForJoinShapeCheck(ctx, provider, principal, capability.Kind, subject)
		}
		if subject, ok := repoBranch[capability.Kind]; ok {
			readFactsForJoinShapeCheck(ctx, provider, principal, capability.Kind, subject)
		}
	}

	if len(client.queries) == 0 {
		t.Fatal("no statements captured -- the fake client is not wired to the providers")
	}
	totalClauses, totalConjuncts := 0, 0
	for _, query := range client.queries {
		violations, clauses, conjuncts := chfixture.JoinONViolations(query.statement)
		totalClauses += clauses
		totalConjuncts += conjuncts
		for _, violation := range violations {
			t.Errorf("a JOIN ON conjunct is not exactly <operand> = <operand> (%q); the pre-26 ClickHouse analyzer rejects anything else in an ON clause (CHAOS-4549)\n%s", violation, query.statement)
		}
	}
	t.Logf("CHAOS-4549: checked %d JOIN ON clauses (%d conjuncts) across %d captured statements (devhealthfacts)", totalClauses, totalConjuncts, len(client.queries))
}

// readFactsForJoinShapeCheck issues one ReadFacts call purely to capture
// the SQL it sends. Errors and unavailable results are not this test's
// concern -- the fake client returns empty rows for every statement, which
// some producers surface as SourceUnavailable/no-data; all that matters
// here is which SQL text got sent to the client.
func readFactsForJoinShapeCheck(ctx context.Context, provider contextfabric.FactProvider, principal storage.Principal, kind contextfabric.FactKind, subject contextfabric.SubjectRef) {
	_, _ = provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     kind,
		Subjects: []contextfabric.SubjectRef{subject},
	})
}
