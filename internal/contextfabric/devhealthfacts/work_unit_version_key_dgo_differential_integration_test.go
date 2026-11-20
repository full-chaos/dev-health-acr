package devhealthfacts_test

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/full-chaos/dev-health-go/latestrow"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

type versionRow struct {
	group, id  uint32
	ms         int64
	block, off uint64
}

// maxComputedAtMillis is the last millisecond of DateTime64(3, 'UTC') (2299-12-31 23:59:59.999).
const maxComputedAtMillis = int64(10413791999999)

// versionRows is the seeded input of the differential cell: boundary values of
// every component in every combination, then random rows that draw each
// component from a small pool of shared values (so components tie) or from the
// whole domain, with exact duplicates of whole keys.
func versionRows() []versionRow {
	edgesMs := []int64{0, 1, 999, 1000, maxComputedAtMillis - 1, maxComputedAtMillis}
	edgesU64 := []uint64{0, 1, 2, 1 << 31, 1<<32 - 1, 1 << 32, 1<<32 + 1, 1 << 63, math.MaxUint64 - 1, math.MaxUint64}
	var rows []versionRow
	add := func(group uint32, ms int64, block, off uint64) {
		rows = append(rows, versionRow{group: group, id: uint32(len(rows)), ms: ms, block: block, off: off})
	}
	for _, ms := range edgesMs {
		for _, block := range edgesU64 {
			for _, off := range edgesU64 {
				add(0, ms, block, off)
			}
		}
	}
	rng := rand.New(rand.NewSource(14320))
	poolMs := make([]int64, 6)
	for i := range poolMs {
		poolMs[i] = rng.Int63n(maxComputedAtMillis + 1)
	}
	draw64 := func(pool []uint64) uint64 {
		switch rng.Intn(3) {
		case 0:
			return pool[rng.Intn(len(pool))]
		case 1:
			return uint64(rng.Int63n(1 << uint(1+rng.Intn(62))))
		default:
			return rng.Uint64()
		}
	}
	poolBlock := []uint64{3, 7, 1 << 20, 1 << 40, math.MaxUint64}
	poolOff := []uint64{0, 5, 1 << 33, math.MaxUint64}
	for len(rows) < 12000 {
		group := uint32(1 + rng.Intn(500))
		ms := poolMs[rng.Intn(len(poolMs))]
		if rng.Intn(3) == 0 {
			ms = rng.Int63n(maxComputedAtMillis + 1)
		}
		block, off := draw64(poolBlock), draw64(poolOff)
		add(group, ms, block, off)
		if rng.Intn(10) == 0 {
			add(group, ms, block, off)
		}
	}
	return rows
}

// acr's UInt256 ordinal and dev-health-go's latestrow key are two spellings of
// one order. Both run over the same rows (the subquery is named
// work_unit_investments, so the columns of both texts resolve), and the cell
// asserts the same total order (dense_rank of every row) and the same picked row
// of every group. latestrow is imported, not copied.
func TestWorkUnitVersionKeyIsTheSameOrderAsTheDgoLatestRowKeyOnSeededRowsAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, direct := newCHAOS3780IntegrationClient(t, ctx)
	rows := versionRows()
	if len(rows) < 10000 {
		t.Fatalf("%d rows, want at least 10000", len(rows))
	}
	if err := direct.Exec(ctx, `CREATE TABLE version_rows (g UInt32, id UInt32, ms Int64, block UInt64, off UInt64) ENGINE = Memory`); err != nil {
		t.Fatalf("create version_rows: %v", err)
	}
	batch, err := direct.PrepareBatch(ctx, `INSERT INTO version_rows`)
	if err != nil {
		t.Fatalf("prepare batch: %v", err)
	}
	for _, r := range rows {
		if err := batch.Append(r.group, r.id, r.ms, r.block, r.off); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send: %v", err)
	}
	// A part name is all_<min>_<max>_<level>: the key reads the max block.
	source := `(SELECT g, id, ms, block, off,
		fromUnixTimestamp64Milli(ms, 'UTC') AS computed_at,
		concat('all_', toString(intDiv(block, 2)), '_', toString(block), '_0') AS _part,
		off AS _part_offset
	FROM version_rows) AS work_unit_investments`

	ranks := func(key string) map[uint32]uint64 {
		t.Helper()
		out, err := direct.Query(ctx, `SELECT id, dense_rank() OVER (ORDER BY `+key+`) AS r FROM `+source+` ORDER BY id`)
		if err != nil {
			t.Fatalf("rank by %.60s: %v", key, err)
		}
		defer out.Close()
		got := map[uint32]uint64{}
		for out.Next() {
			var id uint32
			var r uint64
			if err := out.Scan(&id, &r); err != nil {
				t.Fatalf("scan rank: %v", err)
			}
			got[id] = r
		}
		if err := out.Err(); err != nil {
			t.Fatalf("rank rows: %v", err)
		}
		return got
	}
	acrRanks, dgoRanks := ranks(devhealthfacts.WorkUnitVersionKey), ranks(latestrow.OrderKey)
	if len(acrRanks) != len(rows) || len(dgoRanks) != len(rows) {
		t.Fatalf("ranked %d (acr) and %d (dgo) rows, want %d", len(acrRanks), len(dgoRanks), len(rows))
	}
	distinct := map[uint64]bool{}
	bad := 0
	for _, r := range rows {
		distinct[dgoRanks[r.id]] = true
		if acrRanks[r.id] != dgoRanks[r.id] {
			if bad++; bad <= 5 {
				t.Errorf("row %d (ms %d, block %d, offset %d): acr rank %d, dgo rank %d", r.id, r.ms, r.block, r.off, acrRanks[r.id], dgoRanks[r.id])
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d rows rank differently", bad, len(rows))
	}
	if len(distinct) < len(rows)/2 || len(distinct) == len(rows) {
		t.Fatalf("control: %d distinct versions of %d rows; the input needs many versions and some equal ones", len(distinct), len(rows))
	}

	picks := func(argMax string) map[uint32]string {
		t.Helper()
		out, err := direct.Query(ctx, `SELECT g, toString(`+argMax+`) FROM `+source+` GROUP BY g ORDER BY g`)
		if err != nil {
			t.Fatalf("pick %.60s: %v", argMax, err)
		}
		defer out.Close()
		got := map[uint32]string{}
		for out.Next() {
			var g uint32
			var p string
			if err := out.Scan(&g, &p); err != nil {
				t.Fatalf("scan pick: %v", err)
			}
			got[g] = p
		}
		if err := out.Err(); err != nil {
			t.Fatalf("pick rows: %v", err)
		}
		return got
	}
	// The picked row's own key components identify it, so a tie of whole keys (the
	// same version twice) cannot make two correct picks differ.
	payload := "tuple(work_unit_investments.ms, work_unit_investments.block, work_unit_investments.off)"
	acrPicks := picks("argMax(" + payload + ", " + devhealthfacts.WorkUnitVersionKey + ")")
	dgoPicks := picks(latestrow.ArgMax(payload))
	best := map[uint32]versionRow{}
	for _, r := range rows {
		if b, ok := best[r.group]; !ok || r.ms > b.ms || r.ms == b.ms && (r.block > b.block || r.block == b.block && r.off > b.off) {
			best[r.group] = r
		}
	}
	if len(acrPicks) != len(best) || len(dgoPicks) != len(best) {
		t.Fatalf("picked %d (acr) and %d (dgo) groups, want %d", len(acrPicks), len(dgoPicks), len(best))
	}
	for g, b := range best {
		want := fmt.Sprintf("(%d,%d,%d)", b.ms, b.block, b.off)
		if acrPicks[g] != want || dgoPicks[g] != want {
			t.Errorf("group %d: acr picks %s, dgo picks %s, want %s", g, acrPicks[g], dgoPicks[g], want)
		}
	}
}
