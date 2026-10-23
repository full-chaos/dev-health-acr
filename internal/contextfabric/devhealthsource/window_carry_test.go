package devhealthsource

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// carryStore is a keyset table that counts the statements it answers.
type carryStore struct {
	rows       keysetRows
	statements int
	failFrom   int // when > 0, statement number failFrom and later fail
	onQuery    func(n int)
}

var errCarryStore = errors.New("carry store down")

func (s *carryStore) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	s.statements++
	if s.onQuery != nil {
		s.onQuery(s.statements)
	}
	if s.failFrom > 0 && s.statements >= s.failFrom {
		return nil, errCarryStore
	}
	return s.rows.Query(ctx, statement, bindings)
}

func carryTable(name string, store *carryStore, carry bool) entityTable {
	return entityTable{name: name, carry: carry, query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return fetch(ctx, store, "", rowLimitBindings(orgID, cursor, limit), limit, scanKeysetRow)
	}}
}

type carryTableSpec struct {
	name  string
	store *carryStore
	carry bool
}

var carryFrontier = cursorState{Since: pageCutStamp.Add(20 * time.Minute), After: "zz"}

func carryPlan(windowCarry bool, pagesPerCall int, specs ...carryTableSpec) sourcePlan {
	tables := make([]entityTable, len(specs))
	for i, spec := range specs {
		tables[i] = carryTable(spec.name, spec.store, spec.carry)
	}
	now := pageCutStamp.Add(21 * time.Minute)
	return sourcePlan{
		client: keysetRows{}, source: "window_carry_test", version: "v1", tables: tables,
		now: func() time.Time { return now }, overlap: 15 * time.Minute, window: newWindowMemo(), windowScope: windowScopeFor("org", 0),
		windowPagesPerCall: pagesPerCall, windowCarry: windowCarry, windowInterval: time.Hour,
	}
}

// markSeen records every row as emitted already, so the walk steps over its
// pages inside one call instead of ending the call at the first unseen row.
func markSeen(plan sourcePlan, specs ...carryTableSpec) {
	var seen []candidate
	for _, spec := range specs {
		for _, row := range spec.store.rows {
			seen = append(seen, candidate{table: spec.name, observedAt: row.at, sortKey: row.key})
		}
	}
	plan.window.record(plan.windowScope, seen, plan.overlap)
}

func spread(prefix string, n int, step, offset time.Duration) keysetRows {
	rows := make(keysetRows, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, keysetRow{at: pageCutStamp.Add(offset + time.Duration(i)*step), key: fmt.Sprintf("%s%04d", prefix, i)})
	}
	return rows
}

type windowStep struct {
	walkSince time.Time
	walkAfter string
	pages     int
	rows      int64
	walking   bool
}

func driveWindow(t *testing.T, plan sourcePlan) []windowStep {
	t.Helper()
	steps := driveCalls(t, plan, 400)
	if steps[len(steps)-1].walking {
		t.Fatal("the window pass did not end in 400 calls")
	}
	return steps
}

// driveCalls makes up to n calls, and stops early when a call ends the pass.
func driveCalls(t *testing.T, plan sourcePlan, n int) []windowStep {
	t.Helper()
	var steps []windowStep
	for call := 0; call < n; call++ {
		if _, available, err := plan.overlapBatch(context.Background(), "org", "cursor", carryFrontier); err != nil || available {
			t.Fatalf("call %d: available=%v err=%v", call, available, err)
		}
		pass := plan.window.pass(plan.windowScope)
		steps = append(steps, windowStep{pass.walk.Since, pass.walk.After, pass.pages, int64(pass.rows), pass.walking})
		if !pass.walking {
			return steps
		}
	}
	return steps
}

func seenKeys(plan sourcePlan) []string {
	var keys []string
	for key := range plan.window.scopes[plan.windowScope].seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneSpecs(specs []carryTableSpec) []carryTableSpec {
	out := make([]carryTableSpec, len(specs))
	for i, spec := range specs {
		out[i] = carryTableSpec{name: spec.name, store: &carryStore{rows: spec.store.rows}, carry: spec.carry}
	}
	return out
}

func standardCarrySpecs() []carryTableSpec {
	return []carryTableSpec{
		{"big", &carryStore{rows: spread("a", 700, time.Second, 0)}, true},
		{"mid", &carryStore{rows: spread("b", 30, 20*time.Second, 0)}, true},
		{"none", &carryStore{}, true},
	}
}

func TestWindowCarryServesNonTruncatedTablesFromMemory(t *testing.T) {
	off, on := cloneSpecs(standardCarrySpecs()), cloneSpecs(standardCarrySpecs())
	offPlan, onPlan := carryPlan(false, 40, off...), carryPlan(true, 40, on...)
	markSeen(offPlan, off...)
	markSeen(onPlan, on...)
	offSteps, onSteps := driveWindow(t, offPlan), driveWindow(t, onPlan)
	pages := onSteps[len(onSteps)-1].pages
	if pages < 4 || len(onSteps) != 1 {
		t.Fatalf("pass took %d pages in %d calls, want at least 4 pages in one call", pages, len(onSteps))
	}
	if !reflect.DeepEqual(offSteps, onSteps) {
		t.Fatalf("carry changed the walk: off %v on %v", offSteps, onSteps)
	}
	for i, spec := range on {
		if off[i].store.statements != pages {
			t.Fatalf("without carry table %s ran %d statements over %d pages", spec.name, off[i].store.statements, pages)
		}
	}
	if on[1].store.statements != 1 || on[2].store.statements != 1 {
		t.Fatalf("complete tables ran %d and %d statements, want 1 each", on[1].store.statements, on[2].store.statements)
	}
	if on[0].store.statements != pages {
		t.Fatalf("the truncated table ran %d statements over %d pages, want one per page", on[0].store.statements, pages)
	}
}

func TestWindowCarryReadsATableAgainWhileItIsTruncated(t *testing.T) {
	specs := []carryTableSpec{
		{"short", &carryStore{rows: spread("a", 450, 2*time.Second, 0)}, true},
		{"long", &carryStore{rows: spread("b", 1200, time.Second, 0)}, true},
	}
	plan := carryPlan(true, 40, specs...)
	markSeen(plan, specs...)
	steps := driveWindow(t, plan)
	pages := steps[len(steps)-1].pages
	// The short table is truncated for its first reads and complete from a
	// later one; the long table keeps the walk going, so pages follow that need
	// no statement for the short one.
	if got := specs[0].store.statements; got >= pages || got < 3 {
		t.Fatalf("short table ran %d statements over %d pages, want 3 or more and fewer than the pages", got, pages)
	}
	if got := specs[1].store.statements; got != pages {
		t.Fatalf("long table ran %d statements over %d pages, want one per page", got, pages)
	}
	reference := cloneSpecs(specs)
	refPlan := carryPlan(false, 40, reference...)
	markSeen(refPlan, reference...)
	if !reflect.DeepEqual(driveWindow(t, refPlan), steps) {
		t.Fatal("carry changed the walk of a table that turns complete part way")
	}
}

func TestWindowCarryDropsEverythingWhenANewPassStartsInTheSameCall(t *testing.T) {
	build := func(carry bool) ([]carryTableSpec, sourcePlan) {
		specs := cloneSpecs([]carryTableSpec{
			{"big", &carryStore{rows: spread("a", 1000, time.Second, 0)}, true},
			{"early", &carryStore{rows: spread("b", 20, 5*time.Second, 0)}, true},
		})
		plan := carryPlan(carry, 4, specs...)
		plan.windowInterval = 0
		markSeen(plan, specs...)
		return specs, plan
	}
	const calls = 6
	offSpecs, offPlan := build(false)
	onSpecs, onPlan := build(true)
	offSteps, onSteps := driveCalls(t, offPlan, calls), driveCalls(t, onPlan, calls)
	if !reflect.DeepEqual(offSteps, onSteps) {
		t.Fatalf("carry changed the walk across a pass restart:\noff %v\non  %v", offSteps, onSteps)
	}
	if !reflect.DeepEqual(seenKeys(offPlan), seenKeys(onPlan)) {
		t.Fatal("carry changed the rows the walk reached")
	}
	// The early table's rows all lie in the first page. A call that ends a
	// resumed pass and starts the next one reads them again, because the new
	// pass walks from the window's low edge, behind where the old one stood.
	// Without that reset a carried read from the old pass would hide them.
	if on, off := onSpecs[1].store.statements, offSpecs[1].store.statements; on >= off {
		t.Fatalf("early table ran %d statements with carry and %d without; carry should save some", on, off)
	}
	restarts := 0
	for _, step := range onSteps {
		if step.pages == 0 && step.walking {
			restarts++
		}
	}
	if restarts == 0 {
		t.Fatalf("no call restarted the pass: %v", onSteps)
	}
}

func TestWindowCarryIsNotKeptBetweenCalls(t *testing.T) {
	specs := cloneSpecs(standardCarrySpecs())
	plan := carryPlan(true, 1, specs...)
	markSeen(plan, specs...)
	steps := driveWindow(t, plan)
	calls := len(steps)
	if calls < 4 {
		t.Fatalf("only %d calls", calls)
	}
	if got := specs[1].store.statements; got != calls {
		t.Fatalf("complete table ran %d statements over %d one-page calls, want one per call", got, calls)
	}
}

func TestWindowCarryOffReadsEveryTableOnEveryPage(t *testing.T) {
	specs := cloneSpecs(standardCarrySpecs())
	plan := carryPlan(false, 40, specs...)
	markSeen(plan, specs...)
	pages := driveWindow(t, plan)[0].pages
	for _, spec := range specs {
		if spec.store.statements != pages {
			t.Fatalf("with carry off table %s ran %d statements over %d pages", spec.name, spec.store.statements, pages)
		}
	}
	mixed := cloneSpecs(standardCarrySpecs())
	mixed[1].carry = false
	mixedPlan := carryPlan(true, 40, mixed...)
	markSeen(mixedPlan, mixed...)
	pages = driveWindow(t, mixedPlan)[0].pages
	if mixed[1].store.statements != pages {
		t.Fatalf("a table that is not carry-eligible ran %d statements over %d pages", mixed[1].store.statements, pages)
	}
	if mixed[2].store.statements != 1 {
		t.Fatalf("an eligible complete table ran %d statements", mixed[2].store.statements)
	}
}

// A row of a complete table that lands ahead of the walk during the call is not
// read by that call; the pass after it reads it. Without carry the next page
// reads it.
func TestWindowCarryLeavesALateRowAheadOfTheWalkToTheNextPass(t *testing.T) {
	landed := keysetRow{at: pageCutStamp.Add(15 * time.Minute), key: "landed"}
	run := func(carry bool) (firstPass, secondPass []string) {
		specs := cloneSpecs(standardCarrySpecs())
		specs[0].store.onQuery = func(n int) {
			if n == 2 {
				specs[1].store.rows = append(append(keysetRows(nil), specs[1].store.rows...), landed)
			}
		}
		plan := carryPlan(carry, 40, specs...)
		plan.windowInterval = 0
		markSeen(plan, specs...)
		driveWindow(t, plan)
		hasLanded := func() bool {
			_, ok := plan.window.scopes[plan.windowScope].seen[rowMemoKey(candidate{table: "mid", observedAt: landed.at, sortKey: landed.key})]
			return ok
		}
		if hasLanded() {
			firstPass = []string{"landed"}
		}
		driveWindow(t, plan)
		if hasLanded() {
			secondPass = []string{"landed"}
		}
		return firstPass, secondPass
	}
	if first, second := run(false); len(first) != 1 || len(second) != 1 {
		t.Fatalf("without carry the late row is read in the first pass; got first=%v second=%v", first, second)
	}
	if first, second := run(true); len(first) != 0 || len(second) != 1 {
		t.Fatalf("with carry the late row waits for the next pass; got first=%v second=%v", first, second)
	}
}

func TestWindowCarryServesACopyAndHoldsACopy(t *testing.T) {
	carry := newTableCarry(true)
	table := entityTable{name: "t", carry: true}
	at := pageCutStamp
	input := []candidate{{observedAt: at, sortKey: "a"}, {observedAt: at, sortKey: "b"}, {observedAt: at, sortKey: "c"}}
	carry.note(table, cursorState{}, input, false)
	input[0].sortKey, input[2].sortKey = "x", "x"
	first, ok := carry.take(table, cursorState{Since: at, After: "a"})
	if !ok || len(first) != 2 || first[0].sortKey != "b" || first[1].sortKey != "c" {
		t.Fatalf("served %v ok=%v, want rows b and c", keysOf(first), ok)
	}
	sort.Slice(first, func(i, j int) bool { return first[i].sortKey > first[j].sortKey })
	first[0].sortKey = "y"
	second, ok := carry.take(table, cursorState{Since: at, After: "a"})
	if !ok || len(second) != 2 || second[0].sortKey != "b" || second[1].sortKey != "c" {
		t.Fatalf("a caller's change reached the held rows: %v", keysOf(second))
	}
}

func TestWindowCarryDeclinesAWalkBeforeTheRowsItHolds(t *testing.T) {
	carry := newTableCarry(true)
	table := entityTable{name: "t", carry: true}
	at := pageCutStamp
	carry.note(table, cursorState{Since: at, After: "m"}, []candidate{{observedAt: at, sortKey: "n"}}, false)
	if _, ok := carry.take(table, cursorState{Since: at.Add(-time.Second)}); ok {
		t.Fatal("served rows to a walk that stands before the cursor they were read from")
	}
	if _, ok := carry.take(table, cursorState{Since: at, After: "a"}); ok {
		t.Fatal("served rows to a walk on the same stamp with an earlier key than the read cursor")
	}
	if _, ok := carry.take(table, cursorState{Since: at, After: "m"}); !ok {
		t.Fatal("declined a walk at the read cursor")
	}
	carry.note(table, cursorState{Since: at, After: "m"}, nil, true)
	if _, ok := carry.take(table, cursorState{Since: at, After: "m"}); ok {
		t.Fatal("served a table whose last read was truncated")
	}
}

func TestWindowCarryReadErrorKeepsTheTableName(t *testing.T) {
	specs := cloneSpecs(standardCarrySpecs())
	specs[0].store.failFrom = 2
	plan := carryPlan(true, 40, specs...)
	markSeen(plan, specs...)
	_, available, err := plan.overlapBatch(context.Background(), "org", "cursor", carryFrontier)
	var readErr *tableReadError
	if available || !errors.As(err, &readErr) || readErr.table != "big" || !errors.Is(err, errCarryStore) {
		t.Fatalf("available=%v err=%v, want the read error of table big", available, err)
	}
}

func TestWindowCarryWalksLikeTheStatementsOnRandomTables(t *testing.T) {
	rng := rand.New(rand.NewSource(20261023))
	counts := []int{0, 3, 50, 199, 200, 201, 450}
	for trial := 0; trial < 150; trial++ {
		var specs []carryTableSpec
		for n := 1 + rng.Intn(4); n > 0; n-- {
			used := map[keysetRow]bool{}
			var rows keysetRows
			for count := counts[rng.Intn(len(counts))]; len(rows) < count; {
				row := keysetRow{at: pageCutStamp.Add(time.Duration(rng.Intn(40)) * 10 * time.Second), key: fmt.Sprintf("k%03d", rng.Intn(500))}
				if !used[row] {
					used[row] = true
					rows = append(rows, row)
				}
			}
			specs = append(specs, carryTableSpec{fmt.Sprintf("t%d", len(specs)), &carryStore{rows: rows}, rng.Intn(4) != 0})
		}
		pages := 1 + rng.Intn(6)
		seenShare := rng.Intn(3)
		offSpecs, onSpecs := cloneSpecs(specs), cloneSpecs(specs)
		offPlan, onPlan := carryPlan(false, pages, offSpecs...), carryPlan(true, pages, onSpecs...)
		if seenShare > 0 {
			markSeen(offPlan, offSpecs...)
			markSeen(onPlan, onSpecs...)
		}
		offSteps, onSteps := driveWindow(t, offPlan), driveWindow(t, onPlan)
		if !reflect.DeepEqual(offSteps, onSteps) || !reflect.DeepEqual(seenKeys(offPlan), seenKeys(onPlan)) {
			t.Fatalf("trial %d: carry changed the walk\noff %v\non  %v", trial, offSteps, onSteps)
		}
		for i := range onSpecs {
			if onSpecs[i].store.statements > offSpecs[i].store.statements {
				t.Fatalf("trial %d: table %s ran %d statements with carry, %d without", trial, onSpecs[i].name, onSpecs[i].store.statements, offSpecs[i].store.statements)
			}
			if !onSpecs[i].carry && onSpecs[i].store.statements != offSpecs[i].store.statements {
				t.Fatalf("trial %d: table %s is not carry-eligible and ran %d statements, %d without carry", trial, onSpecs[i].name, onSpecs[i].store.statements, offSpecs[i].store.statements)
			}
		}
	}
}

func TestWindowCarryFilterMatchesKeysetOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(7263))
	for trial := 0; trial < 300; trial++ {
		used := map[keysetRow]bool{}
		var rows keysetRows
		for count := rng.Intn(60); len(rows) < count; {
			row := keysetRow{at: pageCutStamp.Add(time.Duration(rng.Intn(6)) * time.Second), key: fmt.Sprintf("k%02d", rng.Intn(30))}
			if !used[row] {
				used[row] = true
				rows = append(rows, row)
			}
		}
		store := &carryStore{rows: rows}
		table := carryTable("t", store, true)
		pick := func() cursorState {
			return cursorState{Since: pageCutStamp.Add(time.Duration(rng.Intn(7)-1) * time.Second), After: fmt.Sprintf("k%02d", rng.Intn(31))}
		}
		c0, c1 := pick(), pick()
		if cursorBefore(c1, c0) {
			c0, c1 = c1, c0
		}
		limit := 10 + rng.Intn(80)
		read, truncated, err := readTable(context.Background(), table, store, "org", c0, limit)
		if err != nil || truncated {
			continue
		}
		carry := newTableCarry(true)
		carry.note(table, c0, read, false)
		got, ok := carry.take(table, c1)
		want, wantTruncated, err := readTable(context.Background(), table, store, "org", c1, 1000)
		if err != nil || wantTruncated || !ok {
			t.Fatalf("trial %d: err=%v truncated=%v served=%v", trial, err, wantTruncated, ok)
		}
		if !reflect.DeepEqual(keysOf(got), keysOf(want)) {
			t.Fatalf("trial %d: from %v to %v the carry holds\n  %v\nthe statement returns\n  %v", trial, c0, c1, keysOf(got), keysOf(want))
		}
	}
}

// carryVerdicts holds, per producer, whether the window walk may serve its next
// page from memory, and why not when it may not. The CI parity test
// (TestWindowCarryMatchesTheStatementOnTheRealSQL) must pass for every
// producer set true here.
var carryVerdicts = map[string]struct {
	carry  bool
	reason string
}{
	"teams":                       {true, ""},
	"projects":                    {true, ""},
	"project_membership_presence": {true, ""},
	"work_item_team_attributions": {true, ""},
	"team_project_ownership":      {true, ""},
	// devhealthschema:not-a-production-replica names producers of the registry to pin a verdict on each; no columns, engines or sort keys
	"team_repo_ownership": {true, ""},

	"repos":                                {false, "not read by the teams source window; unproven on the real SQL"},
	"work_items":                           {false, "not read by the teams source window; unproven on the real SQL"},
	"git_pull_requests":                    {false, "two-step wide read keyed by the page; unproven on the real SQL"},
	"deployments":                          {false, "not read by the teams source window; unproven on the real SQL"},
	"operational_incidents":                {false, "not read by the teams source window; unproven on the real SQL"},
	"work_item_dependencies":               {false, "tombstone dedupe state and the ended-link set are per statement"},
	"work_items_hierarchy":                 {false, "unmapped-parent counters are per statement; unproven on the real SQL"},
	"work_graph_issue_pr":                  {false, "the outranked-link tombstone is named once per statement"},
	"work_graph_deployment_incident_edges": {false, "not read by the teams source window; unproven on the real SQL"},
	"git_pull_request_reviews":             {false, "not read by the teams source window; unproven on the real SQL"},
	"ci_pipeline_runs":                     {false, "not read by the teams source window; unproven on the real SQL"},
}

func TestEveryProducerSaysWhetherTheWindowMayCarryIt(t *testing.T) {
	tables := append(append([]entityTable(nil), entityTables...), teamsProjectsTables(nil, nil, nil, nil)...)
	seen := map[string]bool{}
	for _, table := range tables {
		verdict, ok := carryVerdicts[table.name]
		if !ok {
			t.Fatalf("producer %s has no carry verdict; add it to carryVerdicts", table.name)
		}
		seen[table.name] = true
		if table.carry != verdict.carry {
			t.Errorf("producer %s: registry carry=%v, verdict %v", table.name, table.carry, verdict.carry)
		}
		if !verdict.carry && strings.TrimSpace(verdict.reason) == "" {
			t.Errorf("producer %s is not carried and says no reason", table.name)
		}
	}
	for name := range carryVerdicts {
		if !seen[name] {
			t.Errorf("carry verdict for %s names no producer", name)
		}
	}
}
