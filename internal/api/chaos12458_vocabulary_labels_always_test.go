package api

import "testing"

// CHAOS-12458: the vocabulary_labels map is served on every answer with no
// switch to turn on. A default app (no flag exists any more) serves the map.
func TestTheMapIsServedWithNoSwitch(t *testing.T) {
	investigator := investigatorOf(answering(t, "context_fabric_investigation_result.v1.json", nil))
	app, token, _ := newStreamTestApp(t, investigator)
	answer := serveJSON(t, app, token, investigationRequestBody())
	if labels := decodeLabeled(t, answer.Body.Bytes()).VocabularyLabels; len(labels) == 0 {
		t.Fatalf("a default app served no vocabulary_labels: %s", answer.Body.String())
	}
}
