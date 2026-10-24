package emitscan_test

import (
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

func TestMain(m *testing.M) {
	code := m.Run()
	emitscan.ShutdownHelper()
	os.Exit(code)
}
