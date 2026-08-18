package graphrank

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// chaos3899Secret is a planted, unmistakable literal -- if this string ever
// shows up in a traced evidence_round/evidence_probe field, the corpus-
// safety rule (resolve.go's ResolutionTracer doc comment: "NEVER raw term
// text, NEVER question text") is broken.
const chaos3899Secret = "zx-CORPUS-SECRET-9f3a1c-do-not-leak"

// TestShadowEvidenceRound_PrivacyCanary plants the secret in EVERY
// text-shaped input the shadow round touches (the question text itself,
// the handle value it extracts from that question, and an anchor claimant
// term) and asserts it is absent from every field of every
// evidence_round/evidence_probe trace event, via reflection over every
// string field (so a future field addition is covered automatically,
// mirroring the existing chaos3897/chaos3858 canary tests' own
// discipline).
func TestShadowEvidenceRound_PrivacyCanary(t *testing.T) {
	t.Parallel()
	question := fmt.Sprintf("why did PR 532 fail for %s?", chaos3899Secret)
	input := ShadowEvidenceRoundInput{
		RequestID: "req-canary", OrgID: "org-1", Question: question,
		CurrentAxis: true, UnscopedVisibility: true, AliasLookupComplete: true,
		PooledKinds: []CensusKind{contextfabric.SubjectPullRequest},
		AliasClaimants: map[string][]IdentityMatch{
			chaos3899Secret: {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + chaos3899Secret}, Mechanism: contextfabric.MatchAlias}},
		},
		CensusFunc: func(_ context.Context, _ string, _ CensusKind, handleValue string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
			if handleValue != "532" {
				t.Fatalf("CensusFunc received handleValue=%q, want the extracted digits only, never the secret", handleValue)
			}
			return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "repo-1:532"}, nil
		},
	}
	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)
	if att.Outcome != ShadowWouldCommit {
		t.Fatalf("att = %#v", att)
	}

	events := append(tracer.eventsForStage("evidence_round"), tracer.eventsForStage("evidence_probe")...)
	if len(events) == 0 {
		t.Fatalf("no evidence_round/evidence_probe events captured -- canary cannot run")
	}
	for _, event := range events {
		assertNoSecretInStringFields(t, event)
	}
}

func assertNoSecretInStringFields(t *testing.T, event ResolutionTraceEvent) {
	t.Helper()
	v := reflect.ValueOf(event)
	typ := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if field.Kind() != reflect.String {
			continue
		}
		if strings.Contains(field.String(), chaos3899Secret) {
			t.Fatalf("field %s.%s leaked the planted secret: %q", typ.Name(), typ.Field(i).Name, field.String())
		}
	}
	// Subject.CanonicalID/Label are the one nested struct this event type
	// carries text-shaped fields on -- covered explicitly since reflection
	// above only walks ResolutionTraceEvent's own top-level fields.
	if strings.Contains(event.Subject.CanonicalID, chaos3899Secret) || strings.Contains(event.Subject.Label, chaos3899Secret) {
		t.Fatalf("event.Subject leaked the planted secret: %#v", event.Subject)
	}
}
