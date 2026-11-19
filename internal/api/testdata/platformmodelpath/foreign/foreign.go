package foreign

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type client struct{ InterpretQuestion func() }

func (client) Investigate() {}

func (client) SynthesizeAnswer() {}

func PhraseStructureOffers() {}

func Investigate(question string) string { return question }

type investigator interface{ Investigate() }

type synthesizer interface {
	SynthesizeAnswer(context.Context, storage.Principal, string) (string, error)
}

func Foreign(c client) {
	c.Investigate()
	run := c.Investigate
	_ = run
	_ = client.Investigate
	c.SynthesizeAnswer()
	c.InterpretQuestion()
	PhraseStructureOffers()
}

func PackageFunction() { _ = Investigate("q"); report := Investigate; _ = report }

func ThroughForeignInterface(i investigator, s synthesizer) {
	i.Investigate()
	_ = investigator.Investigate
	_, _ = s.SynthesizeAnswer(context.Background(), storage.Principal{}, "q")
}
