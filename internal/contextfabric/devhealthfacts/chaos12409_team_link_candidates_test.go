package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// The hash read decides whether the exact read runs, a failed read fails the
// team read, and a team whose candidates are all ambiguous has no link statement
// to run. A rule that is skipped on an error would count a wrong link quietly.
func TestTeamLinkCandidatesTakeTheAmbiguousRule(t *testing.T) {
	t.Parallel()
	// A fresh slice per case: the read keeps the candidates in place.
	candidates := func() fakeTable {
		return fakeTable{match: teamLinkCandidatesMarker, rows: [][]any{{[]string{"X-1", "Y-1"}}}}
	}
	failed := errors.New("read failed")
	for _, tc := range []struct {
		name      string
		tables    []fakeTable
		wantOK    bool
		wantErr   error
		wantExact int
	}{
		{name: "no ambiguous id", tables: []fakeTable{candidates(), {match: teamAmbiguousLinkHashesMatch, rows: [][]any{{[]string{}}}}}, wantOK: true},
		{name: "one ambiguous id", tables: []fakeTable{candidates(), {match: teamAmbiguousLinkHashesMatch, rows: [][]any{{[]string{"7"}}}}, {match: teamAmbiguousLinkIDsMatch, rows: [][]any{{[]string{"X-1"}}}}}, wantOK: true, wantExact: 1},
		{name: "every id ambiguous", tables: []fakeTable{candidates(), {match: teamAmbiguousLinkHashesMatch, rows: [][]any{{[]string{"7", "8"}}}}, {match: teamAmbiguousLinkIDsMatch, rows: [][]any{{[]string{"Y-1", "X-1"}}}}}, wantExact: 1},
		{name: "the hash read failed", tables: []fakeTable{candidates(), {match: teamAmbiguousLinkHashesMatch, err: failed}}, wantErr: failed},
		{name: "the exact read failed", tables: []fakeTable{candidates(), {match: teamAmbiguousLinkHashesMatch, rows: [][]any{{[]string{"7"}}}}, {match: teamAmbiguousLinkIDsMatch, err: failed}}, wantErr: failed, wantExact: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{tables: tc.tables}
			ok, err := devhealthfacts.WithTeamLinkCandidatesForTest(context.Background(), client, "org-a", []string{"team-1"})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			exact := 0
			for _, query := range client.queries {
				if strings.Contains(query.statement, teamAmbiguousLinkIDsMatch) {
					exact++
				}
			}
			if exact != tc.wantExact {
				t.Errorf("exact reads = %d, want %d", exact, tc.wantExact)
			}
		})
	}
}
