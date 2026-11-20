package mcp

import (
	"context"
	"testing"
	"time"
)

type fakeElicit struct {
	res   elicitResult
	block bool
	asked int
}

func (f *fakeElicit) ask(ctx context.Context, _ elicitChoice) elicitResult {
	f.asked++
	if f.block {
		<-ctx.Done()
		return elicitResult{Outcome: elicitUnavailable}
	}
	return f.res
}

func twoOptions() elicitChoice {
	return elicitChoice{Field: "subject", Options: []elicitOption{{Value: "a"}, {Value: "b"}}}
}

func TestElicitOutcomes(t *testing.T) {
	if r := elicit(context.Background(), nil, twoOptions()); r.Outcome != elicitUnavailable {
		t.Fatal("nil transport must be unavailable")
	}
	if r := elicit(context.Background(), &fakeElicit{res: elicitResult{Outcome: elicitAccepted, Value: "a"}}, elicitChoice{Options: []elicitOption{{Value: "a"}}}); r.Outcome != elicitUnavailable {
		t.Fatal("fewer than two options must not ask")
	}
	if r := elicit(context.Background(), &fakeElicit{res: elicitResult{Outcome: elicitAccepted, Value: "b"}}, twoOptions()); r.Outcome != elicitAccepted || r.Value != "b" {
		t.Fatalf("accepted offered value lost: %+v", r)
	}
	if r := elicit(context.Background(), &fakeElicit{res: elicitResult{Outcome: elicitAccepted, Value: "zzz"}}, twoOptions()); r.Outcome != elicitUnavailable || r.Value != "" {
		t.Fatalf("value outside the options must not pass: %+v", r)
	}
	if r := elicit(context.Background(), &fakeElicit{res: elicitResult{Outcome: elicitDeclined}}, twoOptions()); r.Outcome != elicitDeclined {
		t.Fatal("decline lost")
	}
}

func TestElicitTimeout(t *testing.T) {
	old := elicitTimeout
	elicitTimeout = 50 * time.Millisecond
	defer func() { elicitTimeout = old }()
	start := time.Now()
	r := elicit(context.Background(), &fakeElicit{block: true}, twoOptions())
	if r.Outcome != elicitUnavailable || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout not enforced: %+v", r)
	}
}
