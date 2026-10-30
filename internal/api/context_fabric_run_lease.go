package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The run lease on the investigation route (CHAOS-11949, plan §2.7.9,
// INV-7; the store side is internal/contextfabric/run_lease.go). With
// ACR_CONTEXT_FABRIC_RUN_LEASE_ENABLED on, a streamed run admitted under a
// client-named submission is ACR's:
//
//   - its record is admitted under this replica's lease (the admission
//     transaction writes it), and a holder loop renews it every
//     renewInterval and reads the record every cancelPoll for a cancel
//     requested on another replica;
//   - its context is detached from the request: a disconnect does not end
//     it, the stream writer marks the viewer gone and goes on logging;
//   - every event goes to the run's event log BEFORE it goes to the
//     connection, so a client that saw seq n can attach anywhere and find
//     every event after n;
//   - it ends on its terminal, its budget, an explicit cancel (cause
//     cancel_requested; terminal error{canceled}), a lost lease (cause
//     lease_lost; terminal error{run_lost}) or an event log that cannot be
//     written (cause event_log_failed). Each cause is on the cancel effective
//     line with t_eff, and with t_req for a cancel.

// ContextFabricRunLeaseLogMessage is the holder's line: a lease it lost, a
// check that failed, a cancel it saw.
const ContextFabricRunLeaseLogMessage = "context fabric investigation run lease"

// ContextFabricRunTakeoverLogMessage is the line of a lost run this replica
// took over.
const ContextFabricRunTakeoverLogMessage = "context fabric investigation run taken over"

// The causes a leased run's context is cancelled with.
var (
	errRunCancelRequested = errors.New("context fabric investigation run: canceled by its owner")
	errRunLeaseLost       = errors.New("context fabric investigation run: lease lost")
	errRunEventLogFailed  = errors.New("context fabric investigation run: event log unavailable")
)

// runLeaseSettings is the lease's timing and this replica's holder id.
type runLeaseSettings struct {
	holder        string
	ttl           time.Duration
	renewInterval time.Duration
	cancelPoll    time.Duration
	// attachPoll is how often an attach reads the log for new events.
	attachPoll time.Duration
	// keepalive is the silence after which an attach writes a comment.
	keepalive time.Duration
	// checkTimeout bounds one lease check.
	checkTimeout time.Duration
	maxEvents    int
	// appendAttempts and appendBackoff bound the retry of one log write.
	appendAttempts int
	appendBackoff  time.Duration
}

func defaultRunLeaseSettings(runtime *RuntimeDependencies) runLeaseSettings {
	holder := ""
	if runtime != nil {
		holder = strings.TrimSpace(runtime.RunHolderID)
	}
	if holder == "" {
		holder = NewRunHolderID()
	}
	return runLeaseSettings{
		holder:         holder,
		ttl:            contextfabric.RunLeaseTTL,
		renewInterval:  contextfabric.RunLeaseRenewInterval,
		cancelPoll:     contextfabric.RunCancelPollInterval,
		attachPoll:     500 * time.Millisecond,
		keepalive:      contractsv1.ContextFabricInvestigationEventHeartbeatInterval,
		checkTimeout:   2 * time.Second,
		maxEvents:      contextfabric.RunEventLogMaxEvents,
		appendAttempts: 3,
		appendBackoff:  50 * time.Millisecond,
	}
}

// NewRunHolderID names a replica as a run lease holder: its host name (the
// pod name) and 32 random bits, so a restarted pod with the same name is a
// new holder.
func NewRunHolderID() string {
	host, _ := os.Hostname()
	host = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return -1
	}, host)
	if len(host) > 150 {
		host = host[:150]
	}
	if host == "" {
		host = "acr-api"
	}
	buf := make([]byte, 4)
	//nolint:errcheck // crypto/rand.Read does not fail on a supported platform.
	rand.Read(buf)
	return host + "-" + hex.EncodeToString(buf)
}

func (a *App) investigationRuns() contextfabric.RunStore {
	if a.runtime == nil || storage.IsNil(a.runtime.InvestigationRuns) {
		return nil
	}
	return a.runtime.InvestigationRuns
}

// runLeaseServed reports whether this deployment runs leased runs.
func (a *App) runLeaseServed() bool {
	return a.config.ContextFabricRunLeaseEnabled && a.config.ContextFabricEventStreamEnabled && a.investigationRuns() != nil
}

// leasedRun is one run this replica holds.
type leasedRun struct {
	key     contextfabric.RunKey
	holder  string
	request string
	cancel  context.CancelCauseFunc

	mu   sync.Mutex
	tReq time.Time
}

// requestCancel cancels the run for its owner. The first t_req is kept.
func (l *leasedRun) requestCancel(tReq time.Time) {
	l.mu.Lock()
	if l.tReq.IsZero() {
		l.tReq = tReq.UTC()
	}
	l.mu.Unlock()
	l.cancel(errRunCancelRequested)
}

func (l *leasedRun) cancelRequestedAt() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tReq
}

// runRegistry holds this replica's leased runs.
type runRegistry struct {
	mu   sync.Mutex
	runs map[contextfabric.RunKey]*leasedRun
}

func newRunRegistry() *runRegistry {
	return &runRegistry{runs: make(map[contextfabric.RunKey]*leasedRun)}
}

func (r *runRegistry) add(run *leasedRun) (remove func()) {
	r.mu.Lock()
	r.runs[run.key] = run
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		if r.runs[run.key] == run {
			delete(r.runs, run.key)
		}
		r.mu.Unlock()
	}
}

// cancel cancels a run this replica holds; false when it holds none.
func (r *runRegistry) cancel(key contextfabric.RunKey, tReq time.Time) bool {
	r.mu.Lock()
	run, found := r.runs[key]
	r.mu.Unlock()
	if !found {
		return false
	}
	run.requestCancel(tReq)
	return true
}

// holdRunLease starts the holder loop of run. stop ends it and waits.
func (a *App) holdRunLease(logCtx context.Context, store contextfabric.RunStore, run *leasedRun) (stop func()) {
	done, stopped := make(chan struct{}), make(chan struct{})
	settings := a.runLease
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(settings.cancelPoll)
		defer ticker.Stop()
		lastRenewed, lastHeld := a.now(), a.now()
		cancelSeen := false
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			now := a.now()
			var renewUntil time.Time
			if now.Sub(lastRenewed) >= settings.renewInterval {
				renewUntil = now.Add(settings.ttl)
			}
			checkCtx, cancel := context.WithTimeout(logCtx, settings.checkTimeout)
			check, err := store.CheckRunLease(checkCtx, run.key, run.holder, renewUntil, now)
			cancel()
			if err != nil {
				a.logger.WarnContext(logCtx, ContextFabricRunLeaseLogMessage,
					"outcome", "check_failed", "request_id", contextfabric.SanitizeLogAttr(run.request),
					"reserved_result_id", contextfabric.SanitizeLogAttr(run.key.ReservedResultID), "error_class", submissionStoreErrorClass(err))
				// A holder that cannot renew for a whole term must assume it
				// lost the lease: another replica may take the run over.
				if now.Sub(lastHeld) >= settings.ttl {
					a.logRunLeaseLost(logCtx, run, "renewal_unreachable", "")
					run.cancel(errRunLeaseLost)
					return
				}
				continue
			}
			switch {
			case check.Held(run.holder):
			case check.Found && (check.State == contextfabric.SubmissionDone || check.State == contextfabric.SubmissionDoneUnsaved):
				// The run's own save (or unsaved write) made the record final:
				// nothing to hold any more, and nothing to cancel.
				return
			default:
				reason := "taken_over"
				if !check.Found {
					reason = "record_gone"
				} else if check.Holder == run.holder {
					reason = "record_final"
				}
				a.logRunLeaseLost(logCtx, run, reason, string(check.State))
				run.cancel(errRunLeaseLost)
				return
			}
			lastHeld = now
			if check.Renewed {
				lastRenewed = now
			}
			if !check.CancelRequestedAt.IsZero() && !cancelSeen {
				cancelSeen = true
				a.logger.InfoContext(logCtx, ContextFabricRunLeaseLogMessage,
					"outcome", "cancel_seen", "request_id", contextfabric.SanitizeLogAttr(run.request),
					"reserved_result_id", contextfabric.SanitizeLogAttr(run.key.ReservedResultID),
					"t_req", check.CancelRequestedAt.UTC().Format(time.RFC3339Nano))
				run.requestCancel(check.CancelRequestedAt)
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func (a *App) logRunLeaseLost(logCtx context.Context, run *leasedRun, reason, state string) {
	a.logger.WarnContext(logCtx, ContextFabricRunLeaseLogMessage,
		"outcome", "lease_lost", "reason", reason, "state", state,
		"request_id", contextfabric.SanitizeLogAttr(run.request),
		"reserved_result_id", contextfabric.SanitizeLogAttr(run.key.ReservedResultID),
		"holder", contextfabric.SanitizeLogAttr(run.holder))
}

// runEventLog is the stream writer's side of a leased run's log.
type runEventLog struct {
	store    contextfabric.RunStore
	run      contextfabric.RunKey
	settings runLeaseSettings
	now      func() time.Time
}

// append writes one row, retrying a transient failure. A seq refusal (the
// run already ended on another replica, or the row exists) is not retried.
func (l *runEventLog) append(ctx context.Context, event contextfabric.RunEvent) (bool, error) {
	var err error
	attempts := max(1, l.settings.appendAttempts)
	for attempt := 0; attempt < attempts; attempt++ {
		writeCtx, cancel := context.WithTimeout(ctx, contextfabric.SubmissionWriteTimeout)
		var appended bool
		appended, err = l.store.AppendRunEvent(writeCtx, l.run, event, l.settings.maxEvents, l.now())
		cancel()
		if err == nil || errors.Is(err, contextfabric.ErrRunEventSeq) {
			return appended, err
		}
		if attempt < attempts-1 {
			time.Sleep(l.settings.appendBackoff << attempt)
		}
	}
	return false, err
}

// runLostEnvelope is the terminal of a lost run.
func runLostEnvelope(requestID string) contractsv1.ErrorEnvelope {
	return contractsv1.ContextFabricRunTerminalEnvelope(requestID, contractsv1.ErrorCodeRunLost)
}

// TakeOverLostRuns takes over every running record whose lease ended, as
// this replica (the hosted sweeper calls it). It returns how many it took.
func (a *App) TakeOverLostRuns(ctx context.Context) (int, error) {
	store := a.investigationRuns()
	if store == nil {
		return 0, nil
	}
	return a.takeOverLostRuns(ctx, store, nil)
}

func (a *App) takeOverLostRuns(ctx context.Context, store contextfabric.RunStore, only *contextfabric.RunKey) (int, error) {
	lost, err := store.ReapLostRuns(ctx, a.runLease.holder, only, runLostEnvelope, a.now())
	for _, run := range lost {
		a.logger.WarnContext(ctx, ContextFabricRunTakeoverLogMessage,
			"org_id", contextfabric.SanitizeLogAttr(run.Run.OrgID),
			"request_id", contextfabric.SanitizeLogAttr(run.RequestID),
			"reserved_result_id", contextfabric.SanitizeLogAttr(run.Run.ReservedResultID),
			"previous_holder", contextfabric.SanitizeLogAttr(run.PreviousHolder),
			"taker", contextfabric.SanitizeLogAttr(a.runLease.holder),
			"lease_expired_at", run.LeaseExpiredAt.UTC().Format(time.RFC3339Nano),
			"terminal_seq", run.TerminalSeq, "terminal_appended", run.TerminalAppended)
	}
	if err != nil {
		a.logger.WarnContext(ctx, ContextFabricRunTakeoverLogMessage, "outcome", "failed", "error_class", submissionStoreErrorClass(err))
	}
	return len(lost), err
}
