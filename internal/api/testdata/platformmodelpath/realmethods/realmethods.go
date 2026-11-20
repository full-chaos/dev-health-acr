package realmethods

import (
	"context"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type investigator interface {
	Investigate(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InvestigationResult, error)
}

type runtime interface{ cf.ModelRuntime }

type wrapper struct{ *cf.Engine }

func ThroughInterface(i investigator) {
	_, _ = i.Investigate(context.Background(), storage.Principal{}, cf.InvestigationRequest{})
}

func ThroughEmbeddedReceiver(w wrapper) { run := w.Investigate; _ = run }

func ThroughEmbeddedInterface(r runtime) { phrase := r.SynthesizeAnswer; _ = phrase }

func Investigate(question string) string { return question }

func SynthesizeAnswer() {}

func PackageFunctions() {
	_ = Investigate("q")
	report := Investigate
	_ = report
	SynthesizeAnswer()
}
