package devhealthfacts_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type recordedStatement struct {
	reader     string
	shape      string
	start, end time.Duration
}

// turnStatementRecorder counts the ClickHouse statements of one turn and the
// longest chain of them that ran strictly one after another.
type turnStatementRecorder struct {
	origin time.Time
	mu     sync.Mutex
	items  []recordedStatement
	// spans are the intervals of the scope expander calls of the turn.
	spans []scopeSpan
}

type scopeSpan struct{ start, end time.Duration }

type turnCountingClient struct {
	inner contextpacket.ClickHouseQueryClient
	rec   *turnStatementRecorder
}

type turnCountingRows struct {
	contextpacket.ClickHouseRowScanner
	rec  *turnStatementRecorder
	idx  int
	once sync.Once
}

func (r *turnCountingRows) Close() error {
	r.once.Do(func() {
		r.rec.mu.Lock()
		r.rec.items[r.idx].end = time.Since(r.rec.origin)
		r.rec.mu.Unlock()
	})
	return r.ClickHouseRowScanner.Close()
}

func (c turnCountingClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	reader, _ := contextpacket.StatementReaderFromContext(ctx)
	c.rec.mu.Lock()
	idx := len(c.rec.items)
	c.rec.items = append(c.rec.items, recordedStatement{reader: reader, shape: statementShape(statement), start: time.Since(c.rec.origin)})
	c.rec.mu.Unlock()
	rows, err := c.inner.Query(ctx, statement, bindings)
	if err != nil {
		c.rec.mu.Lock()
		c.rec.items[idx].end = time.Since(c.rec.origin)
		c.rec.mu.Unlock()
		return nil, err
	}
	return &turnCountingRows{ClickHouseRowScanner: rows, rec: c.rec, idx: idx}, nil
}

// chain is the longest run of statements where each starts after the
// previous one ended: the number of round trips a turn pays in series.
func (r *turnStatementRecorder) chain() int {
	r.mu.Lock()
	items := append([]recordedStatement(nil), r.items...)
	r.mu.Unlock()
	return serialChain(items)
}

// serialChain is chain over the given statements.
func serialChain(items []recordedStatement) int {
	sort.Slice(items, func(i, j int) bool { return items[i].start < items[j].start })
	best, depth := 0, make([]int, len(items))
	for i := range items {
		depth[i] = 1
		for j := 0; j < i; j++ {
			if items[j].end <= items[i].start && depth[j]+1 > depth[i] {
				depth[i] = depth[j] + 1
			}
		}
		if depth[i] > best {
			best = depth[i]
		}
	}
	return best
}

// scopeMetrics reports the scope expansions of the turn: how many expander
// calls ran, how many statements started inside any of their spans, and the
// longest strictly serial chain among those statements.
func (r *turnStatementRecorder) scopeMetrics() (expansions, statements, chain int) {
	r.mu.Lock()
	items := append([]recordedStatement(nil), r.items...)
	spans := append([]scopeSpan(nil), r.spans...)
	r.mu.Unlock()
	var inside []recordedStatement
	for _, item := range items {
		for _, span := range spans {
			if item.start >= span.start && item.start <= span.end {
				inside = append(inside, item)
				break
			}
		}
	}
	return len(spans), len(inside), serialChain(inside)
}

// spanRecordingExpander is the real scope expander, observed: it records the
// interval of every expansion on the turn's recorder and changes nothing else.
type spanRecordingExpander struct {
	inner contextfabric.FactScopeExpander
	rec   *turnStatementRecorder
}

func (e spanRecordingExpander) ExpandFactScope(ctx context.Context, request contextfabric.FactScopeExpansionRequest) (contextfabric.FactScopeExpansionResult, error) {
	start := time.Since(e.rec.origin)
	result, err := e.inner.ExpandFactScope(ctx, request)
	end := time.Since(e.rec.origin)
	e.rec.mu.Lock()
	e.rec.spans = append(e.rec.spans, scopeSpan{start: start, end: end})
	e.rec.mu.Unlock()
	return result, err
}

// recordScopeSpans wraps inner so the turn counted by client records its
// expansions; a client that is not a counted one is returned unobserved.
func recordScopeSpans(client contextpacket.ClickHouseQueryClient, inner contextfabric.FactScopeExpander) contextfabric.FactScopeExpander {
	if counted, ok := client.(turnCountingClient); ok {
		return spanRecordingExpander{inner: inner, rec: counted.rec}
	}
	return inner
}

var statementTable = regexp.MustCompile(`(?is)\bfrom\s+\(?\s*(?:select.*?from\s+)?([a-z_][a-z0-9_.]*)`)

// statementShape names a statement by its first table and a short hash of its
// text, so repeats of one statement show as one name with a count.
func statementShape(statement string) string {
	table := "?"
	if match := statementTable.FindStringSubmatch(statement); match != nil {
		table = match[1]
	}
	sum := sha256.Sum256([]byte(statement))
	return table + "#" + hex.EncodeToString(sum[:3])
}

func (r *turnStatementRecorder) readers() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := map[string]int{}
	for _, item := range r.items {
		name := item.reader
		if name == "" {
			name = item.shape
		}
		counts[name]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+strconv.Itoa(counts[name]))
	}
	return strings.Join(parts, ",")
}

// countTurnStatements wraps client so the test logs, at its end, the
// statements its turn sent and the longest serial chain among them.
func countTurnStatements(t *testing.T, client contextpacket.ClickHouseQueryClient) contextpacket.ClickHouseQueryClient {
	t.Helper()
	rec := &turnStatementRecorder{origin: time.Now()}
	t.Cleanup(func() {
		rec.mu.Lock()
		total := len(rec.items)
		rec.mu.Unlock()
		expansions, scopeStatements, scopeChain := rec.scopeMetrics()
		t.Logf("turn_statements test=%s statements=%d serial_chain=%d readers=%s scope_expansions=%d scope_statements=%d scope_chain=%d", t.Name(), total, rec.chain(), rec.readers(), expansions, scopeStatements, scopeChain)
	})
	return turnCountingClient{inner: client, rec: rec}
}

func TestTurnStatementsOfAPeriodComparisonTurn(t *testing.T) {
	run := runProjectCohortInvestigationAs(t, context.Background(), projectCohortCells, contextfabric.TemporalIntentPeriodComparison)
	if run.result.Status == "" {
		t.Fatal("period comparison turn served no status")
	}
}

// scopeTurnLine fails the test unless the turn really ran scope expansions: a
// measurement of zero expansions says nothing about the scope phase.
func scopeTurnLine(t *testing.T, run projectCohortRun) {
	t.Helper()
	counted, ok := run.counted.(turnCountingClient)
	if !ok {
		t.Fatal("measurement did not happen: the turn's client is not a counting client")
	}
	expansions, statements, chain := counted.rec.scopeMetrics()
	t.Logf("scope_phase test=%s expansions=%d statements=%d serial_chain=%d", t.Name(), expansions, statements, chain)
	if expansions < 1 {
		t.Fatal("measurement did not happen: the turn ran no scope expansion")
	}
}

func TestTurnScopeStatementsOfAProjectCohortTurn(t *testing.T) {
	chfixture.RequireContainers(t)
	run := runProjectCohortInvestigationWith(t, context.Background(), projectCohortCells, "", true)
	if run.result.Status == "" {
		t.Fatal("project cohort turn served no status")
	}
	scopeTurnLine(t, run)
}

func TestTurnScopeStatementsOfAPeriodComparisonTurn(t *testing.T) {
	chfixture.RequireContainers(t)
	run := runProjectCohortInvestigationWith(t, context.Background(), projectCohortCells, contextfabric.TemporalIntentPeriodComparison, true)
	if run.result.Status == "" {
		t.Fatal("period comparison turn served no status")
	}
	scopeTurnLine(t, run)
}

// The scope metrics count only the statements that started inside an
// expansion span, and chain only those.
func TestTurnStatementRecorderScopeMetrics(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	serial := &turnStatementRecorder{
		items: []recordedStatement{
			{shape: "a", start: ms(1), end: ms(10)},
			{shape: "b", start: ms(20), end: ms(30)},
			{shape: "outside", start: ms(40), end: ms(50)},
		},
		spans: []scopeSpan{{start: ms(0), end: ms(12)}, {start: ms(18), end: ms(35)}},
	}
	if expansions, statements, chain := serial.scopeMetrics(); expansions != 2 || statements != 2 || chain != 2 {
		t.Errorf("serial scope: expansions=%d statements=%d chain=%d, want 2 2 2", expansions, statements, chain)
	}
	overlapping := &turnStatementRecorder{
		items: []recordedStatement{
			{shape: "a", start: ms(1), end: ms(10)},
			{shape: "b", start: ms(2), end: ms(11)},
		},
		spans: []scopeSpan{{start: ms(0), end: ms(12)}, {start: ms(0), end: ms(12)}},
	}
	if expansions, statements, chain := overlapping.scopeMetrics(); expansions != 2 || statements != 2 || chain != 1 {
		t.Errorf("overlapping scope: expansions=%d statements=%d chain=%d, want 2 2 1", expansions, statements, chain)
	}
	none := &turnStatementRecorder{items: []recordedStatement{{shape: "a", start: ms(1), end: ms(2)}}}
	if expansions, statements, chain := none.scopeMetrics(); expansions != 0 || statements != 0 || chain != 0 {
		t.Errorf("no expansion: expansions=%d statements=%d chain=%d, want 0 0 0", expansions, statements, chain)
	}
}
