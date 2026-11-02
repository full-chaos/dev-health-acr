package devhealthfacts_test

// CHAOS-12592, EXECUTED against a real ClickHouse: the organization investment
// fact is served to the principal every web user reaches the provider with (a
// "*" grant kept by the access read) and, the same, to a repository-bound
// credential (CHAOS-12585), never counts a row of another organization, even
// under the same repository slug, and an unknown repository reads as missing,
// never as zero.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

func TestAWebUserIsServedTheOrganizationInvestmentFactAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgA, orgB = "org-12592-a", "org-12592-b"
	seedOrg := func(orgID, label string, effort float64, units int) {
		t.Helper()
		repo := repoUUID(orgID + "/" + label)
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repo, orgID, "acme/svc", "github", at); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
		for i := range units {
			if err := direct.Exec(ctx,
				`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
				fmt.Sprintf("%s-wu%d", orgID, i), at, at, effort, map[string]float64{"feature_delivery": 1.0}, map[string]float64{}, fmt.Sprintf(`{"issues":[],"prs":["%s#pr%d"]}`, repo, i), at, orgID); err != nil {
				t.Fatalf("seed unit: %v", err)
			}
		}
	}
	// Org B holds the SAME repository slug, with more units: none of them may
	// reach org A's fact.
	seedOrg(orgA, "svc", 10, 2)
	seedOrg(orgB, "svc", 99, 7)

	read := func(principal storage.Principal) contextfabric.FactProviderResult {
		t.Helper()
		result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
			Time: investmentAllHistory(), Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{organizationSubject(orgA)},
		})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return result
	}
	served := read(userprincipal.WebUser(t, orgA))
	if len(served.Facts) != 1 {
		t.Fatalf("web user: %d facts (state %q, reason %q), want the organization fact served", len(served.Facts), served.State, served.Reason)
	}
	fact := served.Facts[0]
	if n := *fact.Fields["repositories_in_scope"].Integer; n != 1 {
		t.Fatalf("repositories_in_scope = %d, want 1: org B's repository of the same slug counted", n)
	}
	if units := *fact.Fields["work_unit_count"].Integer; units != 2 {
		t.Fatalf("work_unit_count = %d, want org A's 2: a row of org B counted", units)
	}
	// An unknown repository reads as missing, never as zero or healthy (North
	// Star check 12): org B's repository, and a repository no organization has.
	// The read is scoped to org A, so neither is a fact, and the result says why.
	for name, unknown := range map[string]string{"org B's repository of the same slug": repoUUID(orgB + "/svc"), "a repository no organization has": repoUUID("acme/never")} {
		result, err := provider.ReadFacts(ctx, userprincipal.WebUser(t, orgA), contextfabric.FactQuery{
			Time: investmentAllHistory(), Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + unknown}},
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(result.Facts) != 0 {
			t.Fatalf("%s: %d facts (%+v), want none: an unknown repository is not a fact, and never a zero", name, len(result.Facts), result.Facts[0].Fields)
		}
		why := result.Reason
		for _, reason := range result.SubjectReasons {
			why += " " + reason
		}
		if result.State != contextfabric.SourceNoData || strings.TrimSpace(why) == "" {
			t.Fatalf("%s: state %q, reason %q: want no_data with its reason, never an available read of zero", name, result.State, strings.TrimSpace(why))
		}
		t.Logf("%s: state %q, reason %q", name, result.State, strings.TrimSpace(why))
	}
	// chris's ruling on CHAOS-12585: every caller reads the organization
	// investment fact, a repository-bound credential included. It is the same
	// organization total, and it names no repository.
	bound := read(userprincipal.Narrowed(t, orgA, []string{"acme/svc"}, []string{"acme/svc"}))
	if len(bound.Facts) != 1 || *bound.Facts[0].Fields["repositories_in_scope"].Integer != 1 || *bound.Facts[0].Fields["work_unit_count"].Integer != 2 {
		t.Fatalf("repository-bound credential: %d facts, state %q, reason %q; want the same organization fact", len(bound.Facts), bound.State, bound.Reason)
	}
}
