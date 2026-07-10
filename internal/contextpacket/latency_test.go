package contextpacket_test

import (
	"context"
	"slices"
	"testing"
	"time"
)

func BenchmarkAssembler_fixtureExactCommit(b *testing.B) {
	assembler := fixtureAssembler(&testing.T{})
	request := fixtureRequest("benchmark-exact", "main", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2")
	b.ReportAllocs()
	for range b.N {
		if _, err := assembler.Assemble(context.Background(), fixturePrincipal(), request); err != nil {
			b.Fatal(err)
		}
	}
}

func TestAssembler_fixtureP95CompletesWithinTwoSeconds(t *testing.T) {
	// Given
	assembler := fixtureAssembler(t)
	request := fixtureRequest("latency-exact", "main", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2")
	durations := make([]time.Duration, 0, 25)

	// When
	for range cap(durations) {
		started := time.Now()
		if _, err := assembler.Assemble(context.Background(), fixturePrincipal(), request); err != nil {
			t.Fatalf("assemble fixture packet: %v", err)
		}
		durations = append(durations, time.Since(started))
	}

	// Then
	slices.Sort(durations)
	p95 := durations[(len(durations)*95+99)/100-1]
	if p95 > 2*time.Second {
		t.Fatalf("fixture packet p95 = %s, want <= 2s", p95)
	}
}
