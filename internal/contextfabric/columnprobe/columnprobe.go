// Package columnprobe is the ONE probe of whether a ClickHouse table already
// has columns that an ops migration adds. A statement that names a missing
// column fails, so a reader names the columns only after this answers yes.
//
// It is a leaf (it imports nothing from this module) so the fact readers
// (devhealthfacts) and the evidence catalog (contextpacket, which
// devhealthfacts imports and so cannot import back) share one probe type, one
// cache policy and one log line.
//
// A positive answer is final for the process; a negative one is asked again
// after the recheck window, so a roll of the ops migration is picked up
// without a restart. A failed probe keeps the last known answer; with none the
// answer is "no" for this call only and is not cached, so a flaky probe cannot
// pin either shape.
package columnprobe

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

// Recheck is how long a negative probe stands before ClickHouse is asked again.
const Recheck = time.Minute

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Counter runs the probe statement with names bound as {names:Array(String)}
// and returns the count it selected.
type Counter func(ctx context.Context, statement string, names []string) (uint64, error)

// Probe answers whether Table has every one of Columns. The zero value is
// usable once Configure has named the table and columns.
type Probe struct {
	mu         sync.Mutex
	name       string
	table      string
	columns    []string
	configured bool
	known      bool
	available  bool
	checkedAt  time.Time
}

// New returns a configured probe.
func New(name, table string, columns []string) *Probe {
	p := &Probe{}
	p.Configure(name, table, columns)
	return p
}

// Configure names the probe; the first call wins.
func (p *Probe) Configure(name, table string, columns []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.configured {
		return
	}
	p.name, p.table, p.columns, p.configured = name, table, append([]string(nil), columns...), true
}

// Statement is the probe statement for table.
func Statement(table string) string {
	return `SELECT count() FROM system.columns
WHERE database = currentDatabase() AND table = '` + table + `'
	AND has({names:Array(String)}, name)`
}

// Known reports whether any probe answer is cached.
func (p *Probe) Known() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.known
}

// Available is true when the table has every configured column.
func (p *Probe) Available(ctx context.Context, count Counter, now time.Time) bool {
	if p == nil || count == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.configured || !identifier.MatchString(p.table) {
		return false
	}
	if p.known && (p.available || now.Sub(p.checkedAt) < Recheck) {
		return p.available
	}
	n, err := count(ctx, Statement(p.table), p.columns)
	if err != nil {
		// The probe name and table are configured identifiers and the error is
		// named by its Go type only, so they ride in the message, not in
		// string attributes.
		slog.WarnContext(ctx, fmt.Sprintf("column_probe_failed: probe=%s table=%s cause_type=%T; the reader uses the older table shape for this call", p.name, p.table, err),
			"shape_known", p.known)
		return p.known && p.available
	}
	first, was := !p.known, p.available
	p.known, p.available, p.checkedAt = true, n == uint64(len(p.columns)), now
	if first || was != p.available {
		slog.InfoContext(ctx, fmt.Sprintf("column_probe_answered: probe=%s table=%s", p.name, p.table), "columns_present", p.available)
	}
	return p.available
}
