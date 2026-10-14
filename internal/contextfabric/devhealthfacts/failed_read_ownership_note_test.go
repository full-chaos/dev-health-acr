package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ownershipOnlyClient answers the team ownership reads (team A owns one
// repository, team B none) and fails every other read when fail is set.
type ownershipOnlyClient struct {
	fail bool
}

func (c *ownershipOnlyClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "GROUP BY team_id, repo_key") {
		return &fakeScanner{rows: [][]any{{"A", "repo-1", "org/repo-1"}}}, nil
	}
	if c.fail && !strings.Contains(statement, "SELECT DISTINCT team_id FROM team_project_ownership") {
		return nil, errors.New("table missing")
	}
	return &fakeScanner{}, nil
}

func ownershipNoteRead(t *testing.T, provider contextfabric.FactProvider, kind contextfabric.FactKind, fail bool) (contextfabric.FactProviderResult, error) {
	t.Helper()
	return provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: kind,
		Subjects: []contextfabric.SubjectRef{teamSubject("A"), teamSubject("B")},
	})
}

func failureNote(err error) string {
	var failure *contextfabric.FactReadFailure
	if errors.As(err, &failure) {
		return failure.RequestNote
	}
	return ""
}

// A kind that serves "N of M requested teams own no repository" when its read
// succeeds serves the same sentence when its read fails: the sentence comes
// from ownership, not from the failed read. A kind that fails without the
// sentence while its success path serves it would drop the note again.
func TestEveryTeamKindKeepsItsOwnershipNoteWhenItsReadFails(t *testing.T) {
	t.Parallel()
	withNote := map[contextfabric.FactKind]bool{}
	for _, probe := range devhealthfacts.NewProviders(&ownershipOnlyClient{}) {
		capability := probe.Capability()
		supportsTeam := false
		for _, subjectKind := range capability.SupportedSubjectKinds {
			supportsTeam = supportsTeam || subjectKind == contextfabric.SubjectTeam
		}
		if !supportsTeam {
			continue
		}
		kind := capability.Kind
		t.Run(string(kind), func(t *testing.T) {
			succeeded, err := ownershipNoteRead(t, findProvider(t, devhealthfacts.NewProviders(&ownershipOnlyClient{}), kind), kind, false)
			if err != nil {
				t.Fatalf("success read error = %v", err)
			}
			wantNote := strings.Contains(succeeded.RequestNote, "1 of 2 requested teams own no repository")
			_, failedErr := ownershipNoteRead(t, findProvider(t, devhealthfacts.NewProviders(&ownershipOnlyClient{fail: true}), kind), kind, true)
			if failedErr == nil {
				t.Fatalf("%s: the read did not fail although every data read failed (state %q)", kind, succeeded.State)
			}
			gotNote := strings.Contains(failureNote(failedErr), "1 of 2 requested teams own no repository")
			if wantNote != gotNote {
				t.Fatalf("%s: success serves the ownership note = %v, failure carries it = %v (success note %q, failure note %q)", kind, wantNote, gotNote, succeeded.RequestNote, failureNote(failedErr))
			}
			withNote[kind] = wantNote
		})
	}
	for _, kind := range []contextfabric.FactKind{
		contextfabric.FactHealth, contextfabric.FactOperationalDeficiencies, contextfabric.FactInvestment, contextfabric.FactMetrics,
		contextfabric.FactBlockers, contextfabric.FactDeployments, contextfabric.FactIncidents, contextfabric.FactPullRequests,
	} {
		if !withNote[kind] {
			t.Errorf("%s: its success path no longer serves the ownership note, or the kind was not measured; the census would pass vacuously", kind)
		}
	}
}

func TestFailedHealthReadCarriesTheOwnershipNote(t *testing.T) {
	t.Parallel()
	_, err := ownershipNoteRead(t, findProvider(t, devhealthfacts.NewProviders(&ownershipOnlyClient{fail: true}), contextfabric.FactHealth), contextfabric.FactHealth, true)
	if err == nil {
		t.Fatal("health read must fail when its data reads fail")
	}
	if note := failureNote(err); !strings.Contains(note, "1 of 2 requested teams own no repository") {
		t.Fatalf("failure note = %q, want the 1 of 2 ownership sentence", note)
	}
}
