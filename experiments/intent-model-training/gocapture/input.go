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
	"sort"
	"time"

	"github.com/full-chaos/dev-health-acr/experiments/intent-model-training/internal/interpreq"
	"golang.org/x/sys/unix"
)

// Python interface (spec §5). The sealing lane writes these files; this
// runner only reads them, except for appending its own exposure line.

type membershipRow struct {
	ExampleID     string `json:"example_id"`
	InputSHA256   string `json:"input_sha256"`
	RequestSHA256 string `json:"request_sha256"`
	RowID         string `json:"row_id"`
}

type sealRecord struct {
	Kind                        string          `json:"kind"`
	Schema                      string          `json:"schema"`
	Set                         string          `json:"set"`
	SetVersion                  json.RawMessage `json:"set_version"`
	SealDigest                  string          `json:"seal_digest"`
	MembershipDigest            string          `json:"membership_digest"`
	Membership                  []membershipRow `json:"membership"`
	ExpectedSystemMessageSHA256 string          `json:"expected_system_message_sha256"`
	EvaluationManifestSHA256    string          `json:"evaluation_manifest_sha256"`
	SealedAt                    string          `json:"sealed_at"`
}

type openingsRecord struct {
	Kind                     string   `json:"kind"`
	SessionID                string   `json:"session_id"`
	Set                      string   `json:"set,omitempty"`
	SealDigest               string   `json:"seal_digest,omitempty"`
	MembershipDigest         string   `json:"membership_digest,omitempty"`
	EvaluationManifestSHA256 string   `json:"evaluation_manifest_sha256,omitempty"`
	Phase                    string   `json:"phase,omitempty"`
	AuthorizedBy             string   `json:"authorized_by,omitempty"`
	AllowedReaders           []string `json:"allowed_readers,omitempty"`
	ReopenReason             string   `json:"reopen_reason,omitempty"`
	OpenedAt                 string   `json:"opened_at,omitempty"`
	Reader                   string   `json:"reader,omitempty"`
	Event                    string   `json:"event,omitempty"`
	RunID                    string   `json:"run_id,omitempty"`
	BinarySHA256             *string  `json:"binary_sha256,omitempty"`
	ClosedBy                 string   `json:"closed_by,omitempty"`
	At                       string   `json:"at,omitempty"`
}

type inputHeader struct {
	Kind                        string `json:"kind"`
	Schema                      string `json:"schema"`
	Set                         string `json:"set"`
	SealDigest                  string `json:"seal_digest"`
	MembershipDigest            string `json:"membership_digest"`
	SessionID                   string `json:"session_id"`
	ExpectedSystemMessageSHA256 string `json:"expected_system_message_sha256"`
	RowCount                    int    `json:"row_count"`
}

type inputRow struct {
	Kind          string          `json:"kind"`
	RowID         string          `json:"row_id"`
	ExampleID     string          `json:"example_id"`
	Request       json.RawMessage `json:"request"`
	RequestSHA256 string          `json:"request_sha256"`
	InputSHA256   string          `json:"input_sha256"`
}

// captureRow is a verified input row, ready to send.
type captureRow struct {
	RowID     string
	ExampleID string
	Rendered  interpreq.Rendered
}

type verifiedInput struct {
	Seal    sealRecord
	Session openingsRecord
	Rows    []captureRow
	Reopen  bool
	Digest  string
}

const (
	openingsName = "heldout-openings.jsonl"
	sealsName    = "seals.jsonl"
	inputName    = "capture-input.jsonl"
)

func membershipDigest(rows []membershipRow) (string, error) {
	sorted := append([]membershipRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RowID < sorted[j].RowID })
	return cjSHA(sorted)
}

func phaseFor(set string) (string, error) {
	switch set {
	case "H1":
		return "H1-decision", nil
	case "H2":
		return "H2-final", nil
	case "H3":
		// The Python side holds the same table (heldout.PHASES).
		return "H3-round3", nil
	case "H4":
		return "H4-cov2", nil
	}
	return "", fmt.Errorf("unknown held-out set %q", set)
}

func splitJSONLines(data []byte) [][]byte {
	var lines [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	return lines
}

func parseOpenings(data []byte) ([]openingsRecord, error) {
	var out []openingsRecord
	for i, line := range splitJSONLines(data) {
		var rec openingsRecord
		if err := strictUnmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("openings line %d: %w", i+1, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// sessionState finds the named session, whether it is open, and whether an
// earlier session already exposed the same seal (a reopening).
func sessionState(records []openingsRecord, sessionID string) (openingsRecord, bool, bool, error) {
	var session *openingsRecord
	closed := false
	exposedSeals := map[string]string{}
	sessionSeal := map[string]string{}
	for i, rec := range records {
		switch rec.Kind {
		case "evaluation_session":
			sessionSeal[rec.SessionID] = rec.SealDigest
			if rec.SessionID == sessionID {
				if session != nil {
					return openingsRecord{}, false, false, errors.New("session opened twice")
				}
				r := rec
				session = &r
			}
		case "exposure":
			if seal, ok := sessionSeal[rec.SessionID]; ok && rec.SessionID != sessionID {
				exposedSeals[seal] = rec.SessionID
			}
		case "session_closed":
			if rec.SessionID == sessionID {
				closed = true
			}
		default:
			return openingsRecord{}, false, false, fmt.Errorf("openings record %d: unknown kind %q", i+1, rec.Kind)
		}
	}
	if session == nil {
		return openingsRecord{}, false, false, fmt.Errorf("evaluation session %q not found", sessionID)
	}
	_, reopening := exposedSeals[session.SealDigest]
	return *session, !closed, reopening, nil
}

// checkSession applies every admission rule to the session.
func checkSession(session openingsRecord, open, reopening bool) error {
	if !open {
		return errors.New("evaluation session is closed")
	}
	if session.AuthorizedBy != "human:chris" {
		return errors.New("evaluation session was not opened by human:chris")
	}
	phase, err := phaseFor(session.Set)
	if err != nil {
		return err
	}
	if session.Phase != phase {
		return fmt.Errorf("set %s needs phase %s, session has %q", session.Set, phase, session.Phase)
	}
	allowed := false
	for _, r := range session.AllowedReaders {
		allowed = allowed || r == "capture"
	}
	if !allowed {
		return errors.New("session does not allow reader capture")
	}
	if reopening && session.ReopenReason == "" {
		return errors.New("this seal was already exposed in another session; a reopening needs chris's reopen_reason")
	}
	return nil
}

// openingsLock holds the flock on heldout-openings.lock: the lock the Python
// side (heldout.OPENINGS_LOCK) takes to open or close a session and to record
// its exposures, so admission and closing exclude each other (sol r1 M5).
type openingsLock struct {
	lock *os.File
	file *os.File
}

const openingsLockName = "heldout-openings.lock"

func lockOpenings(review *cdir) (*openingsLock, []openingsRecord, error) {
	fd, err := unix.Openat(review.fd, openingsLockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", openingsLockName, err)
	}
	lock := os.NewFile(uintptr(fd), openingsLockName)
	if err := lockFile(lock, 10*time.Second); err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	file, err := review.openFile(openingsName, unix.O_RDWR|unix.O_APPEND, false)
	if err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, 1<<40))
	if err == nil {
		var records []openingsRecord
		if records, err = parseOpenings(data); err == nil {
			return &openingsLock{lock: lock, file: file}, records, nil
		}
	}
	_ = file.Close()
	_ = lock.Close()
	return nil, nil, err
}

func (o *openingsLock) release() {
	_ = o.file.Close()
	_ = o.lock.Close()
}

func (o *openingsLock) append(rec openingsRecord) error {
	line, err := cj(rec)
	if err != nil {
		return err
	}
	if _, err := o.file.Write(append(line, '\n')); err != nil {
		return err
	}
	return o.file.Sync()
}

// admit re-validates the session under the openings lock and returns the
// held lock: a session cannot close while an invocation is admitted.
func admit(review *cdir, sessionID string) (*openingsLock, error) {
	lock, records, err := lockOpenings(review)
	if err != nil {
		return nil, err
	}
	session, open, reopening, err := sessionState(records, sessionID)
	if err == nil {
		err = checkSession(session, open, reopening)
	}
	if err != nil {
		lock.release()
		return nil, err
	}
	return lock, nil
}

// appendExposure validates the session and records the exposure in one
// locked transaction, fsync'd before any send.
func appendExposure(review *cdir, rec openingsRecord, now time.Time) error {
	lock, err := admit(review, rec.SessionID)
	if err != nil {
		return err
	}
	defer lock.release()
	rec.Kind, rec.Reader, rec.At = "exposure", "capture", now.UTC().Format(time.RFC3339Nano)
	return lock.append(rec)
}

func loadSeal(review *cdir, set, sealDigest string) (sealRecord, error) {
	data, err := review.readFile(sealsName)
	if err != nil {
		return sealRecord{}, err
	}
	var found *sealRecord
	var foundBytes []byte
	for _, line := range splitJSONLines(data) {
		var probe struct {
			Kind       string `json:"kind"`
			SealDigest string `json:"seal_digest"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return sealRecord{}, fmt.Errorf("seals.jsonl: %w", err)
		}
		if probe.Kind != "seal" || probe.SealDigest != sealDigest {
			continue
		}
		canon, err := cjRaw(line)
		if err != nil {
			return sealRecord{}, fmt.Errorf("seal record: %w", err)
		}
		if found != nil {
			if !bytes.Equal(canon, foundBytes) {
				return sealRecord{}, errors.New("two seal records share a seal_digest with different content")
			}
			continue
		}
		var rec sealRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return sealRecord{}, err
		}
		found, foundBytes = &rec, canon
	}
	if found == nil {
		return sealRecord{}, errors.New("no seal record for the session's seal_digest")
	}
	if found.Set != set {
		return sealRecord{}, errors.New("seal record set differs from the session set")
	}
	got, err := membershipDigest(found.Membership)
	if err != nil {
		return sealRecord{}, err
	}
	if got != found.MembershipDigest {
		return sealRecord{}, errors.New("seal membership_digest does not recompute")
	}
	return *found, nil
}

// verifyInput runs every §5 check before any write or call.
func verifyInput(root *cdir, sessionID string) (verifiedInput, error) {
	review, err := root.walk(false, "review")
	if err != nil {
		return verifiedInput{}, err
	}
	defer review.close()
	data, err := review.readFile(openingsName)
	if err != nil {
		return verifiedInput{}, err
	}
	records, err := parseOpenings(data)
	if err != nil {
		return verifiedInput{}, err
	}
	session, open, reopening, err := sessionState(records, sessionID)
	if err != nil {
		return verifiedInput{}, err
	}
	if err := checkSession(session, open, reopening); err != nil {
		return verifiedInput{}, err
	}
	if err := validName(session.Set); err != nil {
		return verifiedInput{}, err
	}
	seal, err := loadSeal(review, session.Set, session.SealDigest)
	if err != nil {
		return verifiedInput{}, err
	}
	if seal.MembershipDigest != session.MembershipDigest || seal.EvaluationManifestSHA256 != session.EvaluationManifestSHA256 {
		return verifiedInput{}, errors.New("session and seal disagree on membership or evaluation manifest")
	}
	setDir, err := root.walk(false, "heldout", session.Set)
	if err != nil {
		return verifiedInput{}, err
	}
	defer setDir.close()
	inputBytes, err := setDir.readFile(inputName)
	if err != nil {
		return verifiedInput{}, err
	}
	lines := splitJSONLines(inputBytes)
	if len(lines) == 0 {
		return verifiedInput{}, errors.New("capture-input is empty")
	}
	var header inputHeader
	if err := strictUnmarshal(lines[0], &header); err != nil || header.Kind != "header" || header.Schema != "intent-training.capture-input.v1" {
		return verifiedInput{}, fmt.Errorf("capture-input header: %v", err)
	}
	if header.Set != seal.Set || header.SealDigest != seal.SealDigest || header.MembershipDigest != seal.MembershipDigest ||
		header.SessionID != sessionID || header.ExpectedSystemMessageSHA256 != seal.ExpectedSystemMessageSHA256 || header.RowCount != len(lines)-1 {
		return verifiedInput{}, errors.New("capture-input header does not match the seal and session")
	}
	sealRows := make(map[string]membershipRow, len(seal.Membership))
	for _, r := range seal.Membership {
		sealRows[r.RowID] = r
	}
	seen := map[string]bool{}
	var rows []captureRow
	var observed []membershipRow
	for i, line := range lines[1:] {
		var row inputRow
		if err := strictUnmarshal(line, &row); err != nil || row.Kind != "row" {
			return verifiedInput{}, fmt.Errorf("capture-input row %d: malformed or carries an unknown key", i+1)
		}
		if seen[row.RowID] {
			return verifiedInput{}, fmt.Errorf("capture-input row %d: duplicate row_id", i+1)
		}
		seen[row.RowID] = true
		want, ok := sealRows[row.RowID]
		if !ok {
			return verifiedInput{}, fmt.Errorf("capture-input row %d: not in the seal", i+1)
		}
		if row.ExampleID != want.ExampleID {
			return verifiedInput{}, fmt.Errorf("capture-input row %d: example_id differs from the seal", i+1)
		}
		if row.RequestSHA256 != want.RequestSHA256 || row.InputSHA256 != want.InputSHA256 {
			return verifiedInput{}, fmt.Errorf("capture-input row %d: claimed hashes differ from the seal", i+1)
		}
		rendered, err := interpreq.Render(row.Request)
		if err != nil {
			return verifiedInput{}, fmt.Errorf("capture-input row %d: %w", i+1, err)
		}
		if rendered.Decoded.ValidateErr != nil {
			return verifiedInput{}, fmt.Errorf("capture-input row %d: request does not validate", i+1)
		}
		if rendered.RequestSHA256 != want.RequestSHA256 || rendered.InputSHA256 != want.InputSHA256 {
			return verifiedInput{}, fmt.Errorf("capture-input row %d: request renders differently from the seal", i+1)
		}
		rows = append(rows, captureRow{RowID: row.RowID, ExampleID: row.ExampleID, Rendered: rendered})
		observed = append(observed, membershipRow{ExampleID: row.ExampleID, InputSHA256: rendered.InputSHA256, RequestSHA256: rendered.RequestSHA256, RowID: row.RowID})
	}
	if len(seen) != len(sealRows) {
		return verifiedInput{}, errors.New("capture-input is missing sealed rows")
	}
	digest, err := membershipDigest(observed)
	if err != nil || digest != seal.MembershipDigest {
		return verifiedInput{}, errors.New("recomputed membership digest differs from the seal")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].RowID < rows[j].RowID })
	return verifiedInput{Seal: seal, Session: session, Rows: rows, Reopen: reopening, Digest: sha256Hex(inputBytes)}, nil
}
