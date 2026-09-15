package devhealthfacts_test

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

// lockedLogBuffer preserves every standard-log write while the package's
// shared ClickHouse watcher may write to the same destination concurrently.
type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestDiagnosticStandardLogAfterRealShapeTestCleanup drives the two real
// provider tests as nested tests and checks that their cleanup restores the
// standard log destination and flags as well as the slog logger. It preserves
// any shared ClickHouse fixture initialized by an earlier package test.
func TestDiagnosticStandardLogAfterRealShapeTestCleanup(t *testing.T) {
	cases := []struct {
		name string
		run  func(*testing.T)
	}{
		{"investment", TestInvestmentProviderReportsSubjectIDShapeRejectionNotNoData},
		{"every_provider", TestEveryProviderDisclosesShapeRejectionAlongsideItsOwnSubjectKind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previousSharedQuery := sharedClickHouseQuery
			previousSharedConn := sharedClickHouseConn
			previousSharedAddr := sharedClickHouseAddr
			previousSharedErrSet := sharedClickHouseErr != nil
			previousSharedTerminateSet := sharedClickHouseTerminate != nil
			previousSlog := slog.Default()
			previousWriter := log.Writer()
			previousFlags := log.Flags()
			previousPrefix := log.Prefix()
			defer func() {
				slog.SetDefault(previousSlog)
				log.SetOutput(previousWriter)
				log.SetFlags(previousFlags)
				log.SetPrefix(previousPrefix)
				log.Printf("CF5755_LOG_PROOF_%s_RESTORED", tc.name)
				t.Logf("diagnostic_cleanup writer_restored=%t flags_restored=%t slog_restored=%t", log.Writer() == previousWriter, log.Flags() == previousFlags, slog.Default() == previousSlog)
			}()

			originalDestination := &lockedLogBuffer{}
			captureWriter := io.MultiWriter(os.Stderr, originalDestination)
			log.SetOutput(captureWriter)
			before := fmt.Sprintf("CF5755_LOG_PROOF_%s_BEFORE", tc.name)
			after := fmt.Sprintf("CF5755_LOG_PROOF_%s_AFTER", tc.name)
			log.Printf("%s", before)
			var actualHandler *recordingSlogHandler
			t.Run("real_test", func(t *testing.T) {
				tc.run(t)
				var ok bool
				actualHandler, ok = slog.Default().Handler().(*recordingSlogHandler)
				if !ok {
					t.Fatalf("actual test handler is %T", slog.Default().Handler())
				}
			})
			if actualHandler == nil {
				t.Fatal("actual test handler was not observed")
			}
			writerRestored := log.Writer() == captureWriter
			flagsRestored := log.Flags() == previousFlags
			slogRestored := slog.Default() == previousSlog
			if !writerRestored {
				t.Error("standard log writer was not restored by the nested test before diagnostic cleanup")
			}
			if !flagsRestored {
				t.Errorf("standard log flags = %d after nested test, want %d before diagnostic cleanup", log.Flags(), previousFlags)
			}
			if !slogRestored {
				t.Error("slog default was not restored by the nested test before diagnostic cleanup")
			}
			sharedFixturePreserved := sharedClickHouseQuery == previousSharedQuery &&
				sharedClickHouseConn == previousSharedConn &&
				sharedClickHouseAddr == previousSharedAddr &&
				(sharedClickHouseErr != nil) == previousSharedErrSet &&
				(sharedClickHouseTerminate != nil) == previousSharedTerminateSet
			if !sharedFixturePreserved {
				t.Error("nested test changed the shared ClickHouse fixture identity or lifecycle")
			}
			log.Printf("%s", after)
			capturedAfter := false
			for _, record := range actualHandler.snapshot() {
				if record.Message == after {
					capturedAfter = true
				}
			}
			beforeVisible := strings.Contains(originalDestination.String(), before)
			afterVisible := strings.Contains(originalDestination.String(), after)
			t.Logf("OBSERVATION before_at_original=%t after_at_original=%t after_in_real_test_handler=%t writer_restored=%t flags_restored=%t slog_restored=%t shared_fixture_preserved=%t writer_before=%T writer_after=%T flags_before=%d flags_after=%d", beforeVisible, afterVisible, capturedAfter, writerRestored, flagsRestored, slogRestored, sharedFixturePreserved, captureWriter, log.Writer(), previousFlags, log.Flags())
			if !beforeVisible {
				t.Fatal("measurement failed: before marker did not reach original destination")
			}
			if !afterVisible {
				t.Error("standard log marker after real test cleanup did not reach original destination")
			}
			if capturedAfter {
				t.Error("standard log marker after real test cleanup was retained by that test's in-memory handler")
			}
		})
	}
}
