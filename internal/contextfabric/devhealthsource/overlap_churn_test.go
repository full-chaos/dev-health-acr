package devhealthsource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// liveKeysetRows is keysetRows that a writer keeps appending to between ticks.
type liveKeysetRows struct{ rows keysetRows }

func (s *liveKeysetRows) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return s.rows.Query(ctx, statement, bindings)
}

func scanRepositoryKeysetRow(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
	var at time.Time
	var key string
	if err := r.Scan(&at, &key); err != nil {
		return nil, err
	}
	slug := "acme/" + key
	entity := contractsv1.ContextFabricEntityProjection{
		Subject:        contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + key, Label: slug},
		Authorization:  repoAuthorization(slug),
		EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, key)},
		ObservedAt:     at, ValidFrom: requiredTime(at), SourceVersion: ClickHouseSourceVersion,
	}
	return []candidate{{observedAt: at, sortKey: key, entity: &entity}}, nil
}

const churnTick = 15 * time.Second

// churnRig drives one source the way the coordinator does: per tick it calls
// nextBatch until the source reports nothing available, and persists each
// batch's NextCursor as the checkpoint.
type churnRig struct {
	t         *testing.T
	store     *liveKeysetRows
	plan      sourcePlan
	now       time.Time
	cursor    string
	tick      int
	seq       int
	projected map[string]int // row key -> tick of the batch that carried it
	logs      bytes.Buffer
}

func newChurnRig(t *testing.T, start time.Time) *churnRig {
	r := &churnRig{t: t, store: &liveKeysetRows{}, now: start, projected: map[string]int{}}
	r.plan = sourcePlan{
		client: keysetRows{}, source: "overlap_churn_test", version: ClickHouseSourceVersion,
		tables: []entityTable{{name: "repos", query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
			return fetch(ctx, r.store, "", rowLimitBindings(orgID, cursor, limit), limit, scanRepositoryKeysetRow)
		}}},
		now: func() time.Time { return r.now }, overlap: defaultReprojectOverlap, window: newWindowMemo(),
		logger: slog.New(slog.NewJSONHandler(&r.logs, nil)),
	}
	return r
}

func (r *churnRig) key() string {
	r.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
}

// land writes n rows whose stamps are spread evenly over (from, to].
func (r *churnRig) land(n int, from, to time.Time) {
	step := to.Sub(from) / time.Duration(n)
	for i := 1; i <= n; i++ {
		r.store.rows = append(r.store.rows, keysetRow{at: from.Add(time.Duration(i) * step).Truncate(time.Microsecond), key: r.key()})
	}
}

func (r *churnRig) landOne(at time.Time) string {
	key := r.key()
	r.store.rows = append(r.store.rows, keysetRow{at: at, key: key})
	return key
}

type churnTickResult struct {
	paged, window int
	yield         map[string]any // the "pass continues" line of this tick, if any
	open          map[string]any // the line of a tick that ended with the pass open, whatever its level
}

func (r *churnRig) runTick() churnTickResult {
	r.t.Helper()
	r.tick++
	r.logs.Reset()
	var out churnTickResult
	for calls := 0; ; calls++ {
		if calls > 2000 {
			r.t.Fatalf("tick %d did not end", r.tick)
		}
		batch, available, err := r.plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: r.plan.source, Cursor: r.cursor})
		if err != nil {
			r.t.Fatalf("tick %d: %v", r.tick, err)
		}
		if !available {
			break
		}
		moved := true
		if r.cursor != "" {
			before, _ := decodeCursor(r.cursor)
			after, _ := decodeCursor(batch.NextCursor)
			moved = !before.Since.Equal(after.Since) || before.After != after.After
		}
		if moved {
			out.paged++
		} else {
			out.window++
		}
		for _, e := range batch.Entities {
			key := strings.TrimPrefix(e.Subject.CanonicalID, "repository:")
			if _, ok := r.projected[key]; !ok {
				r.projected[key] = r.tick
			}
		}
		r.cursor = batch.NextCursor
	}
	for _, line := range strings.Split(strings.TrimSpace(r.logs.String()), "\n") {
		var decoded map[string]any
		if json.Unmarshal([]byte(line), &decoded) == nil && strings.Contains(fmt.Sprint(decoded["msg"]), "overlap window pass continues") {
			out.yield = decoded
		}
		if _, aged := decoded["pass_age_seconds"]; aged {
			out.open = decoded
		}
	}
	return out
}

func (r *churnRig) advance() { r.now = r.now.Add(churnTick) }

func (r *churnRig) pass() windowPass { return r.plan.window.pass(windowScopeFor("org", 0)) }

func (r *churnRig) logTick(res churnTickResult) {
	p := r.pass()
	age := any("-")
	if res.yield != nil {
		age = res.yield["pass_age_seconds"]
	}
	r.t.Logf("tick %3d now=%s paged=%d window=%d yield=%v walking=%v low=%s walk=%s pass_age_s=%v rows=%d",
		r.tick, r.now.Format("15:04:05"), res.paged, res.window, res.yield != nil, p.walking, p.low.Format("15:04:05.000"), p.walk.Since.Format("15:04:05.000"), age, len(r.store.rows))
}

var churnStart = time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)

// A window of 3,000 rows (three ticks of walk at 5 pages x 200 rows) and a
// writer that lands 20 new rows beyond the frontier every tick. A late row
// lands inside the window, behind the walk: the next pass finds it.
func TestOverlapProjectsALateRowBehindTheWalkOfADeepWindow(t *testing.T) {
	r := newChurnRig(t, churnStart)
	r.land(3000, churnStart.Add(-20*time.Minute), churnStart.Add(-time.Minute))
	r.logTick(r.runTick())
	if len(r.projected) != 3000 {
		t.Fatalf("first drain projected %d rows", len(r.projected))
	}
	late, landed := "", 0
	for i := 0; i < 40; i++ {
		prev := r.now
		r.advance()
		r.land(20, prev, r.now.Add(-time.Second))
		if i == 1 {
			late, landed = r.landOne(churnStart.Add(-19*time.Minute)), r.tick+1
		}
		r.logTick(r.runTick())
	}
	at, ok := r.projected[late]
	if !ok || at > landed+4 {
		t.Fatalf("the late row inside the window: projected=%v at tick %d, landed at tick %d; want it within 4 ticks", ok, at, landed)
	}
}

// A late row stamped below the window's lower edge (more than overlap + slack
// behind the frontier) is outside the window: the re-read does not look for
// it. This is the documented bound of the window, pinned so that a change to
// it is made on purpose.
func TestALateRowBelowTheWindowEdgeIsOutsideTheWindow(t *testing.T) {
	r := newChurnRig(t, churnStart)
	r.land(3000, churnStart.Add(-20*time.Minute), churnStart.Add(-time.Minute))
	r.runTick()
	inside, below := "", ""
	for i := 0; i < 60; i++ {
		prev := r.now
		r.advance()
		r.land(20, prev, r.now.Add(-time.Second))
		if i == 1 {
			inside = r.landOne(churnStart.Add(-25 * time.Minute))
			below = r.landOne(churnStart.Add(-45 * time.Minute))
		}
		r.runTick()
	}
	if _, ok := r.projected[inside]; !ok {
		t.Fatalf("precondition: a late row 25 minutes behind the frontier (inside the window) was not projected")
	}
	if at, ok := r.projected[below]; ok {
		t.Fatalf("a row 45 minutes behind the frontier was projected at tick %d: the window is deeper than overlap + slack", at)
	}
}

// A writer that lands more rows per tick (1,200) than one tick of walk reads
// (5 pages x 200). A late row lands inside the window, behind the walk. It is
// reached only if the pass in progress ends, so that the next one starts.
func TestOverlapPassEndsUnderAWriterFasterThanTheWalk(t *testing.T) {
	r := newChurnRig(t, churnStart)
	r.land(3000, churnStart.Add(-20*time.Minute), churnStart.Add(-time.Minute))
	r.runTick()
	late, landed := "", 0
	open, oldest := 0, 0.0
	starts := map[time.Time]bool{}
	for i := 0; i < 100; i++ {
		prev := r.now
		r.advance()
		r.land(1200, prev, r.now.Add(-time.Second))
		if i == 1 {
			late, landed = r.landOne(churnStart.Add(-19*time.Minute)), r.tick+1
		}
		res := r.runTick()
		if res.open != nil {
			open++
			if age, _ := res.open["pass_age_seconds"].(float64); age > oldest {
				oldest = age
			}
		}
		if p := r.pass(); p.walking {
			starts[p.passStart] = true
		}
		if i < 6 || i%20 == 0 || i == 99 {
			r.logTick(res)
		}
	}
	at, ok := r.projected[late]
	t.Logf("late row: projected=%v at tick %d (landed at tick %d); ticks that ended with the pass open=%d; passes started=%d; oldest pass=%vs", ok, at, landed, open, len(starts), oldest)
	if !ok {
		t.Fatalf("the late row inside the window was never projected in 100 ticks (25 min): the pass in progress never ended (%d passes started, oldest %vs)", len(starts), oldest)
	}
	if at > landed+10 {
		t.Fatalf("the late row was projected at tick %d, %d ticks after it landed; want within 10", at, at-landed)
	}
	if len(starts) < 3 {
		t.Fatalf("%d passes started in 100 ticks; want the passes to keep ending and starting", len(starts))
	}
}
