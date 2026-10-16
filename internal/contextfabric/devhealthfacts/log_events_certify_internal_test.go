package devhealthfacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
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
	written := 0
	for _, candidate := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if bytes.Contains(candidate, []byte(`"msg":"`+event.Msg+`"`)) {
			line = candidate
			written++
		}
	}
	if written != 1 {
		t.Fatalf("one producer invocation wrote %d %q lines, want exactly one: %s", written, event.Msg, buf.String())
	}
	for _, field := range event.Fields {
		if field.Key == "request_id" {
			want["request_id"] = linkLineRequestID
		}
	}
	assertKeyOrderIsTheDeclarationOrder(t, line, event)
	log, err := certify.Parse(line)
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: event, Want: want}); err != nil {
		t.Fatalf("%s failed certification: %v\n%s", event.ID, err, line)
	}
}

// assertKeyOrderIsTheDeclarationOrder pins the raw key order of a line: time,
// level and msg, then the declared keys the line carries, in declaration order.
func assertKeyOrderIsTheDeclarationOrder(t *testing.T, line []byte, event eventspec.Event) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(line))
	if _, err := decoder.Token(); err != nil {
		t.Fatalf("token: %v", err)
	}
	var order []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		order = append(order, token.(string))
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			t.Fatalf("value: %v", err)
		}
	}
	present := map[string]bool{}
	for _, key := range order {
		present[key] = true
	}
	expected := []string{"time", "level", "msg"}
	for _, field := range event.Fields {
		if present[field.Key] {
			expected = append(expected, field.Key)
		}
	}
	if !reflect.DeepEqual(order, expected) {
		t.Fatalf("%s: key order %v differs from the declaration order %v", event.ID, order, expected)
	}
}

// TestTheTeamLinkLinesCertifyThroughTheirProducers drives the three readers
// that write the decision-basis lines through a stub client and certifies each
// line, and requires every closed value the declarations name to be one the
// producers write.
const linkLineRequestID = "req_0123456789abcdef0123456789abcdef"

func TestTheTeamLinkLinesCertifyThroughTheirProducers(t *testing.T) {
	ctx := observability.WithRequestID(context.Background(), linkLineRequestID)

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

// TestTheTeamLinkLinesCarryNoRequestIDWithoutOne: the request id is read from
// the context only; a context without one writes no request_id key, for every
// one of the three producers.
func TestTheTeamLinkLinesCarryNoRequestIDWithoutOne(t *testing.T) {
	native := [][]any{{[]string{"r1"}, []string{"1"}, []string{"t1"}, []string{""}, []uint8{3}}}
	for name, run := range map[string]func(*InvestmentProvider) error{
		"link state": func(p *InvestmentProvider) error {
			_, _, err := p.readTeamPRLinkState(context.Background(), "org-1", []string{"repo-1"}, factTimeBound{})
			return err
		},
		"linked set": func(p *InvestmentProvider) error {
			_, _, _, err := p.readLinkedPullRequestSet(context.Background(), "org-1", []string{"t1"}, factTimeBound{})
			return err
		},
		"mix set": func(p *InvestmentProvider) error {
			seeded := context.WithValue(context.Background(), loadedMembershipScopeKey{}, loadedMembershipScope{orgID: "org-1", runID: "run-1", scope: membershipScope{mode: membershipScopeNone}})
			_, err := p.readLinkedPullRequestMixRows(seeded, "org-1", []string{"t1"}, factTimeBound{})
			return err
		},
	} {
		buf := captureLinkLines(t)
		client := &linkStubClient{setRows: native, linkStateRows: [][]any{{"repo-1", uint64(5), uint64(3), uint64(0)}}}
		if err := run(newInvestmentProvider(client)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(buf.String(), "devhealthfacts.") {
			t.Fatalf("%s wrote no devhealthfacts line: %s", name, buf.String())
		}
		if strings.Contains(buf.String(), "request_id") {
			t.Fatalf("%s: a context without a request id wrote a request_id key: %s", name, buf.String())
		}
	}
}

// TestTheDataDependentLinesFollowTheLoopsThatWriteThem: one mix read of 201
// teams writes three lines of each mix event (a chunk is 100 teams), and one
// link state read of 501 repositories writes one line (it batches
// repositories and writes after the loop).
func TestTheDataDependentLinesFollowTheLoopsThatWriteThem(t *testing.T) {
	ctx := observability.WithRequestID(context.Background(), linkLineRequestID)
	teams := make([]string, 201)
	for i := range teams {
		teams[i] = "team-" + strconv.Itoa(i)
	}
	buf := captureLinkLines(t)
	native := [][]any{{[]string{"r1"}, []string{"1"}, []string{"t1"}, []string{""}, []uint8{3}}}
	seeded := context.WithValue(ctx, loadedMembershipScopeKey{}, loadedMembershipScope{orgID: "org-1", runID: "run-1", scope: membershipScope{mode: membershipScopeNone}})
	if _, err := newInvestmentProvider(&linkStubClient{setRows: native}).readLinkedPullRequestMixRows(seeded, "org-1", teams, factTimeBound{}); err != nil {
		t.Fatalf("readLinkedPullRequestMixRows: %v", err)
	}
	for _, msg := range []string{"devhealthfacts.team_linked_mix_set", "devhealthfacts.team_linked_mix_native_first"} {
		if got := strings.Count(buf.String(), `"msg":"`+msg+`"`); got != 3 {
			t.Fatalf("201 teams wrote %d %s lines, want 3: %s", got, msg, buf.String())
		}
	}
	repos := make([]string, 501)
	for i := range repos {
		repos[i] = "repo-" + strconv.Itoa(i)
	}
	buf = captureLinkLines(t)
	client := &linkStubClient{linkStateRows: [][]any{{"repo-1", uint64(5), uint64(3), uint64(0)}}}
	if _, _, err := newInvestmentProvider(client).readTeamPRLinkState(ctx, "org-1", repos, factTimeBound{}); err != nil {
		t.Fatalf("readTeamPRLinkState: %v", err)
	}
	if got := strings.Count(buf.String(), `"msg":"devhealthfacts.team_pr_link_state_native_first"`); got != 1 {
		t.Fatalf("501 repositories wrote %d link state lines, want 1: %s", got, buf.String())
	}
}

// TestTheDroppedRowDisclosureLinesCertifyThroughTheirProducers drives the
// producers of the three lines that accompany a disclosed omission.
func TestTheDroppedRowDisclosureLinesCertifyThroughTheirProducers(t *testing.T) {
	cells := contractsv1.ContextFabricClaimedFactCombinedCellsMax + 1
	fields := make(map[string]contextfabric.FactValue, cells)
	for i := 0; i < cells; i++ {
		fields["f"+strconv.Itoa(i)] = contextfabric.IntegerFactValue(1)
	}
	buf := captureLinkLines(t)
	drop, dropped, reason := disclosedDualTableDrop("flow", contextfabric.FactFlow, []contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{"a": contextfabric.IntegerFactValue(1)}}}, []contextfabric.FactValueRow{{Fields: fields}}, 2)
	if !drop || dropped != 3 || reason != factBytesBoundExceededReason {
		t.Fatalf("disclosedDualTableDrop = %v %d %q", drop, dropped, reason)
	}
	certifyLinkLine(t, buf, eventspec.FactBytesBoundExceeded, map[string]any{
		"producer": "flow", "kind": "flow",
		"combined_cells_max": contractsv1.ContextFabricClaimedFactCombinedCellsMax, "combined_bytes_max": contractsv1.ContextFabricClaimedFactCombinedContentBytesMax,
	})
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		buf = captureLinkLines(t)
		var result contextfabric.FactProviderResult
		applySubjectShapeRejection(&result, "devhealthfacts.flow", contextfabric.FactKind(kind), 4)
		if result.State != contextfabric.SourceTruncated || result.OmittedCount != 4 {
			t.Fatalf("applySubjectShapeRejection left state %q omitted %d", result.State, result.OmittedCount)
		}
		certifyLinkLine(t, buf, eventspec.SubjectIDShapeRejected, map[string]any{"producer": "devhealthfacts.flow", "kind": string(kind), "rejected": 4})
	}
	buf = captureLinkLines(t)
	recordOwningTeamsTruncated(3)
	certifyLinkLine(t, buf, eventspec.OwningTeamsTruncated, map[string]any{"producer": "devhealthfacts.identity", "omitted": 3})
}

// TestTheGeneratedSlogArgsOfTheTeamLinkLinesOmitTheRequestID: the generated
// construction covers the required fields only, so a line without a request id
// is the same line the generated arguments describe.
func TestTheGeneratedSlogArgsOfTheTeamLinkLinesOmitTheRequestID(t *testing.T) {
	args := eventspec.NewTeamLinkedMixSetFields("sent", 1, 2, "ids").SlogArgs()
	for _, arg := range args {
		if arg == "request_id" {
			t.Fatalf("generated args carry request_id: %v", args)
		}
	}
	if len(args) != 8 {
		t.Fatalf("generated args = %v, want the four required key/value pairs", args)
	}
}
