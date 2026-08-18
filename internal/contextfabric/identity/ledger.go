package identity

import "sync"

// LedgerEntry is one whole-row omission: a natural key whose derived
// canonical id would exceed MaxNaturalKeyBytes was refused rather than
// truncated or silently accepted.
type LedgerEntry struct {
	Kind       string
	Segments   []string
	ByteLength int
}

// Ledger accumulates the natural keys Derive refuses to mint an id for, so
// design brief D10's ">256-byte natural keys: none live" stays a
// continuously monitored fact instead of a one-time snapshot claim (§5b,
// bound-omit ledger signal). It follows the same "count what you skip,
// never drop it silently" shape devhealthsource.logAmbiguousProjectKeys
// already uses for ambiguous project keys (teams_projects_edges.go) --
// same class of guard, not the same code.
//
// A nil *Ledger is valid: Derive treats it as "don't record" and simply
// omits the row, which is why every Derive call site takes *Ledger rather
// than requiring one.
type Ledger struct {
	mu      sync.Mutex
	entries []LedgerEntry
}

// Record appends one omission. Safe for concurrent use.
func (l *Ledger) Record(kind string, segments []string, byteLength int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := make([]string, len(segments))
	copy(cp, segments)
	l.entries = append(l.entries, LedgerEntry{Kind: kind, Segments: cp, ByteLength: byteLength})
}

// Entries returns a snapshot copy of every recorded omission, in the order
// they were recorded.
func (l *Ledger) Entries() []LedgerEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LedgerEntry, len(l.entries))
	copy(out, l.entries)
	return out
}

// CountByKind summarizes the ledger as one count per kind -- the shape the
// §5b bound-omit-ledger signal reports.
func (l *Ledger) CountByKind() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	counts := make(map[string]int, len(l.entries))
	for _, e := range l.entries {
		counts[e.Kind]++
	}
	return counts
}
