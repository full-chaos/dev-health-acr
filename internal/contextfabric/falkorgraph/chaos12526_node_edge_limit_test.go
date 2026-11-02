package falkorgraph

import (
	"strings"
	"testing"
)

// CHAOS-12526: a limit the server cannot read past makes a cut look complete, and a
// negative one panics on read. Config is where both stop.
func TestANodeEdgeReadLimitThatCannotShowACutIsRefused(t *testing.T) {
	t.Parallel()
	base := validFalkorConfig()
	if err := base.validate(); err != nil {
		t.Fatalf("the base config must pass, or this test proves nothing: %v", err)
	}
	for name, plant := range map[string]struct {
		limit int
		ok    bool
	}{
		"unset takes the default":       {limit: 0, ok: true},
		"the default":                   {limit: defaultNodeEdgeReadLimit, ok: true},
		"limit+1 is the server's cap":   {limit: falkorResultSetSize - 1, ok: true},
		"the limit is the server's cap": {limit: falkorResultSetSize, ok: false},
		"past the server's cap":         {limit: falkorResultSetSize + 1, ok: false},
		"negative":                      {limit: -1, ok: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config := base
			config.NodeEdgeReadLimit = plant.limit
			err := config.validate()
			if plant.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !plant.ok && (err == nil || !strings.Contains(err.Error(), "NodeEdgeReadLimit")) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// The vector read has the same shape: it asks for multiplier*limit+1, and a cap that
// hides the extra row reads a cut as complete.
func TestAnOverFetchThatCannotShowACutIsRefused(t *testing.T) {
	t.Parallel()
	for _, plant := range []struct {
		maxResults, multiplier int
		ok                     bool
	}{
		{50, 0, true},
		{50, 199, true},
		{50, 200, false},
		{25, 400, false},
	} {
		err := validateOverFetch(plant.maxResults, plant.multiplier)
		if plant.ok != (err == nil) {
			t.Fatalf("%+v: got %v", plant, err)
		}
	}
}
