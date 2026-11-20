package mcp

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/version"
	"github.com/stretchr/testify/require"
)

type scriptedElicit struct {
	res   elicitResult
	asked int
}

func (s *scriptedElicit) ask(context.Context, elicitChoice) elicitResult {
	s.asked++
	return s.res
}

type ambiguousShape struct {
	subjects   int
	total      int
	truncated  bool
	complete   bool
	nextCursor string
	status     string
}

func ambiguousJSON(s ambiguousShape) []byte {
	var subs []string
	for i := 0; i < s.subjects; i++ {
		subs = append(subs, fmt.Sprintf(`{"kind":"repository","canonical_id":"repository:%032d","label":"x/y","match":"exact"}`, i))
	}
	cursor := ""
	if s.nextCursor != "" {
		cursor = fmt.Sprintf(`,"next_cursor":%q`, s.nextCursor)
	}
	return []byte(fmt.Sprintf(`{"status":%q,"subjects":[%s],"population":{"returned":%d,"total_known":%d,"truncated":%t},"page":{"returned":%d,"complete":%t%s}}`,
		s.status, strings.Join(subs, ","), s.subjects, s.total, s.truncated, s.subjects, s.complete, cursor))
}

func whole(n int) ambiguousShape {
	return ambiguousShape{subjects: n, total: n, complete: true, status: "ambiguous"}
}

func guardCfg(t *testing.T) (*ProcessConfig, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return NewProcessConfig(sidecar.Config{LogLevel: slog.LevelInfo}, version.Info{}, &buf), &buf
}

func TestAmbiguousElicitationOnlyWhenThePageIsTheWholeSet(t *testing.T) {
	pageOf25of26 := ambiguousShape{subjects: 25, total: 26, complete: false, status: "ambiguous"}
	finalPageOf25of26 := ambiguousShape{subjects: 25, total: 26, complete: true, status: "ambiguous"}
	for name, tc := range map[string]struct {
		shape ambiguousShape
		asks  bool
	}{
		"control 3 complete":              {whole(3), true},
		"2 complete":                      {whole(2), true},
		"25 complete":                     {whole(25), true},
		"26 matches page of 25":           {pageOf25of26, false},
		"26 matches final page of 25":     {finalPageOf25of26, false},
		"limit-N below total":             {ambiguousShape{subjects: 2, total: 5, complete: false, status: "ambiguous"}, false},
		"truncated population":            {ambiguousShape{subjects: 2, total: 2, truncated: true, complete: true, status: "ambiguous"}, false},
		"truncated 2000 known page of 25": {ambiguousShape{subjects: 25, total: 2000, truncated: true, complete: false, status: "ambiguous"}, false},
		"cursor present":                  {ambiguousShape{subjects: 3, total: 3, complete: true, nextCursor: "c", status: "ambiguous"}, false},
		"page not complete":               {ambiguousShape{subjects: 3, total: 3, complete: false, status: "ambiguous"}, false},
		"total over ceiling":              {ambiguousShape{subjects: 26, total: 26, complete: true, status: "ambiguous"}, false},
		"count differs from page length":  {ambiguousShape{subjects: 3, total: 4, complete: true, status: "ambiguous"}, false},
		"single subject":                  {whole(1), false},
		"status complete":                 {ambiguousShape{subjects: 3, total: 3, complete: true, status: "complete"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			raw := ambiguousJSON(tc.shape)
			cfg, _ := guardCfg(t)
			tr := &scriptedElicit{res: elicitResult{Outcome: elicitAccepted, Value: fmt.Sprintf("repository:%032d", 0)}}
			got := pickAmbiguousSubject(context.Background(), cfg, tr, raw)
			if tc.asks {
				require.Equal(t, 1, tr.asked)
				require.NotEqual(t, string(raw), string(got))
				return
			}
			require.Equal(t, 0, tr.asked, "must not ask")
			require.Equal(t, string(raw), string(got), "must keep today's bytes")
		})
	}
}

func TestAmbiguousElicitationMissingPopulationFieldsKeepTodaysBytes(t *testing.T) {
	for name, raw := range map[string]string{
		"no population": `{"status":"ambiguous","subjects":[{"canonical_id":"a"},{"canonical_id":"b"}],"page":{"returned":2,"complete":true}}`,
		"no page":       `{"status":"ambiguous","subjects":[{"canonical_id":"a"},{"canonical_id":"b"}],"population":{"returned":2,"total_known":2,"truncated":false}}`,
		"no truncated":  `{"status":"ambiguous","subjects":[{"canonical_id":"a"},{"canonical_id":"b"}],"population":{"returned":2,"total_known":2},"page":{"returned":2,"complete":true}}`,
		"not json":      `not json`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := guardCfg(t)
			tr := &scriptedElicit{res: elicitResult{Outcome: elicitAccepted, Value: "a"}}
			require.Equal(t, raw, string(pickAmbiguousSubject(context.Background(), cfg, tr, []byte(raw))))
			require.Equal(t, 0, tr.asked)
		})
	}
}

func TestAmbiguousElicitationOutcomeLine(t *testing.T) {
	first := fmt.Sprintf("repository:%032d", 0)
	for name, tc := range map[string]struct {
		shape   ambiguousShape
		tr      elicitTransport
		outcome string
		asked   bool
		offered int
	}{
		"accepted":       {whole(3), &scriptedElicit{res: elicitResult{Outcome: elicitAccepted, Value: first}}, "accepted", true, 3},
		"declined":       {whole(3), &scriptedElicit{res: elicitResult{Outcome: elicitDeclined}}, "declined", true, 3},
		"client error":   {whole(3), &scriptedElicit{res: elicitResult{Outcome: elicitUnavailable}}, "unavailable", false, 3},
		"bad value":      {whole(3), &scriptedElicit{res: elicitResult{Outcome: elicitAccepted, Value: "not offered"}}, "unavailable", true, 3},
		"no transport":   {whole(3), nil, "unavailable", false, 3},
		"single subject": {whole(1), &scriptedElicit{}, "not_eligible", false, 0},
		"not eligible":   {ambiguousShape{subjects: 25, total: 26, complete: false, status: "ambiguous"}, &scriptedElicit{}, "not_eligible", false, 0},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, buf := guardCfg(t)
			pickAmbiguousSubject(context.Background(), cfg, tc.tr, ambiguousJSON(tc.shape))
			log, err := certify.Parse(buf.Bytes())
			require.NoError(t, err)
			require.Len(t, log.LinesWithMsg(eventspec.MCPElicitationOutcome.Msg), 1)
			_, err = certify.Certify(log, certify.Assertion{Event: eventspec.MCPElicitationOutcome, Want: map[string]any{
				"tool": "find_subjects", "outcome": tc.outcome, "asked": tc.asked, "offered": float64(tc.offered),
			}})
			require.NoError(t, err)
			require.NotContains(t, buf.String(), "repository:")
		})
	}
	t.Run("non ambiguous status emits no line", func(t *testing.T) {
		cfg, buf := guardCfg(t)
		pickAmbiguousSubject(context.Background(), cfg, nil, ambiguousJSON(ambiguousShape{subjects: 3, total: 3, complete: true, status: "complete"}))
		require.NotContains(t, buf.String(), eventspec.MCPElicitationOutcome.Msg)
	})
}
