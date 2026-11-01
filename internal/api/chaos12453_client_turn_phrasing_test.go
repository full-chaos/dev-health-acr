package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12453 (lead ruling, option a): a turn on the caller's own model makes
// no service model call. Offer text stays deterministic, whoever would have
// paid: the organization's own key or the deployment model.

// asNarrowedUser sends the rig's calls through the real access read as a
// user-class credential, so each turn runs on the principal
// NarrowRepositoryScopes leaves: the shape every web and MCP user has.
func (r *phrasingRouteRig) asNarrowedUser(t *testing.T) {
	t.Helper()
	reader := &fakePrincipalAccess{}
	reader.set(grantedAccess(), nil)
	r.app.runtime.PrincipalAccess = reader
	// A user-class create is refused below the wait budget's minimum.
	r.app.config.RequestTimeout = time.Minute
	owner, _, _ := strings.Cut(hostedTestRepository, "/")
	authenticateAsCredential(t, r.app, storage.CredentialClientClassOAuthUser, []string{owner + "/*"})
}

func TestACallerModelTurnNeverPhrasesItsOffers(t *testing.T) {
	for name, org := range map[string]struct {
		grants *serviceModelGrants
		hasBYO bool
	}{
		"no feature and no own model": {&serviceModelGrants{}, false},
		"the deployment model":        {&serviceModelGrants{feature: true, fallback: true}, false},
		"its own model":               {&serviceModelGrants{feature: true}, true},
		"both":                        {&serviceModelGrants{feature: true, fallback: true}, true},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newPhrasingRouteRig(t, org.grants, org.hasBYO)
			rig.asNarrowedUser(t)
			options := kindOptionsOf(t, rig.post(t))
			if rig.phrasing.count() != 0 || rig.byo.count() != 0 {
				t.Fatalf("phrasing calls = deployment %d byo %d, want 0 and 0", rig.phrasing.count(), rig.byo.count())
			}
			if options[0].Label != "a pull request" || options[0].Phrasing != "" {
				t.Fatalf("kind option = %+v, want the structural label and no phrasing", options[0])
			}
			if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
				t.Fatalf("model calls = interpret %d synthesize %d, want 0 and 0", interprets, synthesizes)
			}
			if got := phrasingOutcomes(rig.logs); !slices.Equal(got, []string{string(contextfabric.OfferPhrasingWithheld)}) {
				t.Fatalf("phrasing outcomes = %v, want [%s]", got, contextfabric.OfferPhrasingWithheld)
			}
		})
	}
}

// A write-back that lands on the same no-commit turn: it carries a draft and
// must reach the offers without a phrasing call. The first half of the guard
// asserts the turn really composed offers, so a write-back refused before them
// does not read as a pass.
func TestACallerModelWriteBackNeverPhrasesItsOffers(t *testing.T) {
	rig := newPhrasingRouteRig(t, &serviceModelGrants{feature: true, fallback: true}, true)
	rig.asNarrowedUser(t)
	body := investigationRequestBody()
	body.Question = writeBackQuestion
	body.SynthesisMode = contractsv1.ContextFabricSynthesisModeClient
	body.SuppliedInterpretation = writeBackInterpretation(t)
	body.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	assembly := synthesisprompt.ClientAssembly()
	body.SuppliedSynthesis = &contractsv1.ContextFabricSuppliedSynthesis{
		Output: json.RawMessage(`{}`), ModelOutputVersion: assembly.ModelOutputVersion, PromptVersion: assembly.PromptVersion,
		SystemSHA256: assembly.SystemSHA256, InputSHA256: "0000000000000000000000000000000000000000000000000000000000000000", ClientModel: "writer-model-1",
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+rig.token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	rig.app.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("write-back: %d %s", recorder.Code, recorder.Body.String())
	}
	if options := kindOptionsOf(t, decodeEnvelope(t, recorder)); options[0].Phrasing != "" {
		t.Fatalf("kind option = %+v, want no phrasing", options[0])
	}
	if rig.phrasing.count() != 0 || rig.byo.count() != 0 {
		t.Fatalf("phrasing calls = deployment %d byo %d, want 0 and 0", rig.phrasing.count(), rig.byo.count())
	}
}

// The control: web's service-synthesis turn, by the same narrowed user, is
// phrased as before. Only a caller-model turn changed.
func TestAServiceSynthesisTurnIsStillPhrased(t *testing.T) {
	rig := newWindowGatePhrasingRig(t, &serviceModelGrants{feature: true, fallback: true}, false)
	rig.asNarrowedUser(t)
	options := windowGateCandidateOptions(t, rig.postWithoutWindow(t))
	if rig.phrasing.count() != 1 {
		t.Fatalf("deployment phrasing calls = %d, want 1", rig.phrasing.count())
	}
	if options[0].Phrasing != phrasedKindLabel {
		t.Fatalf("candidate option phrasing = %q, want %q", options[0].Phrasing, phrasedKindLabel)
	}
}
