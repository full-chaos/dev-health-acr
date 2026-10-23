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
}

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
		t.Logf("turn_statements test=%s statements=%d serial_chain=%d readers=%s", t.Name(), total, rec.chain(), rec.readers())
	})
	return turnCountingClient{inner: client, rec: rec}
}

func TestTurnStatementsOfAPeriodComparisonTurn(t *testing.T) {
	run := runProjectCohortInvestigationAs(t, context.Background(), projectCohortCells, contextfabric.TemporalIntentPeriodComparison)
	if run.result.Status == "" {
		t.Fatal("period comparison turn served no status")
	}
}
