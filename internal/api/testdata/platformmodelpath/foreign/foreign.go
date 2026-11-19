package foreign

type client struct{ InterpretQuestion func() }

func (client) Investigate() {}

func (client) SynthesizeAnswer() {}

func PhraseStructureOffers() {}

type investigator interface{ Investigate() }

func Foreign(c client) {
	c.Investigate()
	run := c.Investigate
	_ = run
	_ = client.Investigate
	c.SynthesizeAnswer()
	c.InterpretQuestion()
	PhraseStructureOffers()
}

func ThroughInterface(i investigator) { i.Investigate() }
