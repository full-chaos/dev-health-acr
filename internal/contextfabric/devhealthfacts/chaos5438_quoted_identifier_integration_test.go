package devhealthfacts_test

// CHAOS-5438 / round-483-r1 P3(a) -- the quote-and-backslash identifier,
// against a REAL ClickHouse.
//
// WHY THIS TEST EXISTS AND WHY A FAKE CLIENT CANNOT BE IT. Landing the probe
// fix moved this repository's dev-health-go pin across TWO releases, v0.6.3 ->
// v0.6.6, so it also inherited v0.6.4's change to how `Array(String)`
// parameter quotes are escaped (doubling rather than backslash) -- a
// behaviour change this repository never authored and never took on its own.
// Every reader touched by the probe fix binds its subject ids as
// `{ids:Array(String)}`, so that escaping sits directly on the fixed path.
//
// The container proof already on the record exercises that binding with
// ORDINARY ids, which cannot distinguish correct escaping from any other
// escaping: `WIDGET-101` has nothing to escape. Only an id carrying a single
// quote and a backslash can tell the two apart, and only a real server can
// judge it -- the fake client never parses the statement at all, so under it
// a totally broken escape would still "pass".
//
// WHAT RUNNING IT ACTUALLY FOUND, and why the assertions below are shaped the
// way they are. The pin bump changes behaviour for TWO different characters in
// opposite directions, and only an executed run could tell them apart:
//
//   a single quote  -- v0.6.3 escaped it as \' ; the native-protocol parameter
//                      parser REJECTS that form. v0.6.6 doubles it ('') and it
//                      now round-trips byte-exact. This path got BETTER.
//   a backslash     -- v0.6.3 escaped it as \\ and accepted the value.
//                      v0.6.6 FAILS CLOSED with ErrUnsafeBindingValue, because
//                      executed proof upstream showed a doubled backslash next
//                      to another escape can silently DROP BYTES rather than
//                      error. A value that round-trips shorter than it was
//                      sent is a worse failure than a refusal.
//
// So a backslash-bearing work-item id that this repository previously read
// (possibly wrongly) now makes the fact read ERROR. That is a real, inherited
// behaviour change, it is the safer direction, and it is LOUD -- which is the
// property this test pins, because the alternative a reader would assume is a
// silent empty read indistinguishable from a work item with no data.
//
// VENUE: bigboy, with the acr mirror prefixes exported --
//	TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX=ghcr.io/full-chaos/dev-health-acr
//	ACR_IMAGE_MIRROR_PREFIX=ghcr.io/full-chaos/dev-health-acr/

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos5438AwkwardWorkItemID carries BOTH escape-sensitive characters: a
// single quote (what v0.6.4 changed the handling of) and a backslash (what
// the previous handling used as its escape character, so a stale escaper
// mangles it). Modelled on a real shape -- Jira/Linear ids and titles do
// carry apostrophes.
const (
	// The QUOTE-bearing id: what v0.6.4/v0.6.6 changed the handling of, and
	// what must now round-trip byte-exact. Modelled on a real shape -- Jira
	// and Linear ids and titles do carry apostrophes.
	chaos5438QuotedWorkItemID = `O'Brien-42`
	// The BACKSLASH-bearing id: what v0.6.6 refuses outright.
	chaos5438BackslashWorkItemID = `back\slash-42`
	chaos5438AwkwardRepoSlug     = `acme/o'brien-service`
)

func TestChaos5438_QuotedAndEscapedIdentifiersSurviveTheArrayBinding(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Second)

	const repoID = "cc398fbc-1945-3717-05d8-eb78866b4e92"
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustSeed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, chaos5438AwkwardRepoSlug, "github", at)
	mustSeed("backslash work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos5438BackslashWorkItemID, repoID, orgID, "t", "open", "", "", "", at)
	mustSeed("quoted work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos5438QuotedWorkItemID, repoID, orgID, `a title with an ' and a \ in it`, "open", "", "", "", at)

	principal := storage.Principal{OrgID: orgID}
	subject := workItemSubject(repoID, chaos5438QuotedWorkItemID)

	// Every provider whose statement this change touched and whose subject is
	// a work item, driven with a QUOTE-bearing id: each must return its fact,
	// bound to the exact subject asked for. This is the half of v0.6.4's
	// escaping change that had to start working, and an ordinary id like
	// WIDGET-101 cannot demonstrate it -- there is nothing there to escape.
	for _, arm := range []struct {
		name string
		kind contextfabric.FactKind
	}{
		{"status", contextfabric.FactStatus},
		{"work", contextfabric.FactWork},
		{"actual_completion", contextfabric.FactActualCompletion},
		{"identity", contextfabric.FactIdentity},
		{"membership", contextfabric.FactMembership},
	} {
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			provider := findProvider(t, devhealthfacts.NewProviders(query), arm.kind)
			result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
				Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind:     arm.kind,
				Subjects: []contextfabric.SubjectRef{subject},
			})
			if err != nil {
				t.Fatalf("%s: ReadFacts: %v", arm.name, err)
			}
			if len(result.Facts) != 1 {
				t.Fatalf("%s: len(Facts) = %d, want 1 -- an id carrying %q matched nothing, which is what a WRONG Array(String) quote escape looks like: silent, not loud",
					arm.name, len(result.Facts), chaos5438QuotedWorkItemID)
			}
			if got := result.Facts[0].Subject.CanonicalID; got != subject.CanonicalID {
				t.Fatalf("%s: fact bound to %q, want %q -- a mismatched bind is worse than an empty one", arm.name, got, subject.CanonicalID)
			}
			if result.Truncated {
				t.Fatalf("%s: Truncated = true for a single row", arm.name)
			}
		})
	}

	// The BACKSLASH half: refused, and refused LOUDLY. The assertion is on
	// the error, not on emptiness -- a silent empty read here would be
	// indistinguishable from a work item that genuinely has no data, which is
	// exactly the failure the upstream guard exists to prevent.
	t.Run("backslash_fails_closed_rather_than_silently", func(t *testing.T) {
		backslashSubject := workItemSubject(repoID, chaos5438BackslashWorkItemID)
		provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactStatus)
		result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
			Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind:     contextfabric.FactStatus,
			Subjects: []contextfabric.SubjectRef{backslashSubject},
		})
		if err == nil {
			t.Fatalf("a backslash-bearing id was ACCEPTED (facts=%d) -- v0.6.6 fails closed on it precisely because a doubled backslash can silently drop bytes; accepting it means the guard is gone",
				len(result.Facts))
		}
		if len(result.Facts) != 0 {
			t.Fatalf("a refused read still returned %d fact(s)", len(result.Facts))
		}
	})

	// The REPOSITORY-subject branch too (CHAOS-5474): its slug carries a
	// quote, and it shares the truncation flag with the work-item branch.
	t.Run("repository_identity", func(t *testing.T) {
		provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactIdentity)
		result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
			Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind:     contextfabric.FactIdentity,
			Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("len(Facts) = %d, want 1 for the quote-bearing repository", len(result.Facts))
		}
		if result.Truncated {
			t.Fatalf("Truncated = true for a single repository row")
		}
	})
}
