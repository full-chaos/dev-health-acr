package falkorgraph

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func scalar(value string) contextfabric.ScalarValue {
	return contextfabric.ScalarValue{String: &value}
}

func integerScalar(value int64) contextfabric.ScalarValue {
	return contextfabric.ScalarValue{Integer: &value}
}

// fullTemplateEntities is one fully populated entity per TEMPLATED kind --
// the shared fixture the template goldens and the byte-identity proof both
// compose from, so the two tests cannot drift onto different inputs.
func fullTemplateEntities() []contextfabric.EntityProjection {
	return []contextfabric.EntityProjection{
		{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:linear:CHAOS-1725", Label: "Harden session issuance"},
			Aliases: []string{"CHAOS-1725"},
			Properties: map[string]contextfabric.ScalarValue{
				"type": scalar("bug"), "labels": scalar("auth, security"),
				"project_name": scalar("Session Hardening"), "native_team_key": scalar("CHAOS"),
				"status": scalar("in_progress"),
			},
		},
		{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:52", Label: "Typed session tokens"},
			Properties: map[string]contextfabric.ScalarValue{
				"number": integerScalar(52), "repo": scalar("example-org/widget-service"),
				"branch": scalar("feat/chaos-1725-typed-session-tokens"),
				"body":   scalar("Rotate session tokens through typed issuance so replay windows close."),
				"state":  scalar("open"),
			},
		},
		{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectDeployment, CanonicalID: "deployment:deploy-1", Label: "production deployment"},
			Properties: map[string]contextfabric.ScalarValue{
				"environment": scalar("production"), "release_ref": scalar("v0.1.1"),
				"repo": scalar("example-org/widget-service"),
			},
		},
		{
			Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:run-1", Label: "CI run-1"},
			Properties: map[string]contextfabric.ScalarValue{
				"pipeline_name": scalar("fullstack-acceptance"), "branch": scalar("main"),
				"repo": scalar("example-org/widget-service"),
			},
		},
		{
			Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: "pull_request_review:review-1", Label: "PR #52 review"},
			Properties: map[string]contextfabric.ScalarValue{
				"state": scalar("APPROVED"), "number": integerScalar(52),
				"pr_title": scalar("Typed session tokens"), "repo": scalar("example-org/widget-service"),
			},
		},
		{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident:incident-1", Label: "Checkout latency spike"},
			Properties: map[string]contextfabric.ScalarValue{
				"severity": scalar("high"), "status": scalar("resolved"),
				"description": scalar("Checkout p95 breached its objective after the cache warmer stalled."),
			},
		},
		{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"},
			Aliases: []string{"CHAOS"},
			Properties: map[string]contextfabric.ScalarValue{
				"description": scalar("Owns the checkout stack."), "project_keys": scalar("CHAOS, OPS"),
			},
		},
		{
			Subject:     contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:p-1", Label: "Chaos Draw"},
			Aliases:     []string{"CHAOS-DRAW"},
			ProviderIDs: map[string]string{"linear": "p-1"},
			Properties:  map[string]contextfabric.ScalarValue{"state": scalar("started")},
		},
		{
			Subject:    contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1", Label: "example-org/widget-service"},
			Properties: map[string]contextfabric.ScalarValue{"tags": scalar("Go github")},
		},
	}
}

// TestPerKindTemplatesComposeTheSpecShapes pins each §2 template's exact
// output on a fully populated entity, bodies ON -- a golden per kind, so a
// template edit is a visible diff here and a deliberate
// embedTextTemplateVersion bump, never an accident.
func TestPerKindTemplatesComposeTheSpecShapes(t *testing.T) {
	t.Parallel()
	want := map[contextfabric.SubjectKind]string{
		contextfabric.SubjectWorkItem: "CHAOS-1725 Harden session issuance\n" +
			"type: bug labels: auth, security\n" +
			"project: Session Hardening team: CHAOS",
		contextfabric.SubjectPullRequest: "PR #52 Typed session tokens\n" +
			"repo: example-org/widget-service branch: feat/chaos-1725-typed-session-tokens\n" +
			"Rotate session tokens through typed issuance so replay windows close.",
		contextfabric.SubjectDeployment: "production deployment v0.1.1\n" +
			"repo: example-org/widget-service",
		contractsv1.ContextFabricSubjectCIRun: "CI run fullstack-acceptance branch: main repo: example-org/widget-service",
		contractsv1.ContextFabricSubjectPullRequestReview: "APPROVED review of PR #52: Typed session tokens\n" +
			"repo: example-org/widget-service",
		contextfabric.SubjectIncident: "Checkout latency spike\n" +
			"severity: high status: resolved\n" +
			"Checkout p95 breached its objective after the cache warmer stalled.",
		contextfabric.SubjectTeam: "Fullchaos\n" +
			"CHAOS\n" +
			"Owns the checkout stack.\n" +
			"projects: CHAOS, OPS",
		contextfabric.SubjectProject: "Chaos Draw\n" +
			"CHAOS-DRAW\n" +
			"provider: linear state: started",
		contextfabric.SubjectRepository: "example-org/widget-service\n" +
			"tags: Go github",
	}
	for _, entity := range fullTemplateEntities() {
		got := subjectSearchText(entity, true)
		if got != want[entity.Subject.Kind] {
			t.Errorf("%s template:\n got %q\nwant %q", entity.Subject.Kind, got, want[entity.Subject.Kind])
		}
	}
}

// TestTemplatesSkipAbsentFieldsAndLines: a label must never render over an
// absent value, and a line with no fields must vanish -- otherwise every
// sparse row would embed the template's fixed labels as shared corpus-wide
// tokens.
func TestTemplatesSkipAbsentFieldsAndLines(t *testing.T) {
	t.Parallel()
	sparse := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:linear:CHAOS-9", Label: "Sparse item"},
		Aliases: []string{"CHAOS-9"},
	}
	if got := subjectSearchText(sparse, true); got != "CHAOS-9 Sparse item" {
		t.Fatalf("sparse work item composed %q, want the alias+title line only", got)
	}
	degenerate := contextfabric.EntityProjection{
		Subject:    contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:run-9", Label: "CI run-9"},
		Properties: map[string]contextfabric.ScalarValue{"repo": scalar("example-org/widget-service")},
	}
	if got := subjectSearchText(degenerate, true); got != "CI run repo: example-org/widget-service" {
		t.Fatalf("degenerate CI run composed %q, want the L5-documented degenerate shape", got)
	}
}

// TestBodyGateGovernsBothBodyClassFields: bodies OFF removes exactly the
// §3 body-class text (PR body head, incident description head) from the
// ONE composition -- both arms, by construction -- and leaves every
// structured field in place.
func TestBodyGateGovernsBothBodyClassFields(t *testing.T) {
	t.Parallel()
	for _, entity := range fullTemplateEntities() {
		on := subjectSearchText(entity, true)
		off := subjectSearchText(entity, false)
		switch entity.Subject.Kind {
		case contextfabric.SubjectPullRequest:
			if !strings.Contains(on, "replay windows close") || strings.Contains(off, "replay windows close") {
				t.Fatalf("PR body must be present with the gate on and absent with it off; on=%q off=%q", on, off)
			}
			if !strings.Contains(off, "repo: example-org/widget-service") {
				t.Fatalf("gate off must keep structured PR fields, got %q", off)
			}
		case contextfabric.SubjectIncident:
			if !strings.Contains(on, "cache warmer stalled") || strings.Contains(off, "cache warmer stalled") {
				t.Fatalf("incident description must follow the gate; on=%q off=%q", on, off)
			}
		default:
			if on != off {
				t.Fatalf("%s has no body-class field; the gate must not change it (on=%q off=%q)", entity.Subject.Kind, on, off)
			}
		}
	}
}

// TestRenamedSubjectKeepsItsRetrievalHandles pins the invariant the live
// prior-canonical-metadata test enforces end to end: no template may drop
// aliases or previous names entitySearchText always indexed -- a renamed
// subject must stay lexically resolvable by its previous name.
func TestRenamedSubjectKeepsItsRetrievalHandles(t *testing.T) {
	t.Parallel()
	for _, entity := range fullTemplateEntities() {
		entity.PreviousNames = append(entity.PreviousNames, "previous-name-handle")
		if got := subjectSearchText(entity, true); !strings.Contains(got, "previous-name-handle") {
			t.Errorf("%s template dropped a previous name: %q", entity.Subject.Kind, got)
		}
	}
}

// TestWriteAndEmbedPathsComposeByteIdenticalText is the §0 decision-(a)/(b)
// proof: the lexical write path (subjectMergeAttrs -> propSearchText) and
// the embedding pass (collectEmbedTargets) derive BYTE-IDENTICAL text for
// every templated kind, under both body-gate values, at the validation
// floor -- the truncation window can never touch a complete template.
func TestWriteAndEmbedPathsComposeByteIdenticalText(t *testing.T) {
	t.Parallel()
	for _, includeBodies := range []bool{false, true} {
		entities := fullTemplateEntities()
		batch := contextfabric.ProjectionBatch{OrgID: "org", Entities: entities}
		targets, _ := collectEmbedTargets(batch, embedprovider.MinimumMaxTextRunes, includeBodies)
		embedText := map[string]string{}
		for _, target := range targets {
			embedText[target.canonicalID] = target.text
		}
		for _, entity := range entities {
			attrs := subjectMergeAttrs(entity.Subject, entity.Authorization, entity.EvidenceRefIDs,
				time.Time{}, nil, nil, entity.SourceVersion, &entity, includeBodies)
			written, _ := attrs[propSearchText].(string)
			if written == "" {
				t.Fatalf("%s wrote no search text", entity.Subject.CanonicalID)
			}
			if embedText[entity.Subject.CanonicalID] != written {
				t.Errorf("includeBodies=%v %s: embed text %q != written search text %q",
					includeBodies, entity.Subject.CanonicalID, embedText[entity.Subject.CanonicalID], written)
			}
		}
	}
}

// TestEveryCompleteTemplateFitsUnderTheFloor binds the §0 (c) arithmetic:
// a maximally populated template must compose under
// embedprovider.MinimumMaxTextRunes, or the floor no longer guarantees the
// byte-identity claim and must rise with the template.
func TestEveryCompleteTemplateFitsUnderTheFloor(t *testing.T) {
	t.Parallel()
	oversize := strings.Repeat("х", 4000) // multi-byte on purpose: caps are RUNE caps
	for _, entity := range fullTemplateEntities() {
		entity.Subject.Label = oversize
		for name := range entity.Properties {
			if name == "number" {
				continue
			}
			entity.Properties[name] = scalar(oversize)
		}
		got := subjectSearchText(entity, true)
		if runes := utf8.RuneCountInString(got); runes > embedprovider.MinimumMaxTextRunes {
			t.Errorf("%s maximal template is %d runes, above the %d floor",
				entity.Subject.Kind, runes, embedprovider.MinimumMaxTextRunes)
		}
	}
}

// TestOrganizationIsSkippedFromEmbeddingAndCounted (§2 organization + §7
// D2): the organization node -- a raw org UUID, pure vector noise -- is
// never embedded, stays fully lexical, and the skip is a REPORTED count,
// never an inference.
func TestOrganizationIsSkippedFromEmbeddingAndCounted(t *testing.T) {
	t.Parallel()
	org := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization:org-1", Label: "org-1"},
	}
	workItem := fullTemplateEntities()[0]
	batch := contextfabric.ProjectionBatch{OrgID: "org-1", Entities: []contextfabric.EntityProjection{org, workItem}}
	targets, skipped := collectEmbedTargets(batch, embedprovider.MinimumMaxTextRunes, false)
	if skipped != 1 {
		t.Fatalf("skipped = %d, want the organization node counted exactly once", skipped)
	}
	for _, target := range targets {
		if target.kind == string(contextfabric.SubjectOrganization) {
			t.Fatalf("the organization node must never be an embed target, got %+v", target)
		}
	}
	// The write path still composes its lexical text -- skipping is an
	// EMBED decision, not a search_text one.
	attrs := subjectMergeAttrs(org.Subject, org.Authorization, org.EvidenceRefIDs, time.Time{}, nil, nil, "", &org, false)
	if text, _ := attrs[propSearchText].(string); text == "" {
		t.Fatal("the organization node must keep its lexical search text")
	}
}

// TestCompositionTagAndStampCarryTheSemanticConfig pins the §4 Layer B/C
// mechanism: the tag is a readable canonical literal whose components are
// the template version, rune cap, body gate, and prefix selector, and the
// node stamp is the ONE identity string suffixed with it.
func TestCompositionTagAndStampCarryTheSemanticConfig(t *testing.T) {
	t.Parallel()
	if got := EmbedCompositionTag(2000, false, ""); got != "t2:r2000:b0:pnone" {
		t.Fatalf("EmbedCompositionTag = %q, want the canonical literal t2:r2000:b0:pnone", got)
	}
	if got := EmbedCompositionTag(4096, true, ""); got != "t2:r4096:b1:pnone" {
		t.Fatalf("EmbedCompositionTag = %q, want t2:r4096:b1:pnone", got)
	}

	fake := &fakeConn{}
	embedder := &stubEmbedder{vector: []float32{1, 0, 0, 0}}
	adapter := vectorAdapter(t, fake, embedder, 0.55)
	want := embedder.Identity().String() + "#" + EmbedCompositionTag(embedprovider.DefaultMaxTextRunes, false, "")
	if got := adapter.stampedEmbedderIdentity(embedder.Identity()); got != want {
		t.Fatalf("stampedEmbedderIdentity = %q, want %q", got, want)
	}
	// A body-gate flip moves the stamp -- the fence then fails stored
	// vectors closed until the rebuild.
	adapter.config.IncludeEmbedBodies = true
	if got := adapter.stampedEmbedderIdentity(embedder.Identity()); got == want {
		t.Fatal("a body-gate flip must move the stamped identity")
	}
}

// TestWriteStampAndReadVerifyUseTheSameTaggedIdentity drives the two
// identity-comparing sites through the adapter and asserts BOTH carry the
// tagged string -- suffix drift between stamp and verify would either
// never-match (permanent lexical degradation) or always-match (the fence
// stops fencing).
func TestWriteStampAndReadVerifyUseTheSameTaggedIdentity(t *testing.T) {
	t.Parallel()
	embedder := &stubEmbedder{vector: []float32{1, 0, 0, 0}}
	var stamped, verified string
	adapter := vectorAdapter(t, &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		if strings.Contains(cypher, "SET n."+propEmbedding) {
			stamped, _ = params["identity"].(string)
		}
		if strings.Contains(cypher, "n."+propEmbedderIdentity+" <> $identity") {
			verified, _ = params["identity"].(string)
		}
		return nil, nil
	}}, embedder, 0.55)

	target := embedTarget{kind: "work_item", canonicalID: "work_item:x", text: "text"}
	if err := adapter.writeNodeVector(context.Background(), "key", "org", target, []float32{1, 0, 0, 0}, embedder.Identity()); err != nil {
		t.Fatalf("writeNodeVector: %v", err)
	}
	if _, err := adapter.verifyStoredEmbedderIdentity(context.Background(), "key", "org"); err != nil {
		t.Fatalf("verifyStoredEmbedderIdentity: %v", err)
	}
	wantSuffix := "#" + EmbedCompositionTag(embedprovider.DefaultMaxTextRunes, false, "")
	if stamped == "" || !strings.HasSuffix(stamped, wantSuffix) {
		t.Fatalf("write stamp %q must carry the composition tag suffix %q", stamped, wantSuffix)
	}
	if verified != stamped {
		t.Fatalf("read-side expectation %q must equal the write stamp %q", verified, stamped)
	}
}

// TestEmbedRetrievalIdentityFromEnvTracksTheSemanticConfig: the persisted
// answer-reuse dimension and the node stamp derive from the same tag
// authority, "none" is the no-embedder sentinel, and a semantic-config
// flip moves the value.
func TestEmbedRetrievalIdentityFromEnvTracksTheSemanticConfig(t *testing.T) {
	t.Parallel()
	env := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	}
	if got, err := EmbedRetrievalIdentityFromEnv(env(nil)); err != nil || got != EmbedRetrievalIdentityNone {
		t.Fatalf("no embedder => %q, %v; want the literal none sentinel", got, err)
	}
	base := map[string]string{
		embedprovider.EnvBaseURL: "https://embed.example/v1/", embedprovider.EnvProvider: "openai",
		embedprovider.EnvModel: "text-embedding-3-large", embedprovider.EnvDimension: "3072",
	}
	got, err := EmbedRetrievalIdentityFromEnv(env(base))
	if err != nil {
		t.Fatalf("EmbedRetrievalIdentityFromEnv: %v", err)
	}
	want := "openai/text-embedding-3-large#" + EmbedCompositionTag(embedprovider.DefaultMaxTextRunes, false, "")
	if got != want {
		t.Fatalf("identity = %q, want %q", got, want)
	}
	withBodies := map[string]string{embedprovider.EnvProviderLocality: "local"}
	for key, value := range base {
		withBodies[key] = value
	}
	flipped, err := EmbedRetrievalIdentityFromEnv(env(withBodies))
	if err != nil {
		t.Fatalf("EmbedRetrievalIdentityFromEnv(local): %v", err)
	}
	if flipped == got {
		t.Fatal("a body-gate flip must move the embed retrieval identity")
	}
	invalid := map[string]string{embedprovider.EnvProviderLocality: "loopback"}
	for key, value := range base {
		invalid[key] = value
	}
	if _, err := EmbedRetrievalIdentityFromEnv(env(invalid)); err == nil {
		t.Fatal("an invalid locality must fail closed, never default")
	}
}
