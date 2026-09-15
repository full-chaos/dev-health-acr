package devhealthfacts_test

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// This temporary diagnostic invokes the existing baseline tests unchanged.
// It observes the logger while each real test is active, then its cleanup.
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
			if sharedClickHouseTerminate != nil {
				t.Fatal("unexpected shared container before diagnostic")
			}
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
				t.Logf("diagnostic_cleanup writer_restored=%t flags_restored=%t slog_restored=%t shared_container_nil=%t", log.Writer() == previousWriter, log.Flags() == previousFlags, slog.Default() == previousSlog, sharedClickHouseTerminate == nil)
			}()

			var originalDestination bytes.Buffer
			captureWriter := io.MultiWriter(os.Stderr, &originalDestination)
			log.SetOutput(captureWriter)
			before := fmt.Sprintf("CF5755_LOG_PROOF_%s_BEFORE", tc.name)
			after := fmt.Sprintf("CF5755_LOG_PROOF_%s_AFTER", tc.name)
			log.Printf("%s", before)
			var actualHandler *recordingSlogHandler
			t.Run("unchanged_existing_test", func(t *testing.T) {
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
			log.Printf("%s", after)
			capturedAfter := false
			for _, record := range actualHandler.snapshot() {
				if record.Message == after {
					capturedAfter = true
				}
			}
			beforeVisible := strings.Contains(originalDestination.String(), before)
			afterVisible := strings.Contains(originalDestination.String(), after)
			t.Logf("OBSERVATION before_at_original=%t after_at_original=%t after_in_real_test_handler=%t writer_before=%T writer_after=%T flags_before=%d flags_after=%d slog_restored=%t", beforeVisible, afterVisible, capturedAfter, captureWriter, log.Writer(), previousFlags, log.Flags(), slog.Default() == previousSlog)
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
