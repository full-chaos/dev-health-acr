package sidecar

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

const (
	// changedFileRecordMultiplier scales the raw-record read ceiling from
	// the caller's requested max, so rename/copy records (which consume
	// two raw records per changed path) and duplicate paths still leave
	// headroom to reach max distinct files.
	changedFileRecordMultiplier = 4
	// maxChangedFileRecordsReadCeiling is the absolute upper bound on raw
	// `git status -z` records ever read, regardless of the caller's
	// requested max. This is what makes the bound real: a caller cannot
	// defeat it by requesting an enormous MaxChangedFiles, and an
	// adversarial or enormous working tree cannot force an unbounded read
	// before truncation is applied.
	maxChangedFileRecordsReadCeiling = 4000
	// maxChangedFileRecordBytes bounds a single NUL-delimited status
	// record (a status code plus one or two paths), guarding against one
	// adversarially long path exhausting memory before a NUL is ever seen.
	maxChangedFileRecordBytes = 8192
)

// changedFilesResult is the outcome of a bounded changed-file read.
type changedFilesResult struct {
	files        []string
	truncated    bool
	stoppedEarly bool
}

// gitChangedFiles returns a bounded, deterministic (lexicographically
// sorted, de-duplicated) list of paths with staged, unstaged, or untracked
// changes, using `git status --porcelain=v1 -z` so filenames containing
// spaces or other unusual bytes are returned raw (unquoted, NUL-delimited)
// rather than shell-escaped. Reading stops at a bounded number of raw
// records (see maxChangedFileRecordsReadCeiling) before the full output is
// ever buffered, so an enormous or adversarial working tree cannot force an
// unbounded read; it reports whether the true change count exceeded max (or
// the record ceiling) and was truncated.
//
// The git binary is resolved via currentExecutableResolver (exec_resolver.go,
// shared with runGit) rather than a bare "git" argv0, and gitSafeConfigArgs
// (workspace_git.go) is applied here too so `git status` cannot trigger a
// repository-controlled core.fsmonitor hook.
func gitChangedFiles(ctx context.Context, root string, max int) ([]string, bool, error) {
	gitPath, err := currentExecutableResolver("git")
	if err != nil {
		return nil, false, err
	}
	args := append([]string{"-C", root}, gitSafeConfigArgs...)
	args = append(args, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Env = credentialSafeEnviron()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, fmt.Errorf("git status: %w", err)
	}
	stderr := newBoundedStderrWriter()
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		return nil, false, fmt.Errorf("git status: %w", err)
	}

	result, parseErr := parseChangedFilesStream(stdout, max)

	// The read is only known to have reached the child's true EOF — the
	// point at which it is guaranteed to no longer be blocked writing —
	// when parsing succeeded without abandoning the stream early. A
	// record-cap truncation (stoppedEarly) and a scan/parse error
	// (including a control-character rejection, which also stops reading
	// mid-stream) both mean the child may still be writing more than was
	// ever read, so it must be killed rather than waited on normally;
	// waiting on a child still blocked on a full pipe would otherwise risk
	// hanging indefinitely. Both signals are consulted — not just
	// result.stoppedEarly — because an error return from
	// parseChangedFilesStream carries a zero-value changedFilesResult,
	// which would otherwise silently discard a stoppedEarly=true that was
	// set before the error was produced.
	if result.stoppedEarly || parseErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	if parseErr != nil {
		return nil, false, parseErr
	}
	if !result.stoppedEarly && waitErr != nil {
		return nil, false, fmt.Errorf("git status: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return result.files, result.truncated, nil
}

// changedFilesRecordSplit is a bufio.SplitFunc that splits git's
// NUL-delimited `status --porcelain=v1 -z` output on NUL bytes.
func changedFilesRecordSplit(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// parseChangedFilesStream reads NUL-delimited `git status -z` records from
// r and returns a bounded, deterministic changedFilesResult. It never reads
// more than a fixed ceiling of raw records, independent of how much data r
// is willing to produce (see maxChangedFileRecordsReadCeiling), so bounding
// happens before allocation rather than being applied to an already fully
// buffered read. result.stoppedEarly reports whether r was abandoned before
// EOF; the caller must not then perform a normal blocking wait on whatever
// process is feeding r.
func parseChangedFilesStream(r io.Reader, max int) (changedFilesResult, error) {
	recordsCap := max * changedFileRecordMultiplier
	if recordsCap <= 0 || recordsCap > maxChangedFileRecordsReadCeiling {
		recordsCap = maxChangedFileRecordsReadCeiling
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), maxChangedFileRecordBytes)
	scanner.Split(changedFilesRecordSplit)

	seen := make(map[string]struct{})
	recordsRead := 0
	stoppedEarly := false

scanLoop:
	for scanner.Scan() {
		recordsRead++
		if recordsRead > recordsCap {
			stoppedEarly = true
			break scanLoop
		}
		rec := scanner.Bytes()
		if len(rec) < 4 {
			continue
		}
		indexStatus, worktreeStatus := rec[0], rec[1]
		path := string(rec[3:])
		if containsControlChar(path) {
			return changedFilesResult{}, fmt.Errorf("%w: changed file path", ErrControlCharacters)
		}
		seen[path] = struct{}{}
		// Rename/copy records are followed by a second NUL-separated
		// field holding the original path; consume and discard it. A
		// rename or copy can be reported in either the index column
		// (X, rec[0]) -- a staged rename/copy, e.g. after `git add`
		// following a `git mv` -- or the worktree column (Y, rec[1]) --
		// an unstaged rename/copy relative to the index, which `git
		// status` can report once a renamed path is no longer purely
		// untracked, for example after `mv old new && git add -N new`.
		// Porcelain v1 documents both "renamed/copied in index" (X) and
		// "renamed/copied in work tree" (Y) as valid shapes for the
		// second path field, so both columns must be checked here:
		// consulting only rec[0] silently desyncs the record stream the
		// first time an unstaged rename/copy record appears, corrupting
		// every record parsed afterward (the original-path field would
		// instead be read back in as if it were the next record's own
		// status+path line).
		if indexStatus == 'R' || indexStatus == 'C' || worktreeStatus == 'R' || worktreeStatus == 'C' {
			if !scanner.Scan() {
				// Either true EOF (the child's output ended mid-record,
				// which the unconditional scanner.Err() check below will
				// tell apart from a real error) or a genuine scan error.
				// Either way, this stream was not drained to a point we
				// can be certain about, so treat it the same as hitting
				// the record cap: the caller must not perform a normal
				// blocking wait on whatever process is feeding r.
				stoppedEarly = true
				break scanLoop
			}
			recordsRead++
			if recordsRead > recordsCap {
				stoppedEarly = true
				break scanLoop
			}
		}
	}
	// Checked unconditionally, regardless of why the loop above exited:
	// scanner.Err() only ever reports a non-nil error following a Scan()
	// call that itself returned false because of a genuine read/parse
	// failure (e.g. a single record exceeding maxChangedFileRecordBytes,
	// reported as bufio.ErrTooLong) — never merely because this function
	// chose to stop calling Scan() again after reaching recordsCap. A real
	// error here must not be silently swallowed just because stoppedEarly
	// was already set for an unrelated reason.
	if scanErr := scanner.Err(); scanErr != nil {
		return changedFilesResult{}, fmt.Errorf("read git status output: %w", scanErr)
	}

	sorted := make([]string, 0, len(seen))
	for f := range seen {
		sorted = append(sorted, f)
	}
	sort.Strings(sorted)

	truncated := stoppedEarly
	if max > 0 && len(sorted) > max {
		sorted = sorted[:max]
		truncated = true
	}
	return changedFilesResult{files: sorted, truncated: truncated, stoppedEarly: stoppedEarly}, nil
}
