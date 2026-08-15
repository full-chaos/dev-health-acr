package falkorgraph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestIsPureIdentifierTextDetectorTable is the CHAOS-3835 detector table
// test: positive, negative, and edge cases for the general-purpose
// primitive (id_only.go's isPureIdentifierText).
func TestIsPureIdentifierTextDetectorTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want bool
	}{
		// Positive: pure number.
		{"pure number", "12345", true},
		{"pure number with leading/trailing space", "  12345  ", true},
		// Positive: closed-vocabulary generated-id prefix + digits
		// (finding 3: run/build/pipeline/job/ci ONLY, enumerated).
		{"generated-id prefix shape", "run-123", true},
		{"underscore separator id shape", "run_12345", true},
		{"multiple id-shaped tokens", "run-12345 build-6789", true},
		{"pipeline prefix", "pipeline-89", true},
		{"job prefix", "job-12", true},
		{"ci prefix", "ci-345", true},
		// Positive: hex/UUID-shaped tokens. isHexShapedToken's floor is 12
		// characters as of round 4 (see id_only.go) -- "deadbeef01" (10
		// chars) is BELOW that floor and moved to the negative group below.
		{"12-char hex digest with digits", "a3f9c21b99ff", true},
		{"uuid shape", "550e8400-e29b-41d4-a716-446655440000", true},
		// Positive, unicode: pure-digit detection must be Unicode-aware, not
		// ASCII-only -- fullwidth digits recognized like ASCII ones.
		{"unicode fullwidth digits", "１２３４５", true},
		// Negative (finding 3, recall-first doctrine): a letter run +
		// separator + digits is NOT, by itself, id-only any more -- only the
		// enumerated generated-id prefixes are. A ticket key or an
		// unenumerated prefix is treated as carrying semantic content, so it
		// stays embedded rather than being suppressed.
		{"ticket-key shape is no longer id-only (finding 3)", "CHAOS-1725", false},
		{"short project code and hyphen digits is no longer id-only (finding 3)", "ABC-1", false},
		{"non-english letters with hyphen-digit shape is no longer id-only (finding 3)", "构建-12345", false},
		// Negative (finding 3): semantic branch/release names that the OLD
		// over-broad matcher misclassified as id-only, suppressing paraphrase
		// retrieval over them.
		{"semantic release name", "release-2025", false},
		{"semantic sprint name", "sprint-42", false},
		{"semantic branch name", "fix-login-bug", false},
		// Negative: mixed real content alongside an id token -- exactly the
		// ticket's "CI 123 fix login" class (minus the "CI" kind-prefix
		// boilerplate, which this primitive is never handed -- see
		// isPureIdentifierCIRun's doc comment for why the seam is
		// field-level).
		{"mixed id token plus real words", "123 fix login", false},
		{"mixed real slug (letters only, hyphenated)", "fullstack-acceptance", false},
		{"bare word with no digits", "smoke", false},
		{"real multi-word name", "Agent local Context Fabric smoke", false},
		{"letters touching digits with no separator stay real content", "log4j", false},
		// Negative: an all-hex-letter English word must not false-positive as
		// a hex digest just because every character happens to fall in
		// [a-fA-F] -- the digit-presence requirement is what excludes it.
		{"all-hex-letter english word stays real content", "decade", false},
		// Negative (round-3 finding 1, recall-first tiebreak): an all-hex-
		// letter English word carrying exactly ONE digit must also stay
		// real content -- the round-1 digit-presence bar (>=1) let these
		// through; round 3's bar (>=2 digits, length >=7) STILL let
		// "decade22"/"facade12" through (see round-4 finding 2 below).
		{"hex-letter word plus one trailing digit (round-3 finding 1)", "decade2", false},
		{"hex-letter word plus one trailing digit, second case (round-3 finding 1)", "facade1", false},
		{"hex-letter word plus one trailing digit, third case (round-3 finding 1)", "beaded1", false},
		// Negative (round-4 finding 2): round 3's counterexample --
		// all-hex-letter words with two trailing digits, which passed
		// round 3's (length>=7, digits>=2) bar. Round 4 ends the class by
		// length (>=12) rather than picking another digit-count bar that
		// a future word would defeat -- no natural-language word is 12+
		// characters of pure a-f, so these now correctly stay real
		// content instead of getting patched individually.
		{"hex-letter word plus two trailing digits (round-4 finding 2)", "decade22", false},
		{"hex-letter word plus two trailing digits, second case (round-4 finding 2)", "facade12", false},
		// Negative (round-4 finding 2): a real short-SHA-shaped token
		// UNDER the 12-character floor now DELIBERATELY embeds -- the
		// recall-first residual the ruling accepts (a short-sha noise
		// vector is the cheap failure; see id_only.go's isHexShapedToken
		// doc for the full tradeoff).
		{"short-sha-shaped token under the 12-char floor now embeds (round-4 finding 2)", "a3f9c21", false},
		{"10-char hex digest under the 12-char floor now embeds (round-4 finding 2)", "deadbeef01", false},
		// Positive (round-4 finding 2): a full 40-char sha, and any
		// 12+-character 2+-digit hex token, still skip -- the tightened
		// floor must not overshoot into refusing genuine long hex ids.
		{"full 40-char sha (round-4 finding 2)", "a94a8fe5ccb19ba61c4c0873d391e987982fbbd", true},
		// Edge: empty text is explicitly NOT this function's concern -- it
		// is a distinct, already-existing gating reason and must never be
		// double-counted under the id-only label.
		{"empty string", "", false},
		{"whitespace only", "   ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isPureIdentifierText(tc.text); got != tc.want {
				t.Errorf("isPureIdentifierText(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestIsPureIdentifierCIRunReflectsL5Criterion pins spec §5 L5's literal
// criterion ("rows whose composed text has no name/branch") plus the
// conservative id-shaped extension, read off the SOURCE fields
// (pipeline_name, branch) rather than the composed text.
func TestIsPureIdentifierCIRunReflectsL5Criterion(t *testing.T) {
	t.Parallel()
	entity := func(pipelineName, branch string) contextfabric.EntityProjection {
		props := map[string]contextfabric.ScalarValue{}
		if pipelineName != "" {
			props["pipeline_name"] = scalar(pipelineName)
		}
		if branch != "" {
			props["branch"] = scalar(branch)
		}
		return contextfabric.EntityProjection{
			Subject:    contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:x", Label: "CI run"},
			Properties: props,
		}
	}
	cases := []struct {
		name         string
		pipelineName string
		branch       string
		want         bool
	}{
		{"both absent -- the documented 22% degenerate case", "", "", true},
		{"both id-shaped but populated", "run-12345", "build-6789", true},
		{"real pipeline name, no branch", "fullstack-acceptance", "", false},
		{"no pipeline name, real branch", "", "main", false},
		{"real pipeline name and real branch", "fullstack-acceptance", "main", false},
		{"id-shaped pipeline name, absent branch", "12345", "", true},
		{"real pipeline name, id-shaped branch", "fullstack-acceptance", "run-1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isPureIdentifierCIRun(entity(tc.pipelineName, tc.branch))
			if got != tc.want {
				t.Errorf("isPureIdentifierCIRun(pipeline_name=%q, branch=%q) = %v, want %v",
					tc.pipelineName, tc.branch, got, tc.want)
			}
			// isPureIdentifierSubject must dispatch to the same verdict for
			// ci_pipeline_run -- it is the ONE seam collectEmbedTargets
			// calls.
			if got2 := isPureIdentifierSubject(entity(tc.pipelineName, tc.branch)); got2 != tc.want {
				t.Errorf("isPureIdentifierSubject(...) = %v, want %v (must match isPureIdentifierCIRun)", got2, tc.want)
			}
		})
	}
}

// TestIsPureIdentifierSubjectOnlyAppliesToCIRun: every OTHER kind returns
// false unconditionally -- T5 has a measured, documented population for
// exactly one kind (spec §1), and guessing a criterion for kinds the spec
// never measured would risk skipping real content the way an
// under-specified generic detector could.
func TestIsPureIdentifierSubjectOnlyAppliesToCIRun(t *testing.T) {
	t.Parallel()
	for _, entity := range fullTemplateEntities() {
		if entity.Subject.Kind == contractsv1.ContextFabricSubjectCIRun {
			continue
		}
		if isPureIdentifierSubject(entity) {
			t.Errorf("%s must never be treated as id-only by T5 -- only ci_pipeline_run has a documented population", entity.Subject.Kind)
		}
	}
}

// TestCollectEmbedTargetsSkipsIDOnlyCIRunsAndCountsThemSeparately is the
// REQUIRED-PROPERTY-1/2/4 proof at the collectEmbedTargets seam: an
// id-only CI run and a kind-skipped organization node are excluded from
// embed targets, counted under DIFFERENT reasons (never conflated), and
// the write path still composes the id-only row's ordinary lexical text --
// skipping is an EMBED decision, not a search_text one, exactly like the
// existing organization skip.
func TestCollectEmbedTargetsSkipsIDOnlyCIRunsAndCountsThemSeparately(t *testing.T) {
	t.Parallel()
	idOnlyRun := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:run-9", Label: "CI run-9"},
		Properties: map[string]contextfabric.ScalarValue{
			"repo": scalar("example-org/widget-service"),
		},
	}
	namedRun := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:run-10", Label: "CI run-10"},
		Properties: map[string]contextfabric.ScalarValue{
			"pipeline_name": scalar("fullstack-acceptance"), "branch": scalar("main"),
			"repo": scalar("example-org/widget-service"),
		},
	}
	org := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization:org-1", Label: "org-1"},
	}
	batch := contextfabric.ProjectionBatch{
		OrgID:    "org-1",
		Entities: []contextfabric.EntityProjection{idOnlyRun, namedRun, org},
	}
	targets, _, skipped := collectEmbedTargets(batch, embedprovider.MinimumMaxTextRunes, false)

	if skipped.Kind != 1 {
		t.Errorf("skipped.Kind = %d, want 1 (the organization node)", skipped.Kind)
	}
	if skipped.IDOnly != 1 {
		t.Errorf("skipped.IDOnly = %d, want 1 (the id-only CI run)", skipped.IDOnly)
	}
	if skipped.Total() != 2 {
		t.Errorf("skipped.Total() = %d, want 2", skipped.Total())
	}

	if len(targets) != 1 || targets[0].canonicalID != "ci_pipeline_run:run-10" {
		t.Fatalf("expected exactly the named CI run as an embed target, got %+v", targets)
	}

	// The write path must still compose the id-only row's ordinary lexical
	// text -- REQUIRED PROPERTY 2 is an EMBED decision, and lexical
	// retrieval over it must be unaffected.
	attrs := subjectMergeAttrs(idOnlyRun.Subject, idOnlyRun.Authorization, idOnlyRun.EvidenceRefIDs,
		time.Time{}, nil, nil, "", &idOnlyRun, false)
	written, _ := attrs[propSearchText].(string)
	if written == "" {
		t.Fatal("the id-only CI run must keep its lexical search text")
	}
	if written != "CI run repo: example-org/widget-service" {
		t.Errorf("written search text = %q, want the documented degenerate shape", written)
	}
}

// TestEmbedProjectionBatchSkipsIDOnlyCIRunNoVectorNoIdentityAndCountsMetric
// is the end-to-end pipeline proof (REQUIRED PROPERTIES 2 and 4): a batch
// mixing an id-only CI run with a real subject embeds ONLY the real
// subject, never writes a vector or an embedder-identity stamp for the
// id-only row, and the skip is counted through RecordVectorProjection under
// its own reason label -- distinguishable from the kind skip-list.
func TestEmbedProjectionBatchSkipsIDOnlyCIRunNoVectorNoIdentityAndCountsMetric(t *testing.T) {
	t.Parallel()
	var writtenCanonicalIDs []string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		if strings.Contains(cypher, "vecf32($vec)") && strings.Contains(cypher, "SET n.") {
			if id, ok := params["id"].(string); ok {
				writtenCanonicalIDs = append(writtenCanonicalIDs, id)
			}
		}
		return nil, nil
	}}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	telemetry := &recordingTelemetry{}
	adapter := vectorAdapterWithTelemetry(t, fake, &stubEmbedder{vector: make([]float32, 4)}, telemetry)

	observed := time.Now().UTC()
	batch := contextfabric.ProjectionBatch{
		OrgID: "org-1",
		Entities: []contextfabric.EntityProjection{
			{
				Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:run-9", Label: "CI run-9"},
				Properties: map[string]contextfabric.ScalarValue{
					"repo": scalar("example-org/widget-service"),
				},
				ObservedAt: observed, SourceVersion: "v1",
			},
			{
				Subject:    contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Authentication Service"},
				ObservedAt: observed, SourceVersion: "v1",
			},
		},
	}
	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}

	if len(writtenCanonicalIDs) != 1 || writtenCanonicalIDs[0] != "p1" {
		t.Fatalf("expected exactly one vector write, for p1 only, got %v", writtenCanonicalIDs)
	}
	for _, id := range writtenCanonicalIDs {
		if id == "run-9" {
			t.Fatal("the id-only CI run must never receive a vector or an embedder-identity stamp")
		}
	}
	if telemetry.embedded != 1 {
		t.Errorf("telemetry.embedded = %d, want 1", telemetry.embedded)
	}
	if telemetry.skippedIDOnly != 1 {
		t.Errorf("telemetry.skippedIDOnly = %d, want 1", telemetry.skippedIDOnly)
	}
	if telemetry.skippedKind != 0 {
		t.Errorf("telemetry.skippedKind = %d, want 0 -- the id-only skip must not be conflated with the kind skip-list", telemetry.skippedKind)
	}
}

// TestOldCompositionTagFailsTheNewFence is the T5 tag-bump proof (REQUIRED
// PROPERTY 3): a node stamped under the PRE-T5 "t2" composition tag must
// fail the read-side AC-3778-7 fence under the current ("t3") adapter,
// exactly like any other composition change -- the org degrades to
// lexical-only until the prescribed rebuild.
func TestOldCompositionTagFailsTheNewFence(t *testing.T) {
	t.Parallel()
	embedder := &stubEmbedder{vector: make([]float32, 8)}
	oldTagIdentity := embedder.Identity().String() + "#t2:r2000:b0:pnone"

	var sentIdentity string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		if strings.Contains(cypher, propEmbedderIdentity) {
			sentIdentity, _ = params["identity"].(string)
			// Simulate a node stamped under the OLD tag: its stored
			// identity differs from whatever the CURRENT adapter expects,
			// so the fence's mismatch predicate finds it.
			return []row{{"n.canonical_id": "stale-node"}}, nil
		}
		return nil, nil
	}}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, embedder, 0.55)

	matches, err := adapter.verifyStoredEmbedderIdentity(context.Background(), "key", "org")
	if err != nil {
		t.Fatalf("verifyStoredEmbedderIdentity: %v", err)
	}
	if matches {
		t.Fatal("a stale t2-tagged vector must not pass the fence under the t3 adapter")
	}
	// The identity the CURRENT (t3) adapter verified against must be the
	// NEW tag, not the old t2 literal -- confirming the fence's mismatch
	// finding above is actually attributable to the T5 tag bump, not to
	// some unrelated identity difference.
	if sentIdentity == "" {
		t.Fatal("verifyStoredEmbedderIdentity never issued its query")
	}
	if sentIdentity == oldTagIdentity {
		t.Fatalf("the currently-computed identity must not equal the old t2-tagged literal: %q", sentIdentity)
	}
	if want := "#" + EmbedCompositionTag(embedprovider.DefaultMaxTextRunes, false, ""); !strings.HasSuffix(sentIdentity, want) {
		t.Fatalf("fence must verify against the CURRENT (t3) tag, got suffix of %q, want suffix %q", sentIdentity, want)
	}
	if adapter.ensureVectorReadable(context.Background(), "key", "org") {
		t.Fatal("a stale t2-tagged corpus must fail the whole read fence, not just the identity probe")
	}
}
