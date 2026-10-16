package devhealthfacts

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type linkScanner struct {
	rows [][]any
	next int
}

func (s *linkScanner) Next() bool { s.next++; return s.next <= len(s.rows) }
func (s *linkScanner) Scan(dest ...any) error {
	row := s.rows[s.next-1]
	for i, target := range dest {
		switch v := target.(type) {
		case *string:
			*v = row[i].(string)
		case *[]string:
			*v = row[i].([]string)
		case *[]uint8:
			*v = row[i].([]uint8)
		case *uint64:
			*v = row[i].(uint64)
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}
func (s *linkScanner) Err() error   { return nil }
func (s *linkScanner) Close() error { return nil }

// linkStubClient answers the link statements by their text.
type linkStubClient struct {
	setRows         [][]any
	coverRows       [][]any
	linkStateRows   [][]any
	coveredByNative bool
}

func (c *linkStubClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	switch {
	case strings.Contains(statement, "groupUniqArray(work_item_id)"):
		return &linkScanner{rows: c.coverRows}, nil
	case strings.Contains(statement, "groupArray(r), groupArray(n)"):
		return &linkScanner{rows: c.setRows}, nil
	case strings.Contains(statement, "FROM git_pull_requests AS pr"):
		return &linkScanner{rows: c.linkStateRows}, nil
	}
	return &linkScanner{}, nil
}

func captureLinkLines(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func certifyLinkLine(t *testing.T, buf *bytes.Buffer, event eventspec.Event, want map[string]any) {
	t.Helper()
	var line []byte
	for _, candidate := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if bytes.Contains(candidate, []byte(`"msg":"`+event.Msg+`"`)) {
			line = candidate
		}
	}
	if line == nil {
		t.Fatalf("no %q line was written: %s", event.Msg, buf.String())
	}
	log, err := certify.Parse(line)
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: event, Want: want}); err != nil {
		t.Fatalf("%s failed certification: %v\n%s", event.ID, err, line)
	}
}

// TestTheTeamLinkLinesCertifyThroughTheirProducers drives the three readers
// that write the decision-basis lines through a stub client and certifies each
// line, and requires every closed value the declarations name to be one the
// producers write.
func TestTheTeamLinkLinesCertifyThroughTheirProducers(t *testing.T) {
	ctx := context.Background()

	// team pull request link state: both outcomes.
	for _, tc := range []struct {
		suppressed uint64
		outcome    string
	}{{0, "no_fallback_link_suppressed"}, {2, "covered_fallback_links_suppressed"}} {
		buf := captureLinkLines(t)
		client := &linkStubClient{linkStateRows: [][]any{{"repo-1", uint64(5), uint64(3), tc.suppressed}}}
		if _, _, err := newInvestmentProvider(client).readTeamPRLinkState(ctx, "org-1", []string{"repo-1"}, factTimeBound{}); err != nil {
			t.Fatalf("readTeamPRLinkState: %v", err)
		}
		certifyLinkLine(t, buf, eventspec.TeamPRLinkStateNativeFirst, map[string]any{
			"outcome": tc.outcome, "repositories": 1, "pull_requests": 5, "linked": 3, "suppressed_pull_requests": int(tc.suppressed),
		})
	}

	// team linked mix native first: all three outcomes.
	native := [][]any{{[]string{"r1", "r2"}, []string{"1", "2"}, []string{"t1", "t1"}, []string{"", ""}, []uint8{3, 3}}}
	fallback := [][]any{{[]string{"r1", "r2"}, []string{"1", "2"}, []string{"t1", "t1"}, []string{"i1", "i2"}, []uint8{1, 1}}}
	for _, tc := range []struct {
		name  string
		set   [][]any
		cover [][]any
		want  map[string]any
	}{
		{"no fallback rows", native, nil, map[string]any{"outcome": "no_fallback_rows", "link_rows": 2, "fallback_rows": 0, "dropped_rows": 0, "native_cover_issues": 0, "native_cover_pull_requests": 0}},
		{"no fallback row covered", fallback, [][]any{{[]string{}, []string{}}}, map[string]any{"outcome": "no_fallback_row_covered", "link_rows": 2, "fallback_rows": 2, "dropped_rows": 0, "native_cover_issues": 0, "native_cover_pull_requests": 0}},
		{"covered fallback rows dropped", fallback, [][]any{{[]string{"i1"}, []string{"r2|2"}}}, map[string]any{"outcome": "covered_fallback_rows_dropped", "link_rows": 2, "fallback_rows": 2, "dropped_rows": 2, "native_cover_issues": 1, "native_cover_pull_requests": 1}},
	} {
		buf := captureLinkLines(t)
		client := &linkStubClient{setRows: tc.set, coverRows: tc.cover}
		if _, _, _, err := newInvestmentProvider(client).readLinkedPullRequestSet(ctx, "org-1", []string{"t1"}, factTimeBound{}); err != nil {
			t.Fatalf("%s: readLinkedPullRequestSet: %v", tc.name, err)
		}
		certifyLinkLine(t, buf, eventspec.TeamLinkedMixNativeFirst, tc.want)
	}

	// team linked mix set: both outcomes, every scope mode.
	scopes := []membershipScope{
		{mode: membershipScopeNone}, {mode: membershipScopeIDs, ids: []string{"u1"}}, {mode: membershipScopeTableIDs, ids: []string{"u1"}}, subqueryMembershipScope,
	}
	seenModes := map[string]bool{}
	for _, scope := range scopes {
		for _, tc := range []struct {
			set     [][]any
			outcome string
			linked  int
		}{{native, "sent", 2}, {[][]any{{[]string{}, []string{}, []string{}, []string{}, []uint8{}}}, "empty_set_mix_skipped", 0}} {
			buf := captureLinkLines(t)
			client := &linkStubClient{setRows: tc.set}
			provider := newInvestmentProvider(client)
			seeded := context.WithValue(ctx, loadedMembershipScopeKey{}, loadedMembershipScope{orgID: "org-1", runID: "run-1", scope: scope})
			if _, err := provider.readLinkedPullRequestMixRows(seeded, "org-1", []string{"t1"}, factTimeBound{}); err != nil {
				t.Fatalf("readLinkedPullRequestMixRows(%s): %v", scope.modeName(), err)
			}
			seenModes[scope.modeName()] = true
			certifyLinkLine(t, buf, eventspec.TeamLinkedMixSet, map[string]any{
				"outcome": tc.outcome, "teams": 1, "linked_pull_requests": tc.linked, "scope_mode": scope.modeName(),
			})
		}
	}
	var declared []string
	for _, field := range eventspec.TeamLinkedMixSet.Fields {
		if field.Key == "scope_mode" {
			declared = field.ClosedVocabulary
		}
	}
	if len(seenModes) != len(declared) {
		t.Errorf("the drive wrote scope modes %v, the declaration lists %v", seenModes, declared)
	}
	for _, mode := range declared {
		if !seenModes[mode] {
			t.Errorf("declared scope mode %q was not written by any driven scope", mode)
		}
	}
	for _, field := range eventspec.TeamLinkedMixSet.Fields {
		if field.Key == "teams" && (field.MaxInt == nil || *field.MaxInt != linkedMixTeamChunk) {
			t.Errorf("teams is declared with maximum %v, the producer's chunk is %d", field.MaxInt, linkedMixTeamChunk)
		}
	}
}
