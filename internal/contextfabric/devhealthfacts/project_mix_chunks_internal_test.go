package devhealthfacts

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"sort"
	"testing"
)

func chunkScope(ids ...string) projectMixScope {
	s := projectMixScope{}
	for i, id := range ids {
		s.unitIDs = append(s.unitIDs, id)
		s.versionsMs = append(s.versionsMs, int64(1000+i))
	}
	return s
}

// checkChunks asserts the partition: every pin of scope in exactly one chunk
// with its own version, every chunk non-empty and inside its bounds window, lo
// and hi its first and last id, chunks in key order.
func checkChunks(t *testing.T, scope projectMixScope, bounds []string, chunks []projectMixChunk) {
	t.Helper()
	want := map[string][]int64{}
	for i, id := range scope.unitIDs {
		want[id] = append(want[id], scope.versionsMs[i])
	}
	got := map[string][]int64{}
	sorted := slices.Clone(bounds)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	window := func(id string) int { // index of the chunk window id falls in
		return sort.Search(len(sorted), func(i int) bool { return sorted[i] > id })
	}
	chunkOf := map[string]int{}
	for c, chunk := range chunks {
		if len(chunk.pins.unitIDs) == 0 {
			t.Fatalf("chunk %d is empty", c)
		}
		if len(chunk.pins.versionsMs) != len(chunk.pins.unitIDs) {
			t.Fatalf("chunk %d: %d ids, %d versions", c, len(chunk.pins.unitIDs), len(chunk.pins.versionsMs))
		}
		if c > 0 && chunks[c-1].hi >= chunk.lo {
			t.Fatalf("chunk %d starts at %q, not after chunk %d's last id %q", c, chunk.lo, c-1, chunks[c-1].hi)
		}
		w := window(chunk.lo)
		for i, id := range chunk.pins.unitIDs {
			if id < chunk.lo || id > chunk.hi {
				t.Fatalf("chunk %d [%q, %q] holds %q", c, chunk.lo, chunk.hi, id)
			}
			if window(id) != w {
				t.Fatalf("chunk %d holds %q and %q from two bounds windows (bounds %q)", c, chunk.lo, id, sorted)
			}
			if prev, seen := chunkOf[id]; seen && prev != c {
				t.Fatalf("unit %q lands in chunk %d and chunk %d", id, prev, c)
			}
			chunkOf[id] = c
			got[id] = append(got[id], chunk.pins.versionsMs[i])
		}
		if !slices.Contains(chunk.pins.unitIDs, chunk.lo) || !slices.Contains(chunk.pins.unitIDs, chunk.hi) {
			t.Fatalf("chunk %d: lo %q / hi %q is not one of its pins", c, chunk.lo, chunk.hi)
		}
	}
	for id := range want {
		slices.Sort(want[id])
		slices.Sort(got[id])
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("pins in chunks %v, want %v", got, want)
	}
}

func TestSplitProjectMixScopePartitionsThePins(t *testing.T) {
	t.Parallel()
	// Byte order, the server's String order: upper case before lower case,
	// digits and '-' before letters, a multi-byte rune after ASCII.
	mixed := []string{"b-1", "Z9", "a", "0", "-x", "é", "A", "z", "a0", "B"}
	for _, c := range []struct {
		name   string
		scope  projectMixScope
		bounds []string
		chunks int
	}{
		{"no pins", chunkScope(), []string{"m"}, 0},
		{"no pins, no bounds", chunkScope(), nil, 0},
		{"one pin", chunkScope("q"), nil, 1},
		{"one pin, bounds around it", chunkScope("q"), []string{"a", "z"}, 1},
		{"no bounds: one chunk", chunkScope(mixed...), nil, 1},
		{"mixed ids, three windows", chunkScope(mixed...), []string{"B", "a0"}, 3},
		{"bound equal to a pin: the pin opens the next chunk", chunkScope("a", "b", "c", "d"), []string{"c"}, 2},
		{"bound below every pin", chunkScope("m", "n"), []string{"a"}, 1},
		{"bound above every pin", chunkScope("m", "n"), []string{"z"}, 1},
		{"empty windows between bounds are left out", chunkScope("a", "z"), []string{"c", "d", "e"}, 2},
		{"duplicate and unsorted bounds", chunkScope("a", "b", "c", "d", "e"), []string{"d", "b", "d", "b"}, 3},
		// The raw table holds several versions of the boundary id, so the bounds
		// statement can name it twice; it still opens exactly one chunk.
		{"duplicate id at a chunk boundary", chunkScope("a", "b", "c", "c", "d"), []string{"c", "c"}, 2},
		{"every pin its own chunk", chunkScope("a", "b", "c", "d"), []string{"b", "c", "d"}, 4},
		{"bound is the empty string", chunkScope("", "a"), []string{""}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			chunks := splitProjectMixScope(c.scope, c.bounds)
			if len(chunks) != c.chunks {
				t.Fatalf("%d chunks, want %d: %+v", len(chunks), c.chunks, chunks)
			}
			checkChunks(t, c.scope, c.bounds, chunks)
		})
	}
	t.Run("a duplicate pin id never splits", func(t *testing.T) {
		chunks := splitProjectMixScope(chunkScope("a", "c", "b", "c"), []string{"c"})
		if len(chunks) != 2 || !reflect.DeepEqual(chunks[1].pins.unitIDs, []string{"c", "c"}) {
			t.Fatalf("chunks %+v, want [a b] and [c c]", chunks)
		}
	})
	t.Run("random", func(t *testing.T) {
		r := rand.New(rand.NewSource(7316))
		for n := 0; n < 300; n++ {
			var ids, bounds []string
			for i := r.Intn(60); i > 0; i-- {
				ids = append(ids, fmt.Sprintf("%x", r.Intn(64)))
			}
			for i := r.Intn(8); i > 0; i-- {
				bounds = append(bounds, fmt.Sprintf("%x", r.Intn(64)))
			}
			scope := chunkScope(ids...)
			checkChunks(t, scope, bounds, splitProjectMixScope(scope, bounds))
		}
	})
}

// A chunk binds its own pins and its key range, never the whole scope.
func TestProjectMixChunkBindsItsOwnPinsAndRange(t *testing.T) {
	t.Parallel()
	chunks := splitProjectMixScope(chunkScope("a", "b", "c"), []string{"b"})
	got := map[string]any{}
	for _, b := range chunks[1].bindings() {
		got[b.Name] = b.Value
	}
	want := map[string]any{"pin_json": `[["b",1001],["c",1002]]`, "chunk_lo": "b", "chunk_hi": "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bindings %v, want %v", got, want)
	}
}

func TestFirstProjectKeysKeepsWhatOneStatementsLimitKept(t *testing.T) {
	t.Parallel()
	small := map[string]uint64{"b": 1, "a": 2}
	if got := firstProjectKeys(small); !reflect.DeepEqual(got, small) {
		t.Fatalf("under the limit: %v, want %v", got, small)
	}
	// One unchunked statement over units placed in projects keeps the first
	// maxProjectPopulationProbe project keys, each with its whole count. Chunked:
	// each chunk keeps its own first keys; the union is summed and cut.
	r := rand.New(rand.NewSource(13))
	const units, projects = 4000, 400
	unitProject := make([]string, units)
	for u := range unitProject {
		unitProject[u] = fmt.Sprintf("linear:p%03d", r.Intn(projects))
	}
	statement := func(from, to int) map[string]uint64 { // GROUP BY project ORDER BY project LIMIT probe
		all := map[string]uint64{}
		for u := from; u < to; u++ {
			all[unitProject[u]]++
		}
		keys := make([]string, 0, len(all))
		for k := range all {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if len(keys) > maxProjectPopulationProbe {
			keys = keys[:maxProjectPopulationProbe]
		}
		out := map[string]uint64{}
		for _, k := range keys {
			out[k] = all[k]
		}
		return out
	}
	want := statement(0, units)
	if len(want) != maxProjectPopulationProbe {
		t.Fatalf("fixture: the unchunked statement kept %d keys, want the limit %d", len(want), maxProjectPopulationProbe)
	}
	for _, size := range []int{1, 7, 333, 1000, units} {
		union := map[string]uint64{}
		for from := 0; from < units; from += size {
			for k, n := range statement(from, min(from+size, units)) {
				union[k] += n
			}
		}
		if got := firstProjectKeys(union); !reflect.DeepEqual(got, want) {
			t.Fatalf("chunks of %d units: the cut union differs from the unchunked statement (%d vs %d keys)", size, len(got), len(want))
		}
	}
}

func TestSortProjectNativePlacementOrdersByProviderProjectUnit(t *testing.T) {
	t.Parallel()
	unit := []string{"u2", "u1", "u3", "u1"}
	provider := []string{"linear", "linear", "jira", "linear"}
	project := []string{"p2", "p2", "p9", "p1"}
	multi := []uint8{0, 1, 0, 1}
	sortProjectNativePlacement(unit, provider, project, multi)
	got := fmt.Sprint(unit, provider, project, multi)
	want := fmt.Sprint([]string{"u3", "u1", "u1", "u2"}, []string{"jira", "linear", "linear", "linear"}, []string{"p9", "p1", "p2", "p2"}, []uint8{0, 1, 1, 0})
	if got != want {
		t.Fatalf("sorted %s, want %s", got, want)
	}
}
