package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// recordGenReader is a synthetic, never-EOF io.Reader that generates
// NUL-delimited `git status -z`-shaped records on demand. It never
// precomputes a large buffer itself, so it proves whether the consumer
// bounds its own reading rather than proving anything about a
// pre-allocated fixture size.
type recordGenReader struct {
	counter   int
	buf       []byte
	totalRead int
}

func (g *recordGenReader) Read(p []byte) (int, error) {
	for len(g.buf) == 0 {
		g.buf = append(g.buf, []byte("M  f"+strconv.Itoa(g.counter)+"\x00")...)
		g.counter++
	}
	n := copy(p, g.buf)
	g.buf = g.buf[n:]
	g.totalRead += n
	return n, nil
}

// TestParseChangedFilesStream_BoundsRawRecordsRead proves the changed-file
// bound is applied during reading, not after a full buffer is materialized:
// against a source that never reaches EOF, parseChangedFilesStream must
// still return promptly, truncated, having consumed only a small, fixed
// multiple of the requested max rather than attempting to drain the
// (infinite) source.
func TestParseChangedFilesStream_BoundsRawRecordsRead(t *testing.T) {
	gen := &recordGenReader{}
	type outcome struct {
		result changedFilesResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := parseChangedFilesStream(gen, 10)
		done <- outcome{result, err}
	}()

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("parseChangedFilesStream: %v", out.err)
		}
		if !out.result.truncated {
			t.Fatal("expected truncation against an unbounded source")
		}
		if !out.result.stoppedEarly {
			t.Fatal("expected stoppedEarly=true; the source never reaches EOF")
		}
		if len(out.result.files) != 10 {
			t.Fatalf("len(files) = %d, want 10", len(out.result.files))
		}
		if gen.totalRead > 1<<20 {
			t.Fatalf("read %d bytes from an unbounded source; the record bound was not applied before allocation", gen.totalRead)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parseChangedFilesStream did not return; it likely tried to drain an unbounded reader")
	}
}

func TestParseChangedFilesStream_DoesNotTruncateBelowTheBound(t *testing.T) {
	gen := &recordGenReader{}
	result, err := parseChangedFilesStream(gen, 4000)
	if err != nil {
		t.Fatalf("parseChangedFilesStream: %v", err)
	}
	if !result.truncated {
		t.Fatal("expected truncation even at max=4000 against an unbounded source (the record-read ceiling still applies)")
	}
	if len(result.files) != 4000 {
		t.Fatalf("len(files) = %d, want 4000", len(result.files))
	}
}

// TestGitChangedFiles_CanceledContext exercises long_commands/cancellation
// specifically against the streaming status implementation: an
// already-canceled context must abort promptly with context.Canceled.
func TestGitChangedFiles_CanceledContext(t *testing.T) {
	repo := newTestRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := gitChangedFiles(ctx, repo, DefaultMaxChangedFiles)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestGitChangedFiles_NeverEOFRealChildDoesNotHang proves the
// record-cap-truncation kill-before-wait path against a real child
// process and a real OS pipe, not just the synthetic in-process reader
// TestParseChangedFilesStream_BoundsRawRecordsRead already covers: a fake
// `git` that writes valid NUL-delimited records forever must still cause
// gitChangedFiles to return promptly, truncated, with a nil error —
// proving both that the child is killed rather than hung on (it never
// reaches EOF on its own) and that the resulting kill-induced process
// termination is not misreported as a spurious command failure.
func TestGitChangedFiles_NeverEOFRealChildDoesNotHang(t *testing.T) {
	// newFakeGitBinary injects the fake script only into the resolver seam,
	// which newTestRepo's own git setup (runGitCmdT) never consults, so
	// ordering relative to newTestRepo no longer affects correctness.
	repo := newTestRepo(t)
	newFakeGitBinary(t, `
i=0
while true; do
  printf 'M  f%d\0' "$i"
  i=$((i+1))
done
`)

	done := make(chan struct {
		files     []string
		truncated bool
		err       error
	}, 1)
	start := time.Now()
	go func() {
		files, truncated, err := gitChangedFiles(context.Background(), repo, 10)
		done <- struct {
			files     []string
			truncated bool
			err       error
		}{files, truncated, err}
	}()

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("gitChangedFiles: %v", out.err)
		}
		if !out.truncated {
			t.Fatal("expected truncation against a never-EOF child")
		}
		if len(out.files) != 10 {
			t.Fatalf("len(files) = %d, want 10", len(out.files))
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("gitChangedFiles took too long against a never-EOF child: %s", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("gitChangedFiles did not return; the never-EOF child was likely waited on instead of killed")
	}
}

// TestGitChangedFiles_OversizedRecordKillsChildRatherThanHangs is the
// direct regression test for the Oracle-flagged deadlock: a scanner/parse
// error (here, a single record exceeding maxChangedFileRecordBytes,
// reported by bufio.Scanner as ErrTooLong) must still cause the child to
// be killed before Wait, exactly like a record-cap truncation does. The
// fake `git` writes one oversized record and then keeps writing valid
// records forever, so if gitChangedFiles fails to kill it (relying only
// on the discarded stoppedEarly field of the zero-value error-path
// result, as the pre-fix code did), the real child remains blocked on a
// full pipe and cmd.Wait() hangs indefinitely.
func TestGitChangedFiles_OversizedRecordKillsChildRatherThanHangs(t *testing.T) {
	repo := newTestRepo(t)
	newFakeGitBinary(t, `
big=$(head -c 8300 /dev/zero | tr '\0' 'a')
printf 'M  %s\0' "$big"
i=0
while true; do
  printf 'M  f%d\0' "$i"
  i=$((i+1))
done
`)

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, _, err := gitChangedFiles(context.Background(), repo, DefaultMaxChangedFiles)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error from an oversized record")
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("gitChangedFiles took too long against an oversized-record child: %s", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("gitChangedFiles did not return; the scanner error did not trigger a kill before Wait")
	}
}

// TestGitChangedFiles_StderrIsBounded proves stderr capture on the
// `git status` streaming path is bounded independent of the stdout bound:
// a fake `git` that writes far more than maxGitStderrBytes to stderr and
// exits non-zero with no stdout must still produce an error whose text
// stays close to the bound.
func TestGitChangedFiles_StderrIsBounded(t *testing.T) {
	repo := newTestRepo(t)
	newFakeGitBinary(t, `
head -c 200000 /dev/zero | tr '\0' 'e' >&2
exit 1
`)

	_, _, err := gitChangedFiles(context.Background(), repo, DefaultMaxChangedFiles)
	if err == nil {
		t.Fatal("expected an error from a non-zero-exit child")
	}
	if got := len(err.Error()); got > maxGitStderrBytes+256 {
		t.Fatalf("gitChangedFiles error text was %d bytes; stderr capture was not bounded: %v", got, err)
	}
}

// TestGitChangedFiles_DisablesCoreFsmonitor is the gitChangedFiles-specific
// canary for the core.fsmonitor `-c` override, mirroring
// TestRunGit_DisablesCoreFsmonitor: `git status` (the command
// gitChangedFiles always issues) is exactly the invocation core.fsmonitor
// hooks would otherwise affect, so this proves the override reaches this
// command construction independently of runGit's own.
func TestGitChangedFiles_DisablesCoreFsmonitor(t *testing.T) {
	repo := newTestRepo(t)
	newFakeGitBinary(t, `
case "$*" in
  *"core.fsmonitor=false"*) ;;
  *) echo "MISSING core.fsmonitor=false override: $*" >&2; exit 1 ;;
esac
`)
	_, _, err := gitChangedFiles(context.Background(), repo, DefaultMaxChangedFiles)
	if err != nil {
		t.Fatalf("gitChangedFiles: %v", err)
	}
}

// TestDiscoverWorkspace_ChangedFiles_BoundedAgainstManyRealFiles is the
// end-to-end proof, against a real Git process and a real working tree,
// that a working tree with more untracked files than the record-read
// ceiling still returns bounded, deterministic output in bounded time
// rather than hanging or exhausting memory.
func TestDiscoverWorkspace_ChangedFiles_BoundedAgainstManyRealFiles(t *testing.T) {
	repo := newTestRepo(t)
	// The default max is DefaultMaxChangedFiles(200); the raw-record read
	// ceiling for that request is DefaultMaxChangedFiles*changedFileRecordMultiplier
	// (800). fileCount comfortably exceeds that to force stoppedEarly.
	const fileCount = DefaultMaxChangedFiles*changedFileRecordMultiplier + 100
	for i := range fileCount {
		name := "many" + strconv.Itoa(i) + ".txt"
		if err := os.WriteFile(filepath.Join(repo, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
		ExplicitRepoPath:    repo,
		IncludeChangedFiles: true,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("discovery against a large working tree took too long: %s", elapsed)
	}
	if len(info.ChangedFiles) != DefaultMaxChangedFiles {
		t.Fatalf("len(ChangedFiles) = %d, want the default max %d", len(info.ChangedFiles), DefaultMaxChangedFiles)
	}
	if !info.ChangedFilesTruncated {
		t.Fatal("expected ChangedFilesTruncated=true against a working tree larger than the bound")
	}
	if !sortedStrings(info.ChangedFiles) {
		t.Fatalf("ChangedFiles must remain deterministically sorted even when bounded, got %v", info.ChangedFiles)
	}
}

// TestGitChangedFiles_UnstagedRenameParsesBothPathsWithoutDesync is the
// real-git regression test for the porcelain X/Y rename-detection bug:
// `git status` can report a rename/copy in the *worktree* status column
// (Y, rec[1], the second status byte) as well as the index column (X,
// rec[0]) this package's rename handling covered before this fix. `mv
// old new && git add -N new` is the real, reproducible way to produce
// that shape -- confirmed against a real git binary, `git status
// --porcelain` prints " R README.md -> RENAMED.md" (a leading space in
// the X column, R in the Y column) for exactly this sequence. Before this
// fix, the parser checked only rec[0] for 'R'/'C', so it never consumed
// the second NUL-delimited original-path field for this record shape:
// the next scanner.Scan() then read that original-path field itself
// ("README.md") as if it were an unrelated new record, corrupting every
// record after it -- concretely, it drops the real "sentinel.txt" entry
// entirely and fabricates a bogus "DME.md" entry from a byte-offset into
// "README.md". A working fix returns exactly the two real changed paths,
// sorted, with nothing fabricated or dropped.
func TestGitChangedFiles_UnstagedRenameParsesBothPathsWithoutDesync(t *testing.T) {
	repo := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "sentinel.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repo, "README.md"), filepath.Join(repo, "RENAMED.md")); err != nil {
		t.Fatal(err)
	}
	runGitCmdT(t, repo, "add", "-N", "RENAMED.md")

	files, truncated, err := gitChangedFiles(context.Background(), repo, DefaultMaxChangedFiles)
	if err != nil {
		t.Fatalf("gitChangedFiles: %v", err)
	}
	if truncated {
		t.Fatal("did not expect truncation for a two-file working tree")
	}
	want := []string{"RENAMED.md", "sentinel.txt"}
	if len(files) != len(want) {
		t.Fatalf("got %v, want %v", files, want)
	}
	for i, f := range want {
		if files[i] != f {
			t.Fatalf("got %v, want %v", files, want)
		}
	}
}
