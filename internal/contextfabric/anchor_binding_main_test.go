package contextfabric

import (
	"fmt"
	"os"
	"testing"
)

// TestMain runs the package and then fails the run when any engine test made
// an Investigate Save with no anchor binding decision. The recording
// telemetry double collects those instead of stopping the binary, so a
// defect there still lets every test run and report its own result.
func TestMain(m *testing.M) {
	code := m.Run()
	if entries := takeUnrecordedInvestigateSaves(); len(entries) > 0 {
		for _, entry := range entries {
			fmt.Fprintln(os.Stderr, "anchor binding sweep:", entry)
		}
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
