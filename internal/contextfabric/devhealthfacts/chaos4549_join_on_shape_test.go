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
// (same mapping TestLiveSchemaParityAcrossEveryFactProvider uses), PLUS:
//   - the project-subject branch for the seven producers that answer for a
//     project by a real team_project_ownership/project-identity join
//     (FactMetrics/Health/Workload/Investment/Readiness/Flow/Landscape --
//     chaos4099_capability_kinds_test.go's realProjectJoinKinds) -- this is
//     where projectIdentityJoinSQL/projectIdentityMatchSQL/
//     projectOwnershipJoinSQL (shared.go) compose an ON clause at runtime,
//     which the baseline subject alone never reaches, and CHAOS-4521b's
//     defect lived in exactly that composition;
//   - the repository-subject branch for Flow/Landscape's second shape;
//   - the work-item-subject branch for Identity/Membership's second
//     declared subject kind (readers.ReadWorkItemRepository, a distinct
//     JOIN from the repository-subject baseline);
//   - sweepScopeExpander, which drives chaos4099_scope_expander.go's
//     ScopeExpander through all six FactScopePolicy values against the
//     SAME fakeClient -- a completely separate production JOIN ON surface
//     from the FactProvider sweep above, and (codex review finding)
//     unswept before this change.
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
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	// Identity/Membership's SECOND declared subject kind: a work item,
	// dispatching to readers.ReadWorkItemRepository -- a completely
	// different JOIN ON than the repository-subject baseline (codex
	// review finding: MembershipProvider declares SubjectWorkItem but was
	// never actually exercised at that kind).
	workItemBranch := map[contextfabric.FactKind]contextfabric.SubjectRef{
		contextfabric.FactIdentity:   workItemSubject("repo-1", "WI-1"),
		contextfabric.FactMembership: workItemSubject("repo-1", "WI-1"),
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
		if subject, ok := workItemBranch[capability.Kind]; ok {
			readFactsForJoinShapeCheck(ctx, provider, principal, capability.Kind, subject)
		}
	}

	// chaos4099_scope_expander.go's ScopeExpander is a SEPARATE production
	// SQL surface from the FactProvider sweep above -- its own JOIN ON
	// queries were entirely unswept until this codex review finding. It
	// runs on its own isolated, seeded fakeClient (sweepScopeExpander's
	// doc comment explains why) and its captured statements are merged in
	// here, checked by the same loop below.
	allQueries := append([]capturedQuery{}, client.queries...)
	allQueries = append(allQueries, sweepScopeExpander(ctx, orgID)...)

	if len(allQueries) == 0 {
		t.Fatal("no statements captured -- the fake client is not wired to the providers")
	}
	// codex review finding, made self-checking: the second-hop
	// pull-request-review policy's distinct JOIN --
	// pullRequestReviewsForRepositories' own hand-written
	// "r.repo_id = p.repo_id AND r.number = p.number AND r.org_id =
	// p.org_id" ON clause, aliases unique to that function -- is the
	// exact query that went unreached before sweepScopeExpander seeded a
	// first-hop candidate. A plain "git_pull_request_reviews" substring
	// is NOT specific enough: ReviewsProvider's own baseline (FactReviews
	// -> readers.ReadPullRequestReviews, a different, vendored query)
	// already reads that table regardless of ScopeExpander, so that
	// weaker assertion would falsely pass even against the pre-fix
	// sweepScopeExpander (caught while writing this test's own red-first
	// proof). Assert the SPECIFIC join instead, so a future regression
	// that silently stops reaching the second hop again fails loudly
	// here instead of reading as "0 violations".
	if !anyStatementContains(allQueries, "r.repo_id = p.repo_id AND r.number = p.number AND r.org_id = p.org_id") {
		t.Fatal("no captured statement carries pullRequestReviewsForRepositories' own ON clause -- sweepScopeExpander's second-hop review policies never reached their own query")
	}
	totalClauses, totalConjuncts := 0, 0
	for _, query := range allQueries {
		violations, clauses, conjuncts := chfixture.JoinONViolations(query.statement)
		totalClauses += clauses
		totalConjuncts += conjuncts
		for _, violation := range violations {
			t.Errorf("a JOIN ON conjunct is not exactly <operand> = <operand> (%q); the pre-26 ClickHouse analyzer rejects anything else in an ON clause (CHAOS-4549)\n%s", violation, query.statement)
		}
	}
	// codex review finding: a sweep that only checks "at least one
	// statement was captured" still passes if every captured statement
	// happens to carry zero JOIN clauses (a parser regression that stops
	// matching ON at all would read as "0 violations" instead of failing
	// loudly) -- assert real structural coverage, not just query count.
	if totalClauses == 0 {
		t.Fatal("0 JOIN ON clauses found across all captured statements -- the ON/AND parser is not matching anything, not proof of portability")
	}
	t.Logf("CHAOS-4549: checked %d JOIN ON clauses (%d conjuncts) across %d captured statements (devhealthfacts)", totalClauses, totalConjuncts, len(allQueries))
}

// sweepScopeExpander exercises every contextfabric.FactScopePolicy
// chaos4099_scope_expander.go implements, each a distinct production JOIN
// ON query ScopeExpander composes -- entirely separate from the
// FactProvider surface the rest of this test sweeps (codex review
// finding). It uses its OWN isolated fakeClient rather than sharing the
// providers' one, for two reasons: (1) seeding rows a second-hop policy
// needs (below) into a client shared with 20+ other provider calls risks
// a match-string collision silently feeding an unrelated producer's query
// the wrong column shape, and (2) codex review finding -- an EMPTY fake
// client makes every first-hop repository lookup return zero rows, so
// ExpandFactScope exits before the second-hop pull-request/review
// policies ever issue THEIR queries at all; this claimed to sweep six
// policies while only ever reaching the first hop of two of them. Seeding
// one authorized repository candidate for both the project-origin
// (work_items) and team-origin (work_item_team_attributions) first-hop
// queries lets every policy's real query -- including the distinct
// pullRequestsForRepositories/pullRequestReviewsForRepositories joins --
// actually run and get captured. Errors are not this test's concern, same
// as readFactsForJoinShapeCheck: only which SQL text reached the client
// matters here.
func sweepScopeExpander(ctx context.Context, orgID string) []capturedQuery {
	const (
		scopeRepoID   = "11111111-1111-1111-1111-111111111111"
		scopeRepoSlug = "example-org/scope-repo"
	)
	client := &fakeClient{tables: []fakeTable{
		// projectRepositories' first hop (chaos4099_scope_expander.go).
		// Column order: toString(w.repo_id), ifNull(r.repo, ''), min(p.id).
		{match: "FROM work_items AS w FINAL", rows: [][]any{
			{scopeRepoID, scopeRepoSlug, "proj-4549-scope"},
		}},
		// teamRepositories' first hop. Column order: toString(w.repo_id),
		// ifNull(r.repo, ''), argMin(source), min(a.team_id).
		{match: "FROM work_item_team_attributions AS a FINAL", rows: [][]any{
			{scopeRepoID, scopeRepoSlug, "native_team", "CHAOS"},
		}},
	}}
	expander := devhealthfacts.NewScopeExpander(client)
	// RepositoryScopes: ["*"] authorizes every repository (matches
	// chaos4109_axis_rejection_test.go's own request shape) -- without it
	// the seeded candidate above is dropped as unauthorized and the
	// second hop still never fires.
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}
	project := projectSubject("github", "proj-4549-scope")
	team := teamSubject("CHAOS")
	policies := []struct {
		policy  contextfabric.FactScopePolicy
		origin  contextfabric.SubjectRef
		target  contextfabric.SubjectKind
		require contextfabric.FactKind
	}{
		{contextfabric.FactScopePolicyProjectWorkItemRepository, project, contextfabric.SubjectRepository, contextfabric.FactMetrics},
		{contextfabric.FactScopePolicyProjectWorkItemPullRequest, project, contextfabric.SubjectPullRequest, contextfabric.FactPullRequests},
		{contextfabric.FactScopePolicyProjectWorkItemPullRequestReview, project, contractsv1.ContextFabricSubjectPullRequestReview, contextfabric.FactReviews},
		{contextfabric.FactScopePolicyTeamPrimaryAttributionRepository, team, contextfabric.SubjectRepository, contextfabric.FactMetrics},
		{contextfabric.FactScopePolicyTeamPrimaryAttributionPullRequest, team, contextfabric.SubjectPullRequest, contextfabric.FactPullRequests},
		{contextfabric.FactScopePolicyTeamPrimaryAttributionPullRequestReview, team, contractsv1.ContextFabricSubjectPullRequestReview, contextfabric.FactReviews},
	}
	for _, p := range policies {
		_, _ = expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
			Principal:       principal,
			RequirementKind: p.require,
			Origins:         []contextfabric.SubjectRef{p.origin},
			Policy:          p.policy,
			TargetKind:      p.target,
			TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Limit:           20,
		})
	}
	return client.queries
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

func anyStatementContains(queries []capturedQuery, substring string) bool {
	for _, query := range queries {
		if strings.Contains(query.statement, substring) {
			return true
		}
	}
	return false
}
