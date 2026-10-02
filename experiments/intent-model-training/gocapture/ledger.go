//go:build unix

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// The approval ledger (spec §7) is ONE file for the whole approval: every
// run, both sets and every restart spend from it. It is held under an
// exclusive non-blocking flock for the whole command. It is also the
// durable state machine: reservations precede every send, and every stop
// condition is rebuilt from it before anything is sent (sol r1 H2, H3).

const hardApprovalCap = 720

const ledgerName = "incumbent-approval-ledger.jsonl"

type ledgerRecord struct {
	Kind                     string `json:"kind"`
	ApprovalID               string `json:"approval_id"`
	CapHTTPAttempts          *int   `json:"cap_http_attempts,omitempty"`
	ApprovedBy               string `json:"approved_by,omitempty"`
	Source                   string `json:"source,omitempty"`
	RunID                    string `json:"run_id,omitempty"`
	Set                      string `json:"set,omitempty"`
	SealDigest               string `json:"seal_digest,omitempty"`
	RunConfigSHA256          string `json:"run_config_sha256,omitempty"`
	RunCapHTTPAttempts       *int   `json:"run_cap_http_attempts,omitempty"`
	RowID                    string `json:"row_id,omitempty"`
	Replicate                *int   `json:"replicate,omitempty"`
	RecoveryIndex            *int   `json:"recovery_index,omitempty"`
	AttemptSeq               *int   `json:"attempt_seq,omitempty"`
	Draw                     *int   `json:"draw,omitempty"`
	ExpectedDescriptorSHA256 string `json:"expected_descriptor_sha256,omitempty"`
	Outcome                  string `json:"outcome,omitempty"`
	ProductionReturned       *bool  `json:"production_returned,omitempty"`
	ArtifactSHA256           string `json:"artifact_sha256,omitempty"`
	ArtifactName             string `json:"artifact_name,omitempty"`
	Recovered                *bool  `json:"recovered,omitempty"`
	AuthorizedBy             string `json:"authorized_by,omitempty"`
	Reason                   string `json:"reason,omitempty"`
	// Prompt-variant runs only (variant.go). Both are absent from every
	// incumbent record, so incumbent ledger lines are unchanged.
	Variant        string `json:"variant,omitempty"`
	AppendixSHA256 string `json:"appendix_sha256,omitempty"`
	// Replace-mode variants only: "replace" on variant_approval and
	// run_started. Absent for the append mode, so its lines are unchanged.
	VariantMode string `json:"variant_mode,omitempty"`
	At          string `json:"at"`
}

type pairKey struct {
	SealDigest string
	RowID      string
	Replicate  int
}

type recoveryState struct {
	Reserved  bool
	Terminal  *ledgerRecord
	Uncertain bool
}

type pairState struct {
	Reserved       bool
	ReservedRun    string
	Terminal       *ledgerRecord
	Uncertain      bool
	Authorizations int
	Recoveries     map[int]*recoveryState
}

// recoveryTerminal returns the one recovery outcome, if any.
func (st *pairState) recoveryTerminal() *ledgerRecord {
	for i := 1; i <= len(st.Recoveries); i++ {
		if r := st.Recoveries[i]; r != nil && r.Terminal != nil {
			return r.Terminal
		}
	}
	return nil
}

type approvalLedger struct {
	file       *os.File
	approvalID string
	cap        int
	approved   bool
	spent      int
	runSpent   map[string]int
	runs       map[string]ledgerRecord
	stopped    map[string]string
	pairs      map[pairKey]*pairState
	// Prompt-variant series (variant.go): the pairs of each approved
	// variant, kept apart from the incumbent's pairs, and each approved
	// variant's appendix sha256. All spend from the same approval cap.
	variantPairs map[string]map[pairKey]*pairState
	variants     map[string]string
	// variantModes holds each approved variant's mode ("" is append).
	variantModes   map[string]string
	terminalOrder  []ledgerRecord
	lastReservedAt time.Time
	nextSeq        int
	now            func() time.Time
	failAppend     func() error
}

// openLedger opens the ledger inside the confined heldout directory.
func openLedger(dir *cdir, approvalID string, create bool) (*approvalLedger, error) {
	if approvalID == "" {
		return nil, errors.New("approval id is required")
	}
	if err := validName(approvalID); err != nil {
		return nil, err
	}
	file, err := dir.openFile(ledgerName, unix.O_RDWR, create)
	if err != nil {
		return nil, fmt.Errorf("approval ledger: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("approval ledger already in use: %w", err)
	}
	l := &approvalLedger{file: file, approvalID: approvalID, runSpent: map[string]int{}, runs: map[string]ledgerRecord{},
		stopped: map[string]string{}, pairs: map[pairKey]*pairState{}, nextSeq: 1, now: time.Now,
		variantPairs: map[string]map[pairKey]*pairState{}, variants: map[string]string{}, variantModes: map[string]string{}}
	if err := l.load(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return l, nil
}

func (l *approvalLedger) close() { _ = l.file.Close() }

// load replays the ledger. A torn final line (no newline) was never
// fsync'd, so nothing was sent on it: it is kept aside and truncated.
func (l *approvalLedger) load() error {
	if _, err := l.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(l.file)
	if err != nil {
		return err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		cut := bytes.LastIndexByte(data, '\n') + 1
		aside := l.file.Name() + ".torn-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := os.WriteFile(aside, data[cut:], 0o600); err != nil {
			return fmt.Errorf("quarantine torn ledger line: %w", err)
		}
		if err := l.file.Truncate(int64(cut)); err != nil {
			return err
		}
		if err := l.file.Sync(); err != nil {
			return err
		}
		data = data[:cut]
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		var rec ledgerRecord
		if err := strictUnmarshal(scanner.Bytes(), &rec); err != nil {
			return fmt.Errorf("approval ledger line %d: %w", line, err)
		}
		// The runner never writes an empty variant_mode member (the append
		// mode writes none). A decoded record cannot show an empty member, so
		// the line itself is checked.
		if rec.ApprovalID == l.approvalID && rec.VariantMode == "" && hasMember(scanner.Bytes(), "variant_mode") {
			return fmt.Errorf("approval ledger line %d: an empty variant_mode member", line)
		}
		if err := l.apply(rec); err != nil {
			return fmt.Errorf("approval ledger line %d: %w", line, err)
		}
	}
	return scanner.Err()
}

// hasMember reports whether the JSON object in line has the member name.
func hasMember(line []byte, name string) bool {
	var members map[string]json.RawMessage
	if json.Unmarshal(line, &members) != nil {
		return false
	}
	_, ok := members[name]
	return ok
}

// pairsOf is the pair map of one series: the incumbent's ("") or one
// prompt variant's. A variant never shares a pair with the incumbent.
func (l *approvalLedger) pairsOf(variant string) map[pairKey]*pairState {
	if variant == "" {
		return l.pairs
	}
	return l.variantPairs[variant]
}

func (l *approvalLedger) pair(variant string, k pairKey) *pairState {
	pairs := l.pairs
	if variant != "" {
		pairs = l.variantPairs[variant]
		if pairs == nil {
			pairs = map[pairKey]*pairState{}
			l.variantPairs[variant] = pairs
		}
	}
	st, ok := pairs[k]
	if !ok {
		st = &pairState{Recoveries: map[int]*recoveryState{}}
		pairs[k] = st
	}
	return st
}

// runVariantAgrees: a record of a started run belongs to that run's series.
func (l *approvalLedger) runVariantAgrees(rec ledgerRecord) error {
	if run, ok := l.runs[rec.RunID]; ok && run.Variant != rec.Variant {
		return errors.New("record series differs from its run (incumbent and prompt-variant records never mix)")
	}
	return nil
}

func keyOf(rec ledgerRecord) (pairKey, error) {
	if rec.SealDigest == "" || rec.RowID == "" || rec.Replicate == nil {
		return pairKey{}, errors.New("record lacks its pair key")
	}
	return pairKey{SealDigest: rec.SealDigest, RowID: rec.RowID, Replicate: *rec.Replicate}, nil
}

func (l *approvalLedger) apply(rec ledgerRecord) error {
	if rec.ApprovalID != l.approvalID {
		return nil
	}
	if rec.VariantMode != "" && rec.Kind != "variant_approval" && rec.Kind != "run_started" {
		return errors.New("a variant mode belongs on variant_approval and run_started records only")
	}
	switch rec.Kind {
	case "approval":
		if rec.CapHTTPAttempts == nil || *rec.CapHTTPAttempts < 1 || rec.ApprovedBy != "human:chris" {
			return errors.New("malformed approval record")
		}
		l.cap = *rec.CapHTTPAttempts
		l.approved = true
		return nil
	case "variant_approval":
		// chris's approval of one named prompt variant with one appendix
		// (gocapture approve-variant). Its calls spend from this approval.
		if rec.ApprovedBy != "human:chris" || validVariantName(rec.Variant) != nil || !isSHA256Hex(rec.AppendixSHA256) || rec.Source == "" || !validVariantMode(rec.VariantMode) {
			return errors.New("malformed variant_approval record")
		}
		if !l.approved {
			return errors.New("variant_approval before the approval record")
		}
		if prior, ok := l.variants[rec.Variant]; ok && prior != rec.AppendixSHA256 {
			return errors.New("this variant name is approved for another appendix: a changed appendix needs a new variant name")
		}
		if prior, ok := l.variantModes[rec.Variant]; ok && prior != rec.VariantMode {
			return errors.New("this variant name is approved in the other mode (append or replace): another mode needs a new variant name")
		}
		l.variants[rec.Variant], l.variantModes[rec.Variant] = rec.AppendixSHA256, rec.VariantMode
		return nil
	case "run_started":
		if rec.RunID == "" || rec.RunCapHTTPAttempts == nil || rec.RunConfigSHA256 == "" {
			return errors.New("malformed run_started record")
		}
		if prior, ok := l.runs[rec.RunID]; ok && prior.RunConfigSHA256 != rec.RunConfigSHA256 {
			return errors.New("run_started repeated with a different config")
		}
		if rec.Variant != "" || rec.AppendixSHA256 != "" || rec.VariantMode != "" {
			if approved, ok := l.variants[rec.Variant]; !ok || approved != rec.AppendixSHA256 || l.variantModes[rec.Variant] != rec.VariantMode {
				return errors.New("prompt-variant run without chris's variant_approval for this variant and appendix")
			}
		}
		l.runs[rec.RunID] = rec
		return nil
	case "run_stopped":
		if rec.RunID == "" || rec.Reason == "" {
			return errors.New("malformed run_stopped record")
		}
		if err := l.runVariantAgrees(rec); err != nil {
			return err
		}
		l.stopped[rec.RunID] = rec.Reason
		return nil
	case "http_reserved":
		if rec.AttemptSeq == nil || *rec.AttemptSeq != l.nextSeq {
			return errors.New("http reservation out of sequence")
		}
		if err := l.runVariantAgrees(rec); err != nil {
			return err
		}
		l.nextSeq++
		l.spent++
		l.runSpent[rec.RunID]++
		if at, err := time.Parse(time.RFC3339Nano, rec.At); err == nil && at.After(l.lastReservedAt) {
			l.lastReservedAt = at
		}
		return nil
	}
	k, err := keyOf(rec)
	if err != nil {
		return err
	}
	if err := l.runVariantAgrees(rec); err != nil {
		return err
	}
	if rec.Variant != "" {
		if _, ok := l.variants[rec.Variant]; !ok {
			return errors.New("record names a prompt variant that has no variant_approval")
		}
	}
	st := l.pair(rec.Variant, k)
	switch rec.Kind {
	case "pair_reserved":
		if st.Reserved {
			return errors.New("pair reserved twice")
		}
		st.Reserved, st.ReservedRun = true, rec.RunID
	case "pair_terminal":
		if rec.Recovered != nil && *rec.Recovered {
			rs := st.Recoveries[indexOf(rec)]
			if rs == nil || !rs.Reserved || rs.Terminal != nil || rs.Uncertain {
				return errors.New("recovery terminal without its open reservation")
			}
			copyRec := rec
			rs.Terminal = &copyRec
			return nil
		}
		if !st.Reserved || st.Terminal != nil || st.Uncertain {
			return errors.New("terminal without an open pair reservation")
		}
		copyRec := rec
		st.Terminal = &copyRec
		l.terminalOrder = append(l.terminalOrder, copyRec)
	case "pair_uncertain":
		if !st.Reserved || st.Terminal != nil || st.Uncertain {
			return errors.New("uncertain without an open pair reservation")
		}
		st.Uncertain = true
	case "recovery_authorized":
		if !st.Uncertain || rec.AuthorizedBy != "human:chris" || rec.Reason == "" || rec.RecoveryIndex == nil {
			return errors.New("recovery must be authorized by chris, with a reason and an index, for an uncertain pair")
		}
		if st.recoveryTerminal() != nil {
			return errors.New("the pair already has a recovery outcome")
		}
		if *rec.RecoveryIndex != st.Authorizations+1 {
			return errors.New("recovery index out of sequence")
		}
		for _, rs := range st.Recoveries {
			if rs.Reserved && rs.Terminal == nil && !rs.Uncertain {
				return errors.New("an earlier recovery is unreconciled")
			}
		}
		st.Authorizations++
	case "recovery_reserved":
		i := indexOf(rec)
		if i != st.Authorizations || st.Recoveries[i] != nil {
			return errors.New("recovery reserved without its authorization")
		}
		st.Recoveries[i] = &recoveryState{Reserved: true}
	case "recovery_uncertain":
		rs := st.Recoveries[indexOf(rec)]
		if rs == nil || !rs.Reserved || rs.Terminal != nil || rs.Uncertain {
			return errors.New("recovery uncertain without its open reservation")
		}
		rs.Uncertain = true
	default:
		return fmt.Errorf("unknown record kind %q", rec.Kind)
	}
	return nil
}

func indexOf(rec ledgerRecord) int {
	if rec.RecoveryIndex == nil {
		return 0
	}
	return *rec.RecoveryIndex
}

// append writes one record and fsyncs it before returning. The in-memory
// state changes only after the write succeeds.
func (l *approvalLedger) append(rec ledgerRecord) error {
	rec.ApprovalID = l.approvalID
	rec.At = l.now().UTC().Format(time.RFC3339Nano)
	line, err := cj(rec)
	if err != nil {
		return err
	}
	if l.failAppend != nil {
		if err := l.failAppend(); err != nil {
			return err
		}
	}
	if err := l.dryApply(rec); err != nil {
		return err
	}
	if _, err := l.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if _, err := l.file.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := l.file.Sync(); err != nil {
		return err
	}
	return l.apply(rec)
}

// dryApply checks a record against the rules without changing state.
func (l *approvalLedger) dryApply(rec ledgerRecord) error {
	shadow := &approvalLedger{approvalID: l.approvalID, cap: l.cap, approved: l.approved, spent: l.spent, nextSeq: l.nextSeq,
		runSpent: map[string]int{}, runs: map[string]ledgerRecord{}, stopped: map[string]string{}, pairs: map[pairKey]*pairState{},
		variantPairs: map[string]map[pairKey]*pairState{}, variants: map[string]string{}, variantModes: map[string]string{}}
	for k, v := range l.variantModes {
		shadow.variantModes[k] = v
	}
	for k, v := range l.runs {
		shadow.runs[k] = v
	}
	for k, v := range l.variants {
		shadow.variants[k] = v
	}
	if k, err := keyOf(rec); err == nil {
		if st, ok := l.pairsOf(rec.Variant)[k]; ok {
			clone := *st
			clone.Recoveries = map[int]*recoveryState{}
			for i, r := range st.Recoveries {
				c := *r
				clone.Recoveries[i] = &c
			}
			if rec.Variant == "" {
				shadow.pairs[k] = &clone
			} else {
				shadow.variantPairs[rec.Variant] = map[pairKey]*pairState{k: &clone}
			}
		}
	}
	return shadow.apply(rec)
}

var errBudget = errors.New("http attempt budget exhausted")

// reserveHTTP checks BOTH caps under the lock, then durably reserves one
// attempt. Nothing is sent without it (spec §7).
func (l *approvalLedger) reserveHTTP(k pairKey, runID string, draw int, expectedSHA string) (int, error) {
	if !l.approved {
		return 0, errors.New("no approval record")
	}
	run, ok := l.runs[runID]
	if !ok {
		return 0, errors.New("run not started")
	}
	if l.spent >= l.cap || l.runSpent[runID] >= *run.RunCapHTTPAttempts {
		return 0, errBudget
	}
	seq := l.nextSeq
	rep, d := k.Replicate, draw
	// The series is the run's own: a variant run's attempts carry its name.
	rec := ledgerRecord{Kind: "http_reserved", RunID: runID, SealDigest: k.SealDigest, RowID: k.RowID, Replicate: &rep, AttemptSeq: &seq, Draw: &d, ExpectedDescriptorSHA256: expectedSHA, Variant: run.Variant}
	if err := l.append(rec); err != nil {
		return 0, err
	}
	return seq, nil
}

func (l *approvalLedger) remaining() int { return l.cap - l.spent }

// stopReason rebuilds a run's stop state from durable records (sol r1 H3):
// a recorded stop, any runner-owned terminal, or two consecutive
// model_unavailable terminals in ledger order.
func (l *approvalLedger) stopReason(runID string) string {
	if r := l.stopped[runID]; r != "" {
		return r
	}
	streak := 0
	for _, t := range l.terminalOrder {
		if t.RunID != runID {
			continue
		}
		if runnerOwnedOutcome(t.Outcome) {
			return t.Outcome
		}
		if t.Outcome == "model_unavailable" {
			streak++
			if streak >= 2 {
				return "consecutive_model_unavailable"
			}
		} else {
			streak = 0
		}
	}
	return ""
}

// approve appends an approval line: chris's human action.
func approve(dataRoot, approvalID string, cap int, source, approvedBy, raiseReason string) error {
	if approvedBy != "human:chris" {
		return errors.New("an approval is written by human:chris only")
	}
	if cap < 1 {
		return errors.New("cap must be positive")
	}
	if cap > hardApprovalCap && raiseReason == "" {
		return fmt.Errorf("a cap above %d needs --raise-reason", hardApprovalCap)
	}
	if source == "" {
		return errors.New("--source (the approval citation) is required")
	}
	root, err := openRoot(dataRoot)
	if err != nil {
		return err
	}
	defer root.close()
	dir, err := root.walk(true, "heldout")
	if err != nil {
		return err
	}
	defer dir.close()
	l, err := openLedger(dir, approvalID, true)
	if err != nil {
		return err
	}
	defer l.close()
	if l.approved && cap < l.spent {
		return errors.New("a cap below the spending already made is refused")
	}
	reason := source
	if raiseReason != "" {
		reason = source + " | raise: " + raiseReason
	}
	c := cap
	return l.append(ledgerRecord{Kind: "approval", CapHTTPAttempts: &c, ApprovedBy: approvedBy, Source: reason})
}

// approveVariant appends chris's approval of one named prompt variant and
// its appendix (by sha256) under an existing approval. A variant run is
// refused without it, and its HTTP attempts spend from that approval's cap.
func approveVariant(dataRoot, approvalID, name, appendixPath, source, approvedBy string) error {
	return approveVariantFile(dataRoot, approvalID, name, appendixPath, "", source, approvedBy)
}

// approveVariantFile is approveVariant for either mode: appendixPath for
// the append mode, messagePath for the replace mode (a candidate system
// message). The record binds the name, the mode and the file's sha256.
func approveVariantFile(dataRoot, approvalID, name, appendixPath, messagePath, source, approvedBy string) error {
	if approvedBy != "human:chris" {
		return errors.New("a variant approval is written by human:chris only")
	}
	if source == "" {
		return errors.New("--source (the approval citation) is required")
	}
	variant, err := loadVariant(name, appendixPath, messagePath)
	if err != nil {
		return err
	}
	root, err := openRoot(dataRoot)
	if err != nil {
		return err
	}
	defer root.close()
	dir, err := root.walk(false, "heldout")
	if err != nil {
		return err
	}
	defer dir.close()
	l, err := openLedger(dir, approvalID, false)
	if err != nil {
		return err
	}
	defer l.close()
	if !l.approved {
		return errors.New("no approval record for this approval id: a variant is approved under an existing approval")
	}
	if approved, ok := l.variants[variant.Name]; ok && approved == variant.AppendixSHA256 && l.variantModes[variant.Name] == variant.Mode {
		return nil // already approved with this file in this mode
	}
	return l.append(ledgerRecord{Kind: "variant_approval", Variant: variant.Name, AppendixSHA256: variant.AppendixSHA256, VariantMode: variant.Mode, ApprovedBy: approvedBy, Source: source})
}
