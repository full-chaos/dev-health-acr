package mcp

import (
	"context"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// gateRig serves a live capability list the test can change after the server
// is built, and counts the capability reads.
type gateRig struct {
	fx    *fixtureServer
	live  atomic.Pointer[[]string]
	calls atomic.Int64
}

func newGateRig(t *testing.T, registered, live []string) (*gateRig, *mcpsdk.ClientSession, func()) {
	t.Helper()
	r := &gateRig{fx: newFixtureServer(t)}
	r.setLive(live)
	r.fx.CapabilitiesHandler = func(w http.ResponseWriter, _ *http.Request) {
		r.calls.Add(1)
		caps := validCapabilitiesFixture()
		caps.EnabledTools = append(caps.EnabledTools, *r.live.Load()...)
		writeJSONFixture(t, w, http.StatusOK, caps)
	}
	boot := newFixtureBootstrap(t, r.fx)
	boot.Capabilities.EnabledTools = append(boot.Capabilities.EnabledTools, registered...)
	client, closeFn := connectedClient(t, boot)
	return r, client, closeFn
}

func (r *gateRig) setLive(tools []string) { r.live.Store(&tools) }

func gateSurfacesServed(t *testing.T, client *mcpsdk.ClientSession) (prompt, resource bool) {
	t.Helper()
	_, perr := getSynthesizePrompt(t, client)
	_, rerr := readResource(t, client, synthesisOutputURI)
	return perr == nil, rerr == nil
}

func TestSynthesisSurfacesAreServedToACallerWithOnlyTheInterpretationTool(t *testing.T) {
	only := []string{toolInvestigateWithInterpretation}
	_, client, closeFn := newGateRig(t, only, only)
	defer closeFn()
	if !slices.Contains(promptNames(t, client), synthesizeAnswerPromptName) {
		t.Errorf("%s not listed", synthesizeAnswerPromptName)
	}
	if !slices.Contains(resourceURIs(t, client), synthesisOutputURI) {
		t.Errorf("%s not listed", synthesisOutputURI)
	}
	if p, r := gateSurfacesServed(t, client); !p || !r {
		t.Errorf("served prompt=%v resource=%v, want both", p, r)
	}
}

func TestSynthesisSurfacesAreHiddenFromACallerWithNeitherInvestigateTool(t *testing.T) {
	_, client, closeFn := newGateRig(t, nil, nil)
	defer closeFn()
	if slices.Contains(promptNames(t, client), synthesizeAnswerPromptName) {
		t.Errorf("%s listed", synthesizeAnswerPromptName)
	}
	if slices.Contains(resourceURIs(t, client), synthesisOutputURI) {
		t.Errorf("%s listed", synthesisOutputURI)
	}
	if p, r := gateSurfacesServed(t, client); p || r {
		t.Errorf("served prompt=%v resource=%v, want neither", p, r)
	}
}

func TestSynthesisSurfacesLiveCheckFollowsEitherTool(t *testing.T) {
	both := []string{toolInvestigateQuestion, toolInvestigateWithInterpretation}
	cases := []struct {
		name   string
		live   []string
		served bool
	}{
		{"both revoked", nil, false},
		{"only the interpretation tool revoked", []string{toolInvestigateQuestion}, true},
		{"only investigate_question revoked", []string{toolInvestigateWithInterpretation}, true},
		{"neither revoked", both, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, client, closeFn := newGateRig(t, both, both)
			defer closeFn()
			if p, rs := gateSurfacesServed(t, client); !p || !rs {
				t.Fatalf("before revocation prompt=%v resource=%v", p, rs)
			}
			r.setLive(tc.live)
			before := r.calls.Load()
			_, perr := getSynthesizePrompt(t, client)
			if got := r.calls.Load() - before; got != 1 {
				t.Errorf("prompt read made %d capability calls, want 1", got)
			}
			before = r.calls.Load()
			_, rerr := readResource(t, client, synthesisOutputURI)
			if got := r.calls.Load() - before; got != 1 {
				t.Errorf("resource read made %d capability calls, want 1", got)
			}
			if (perr == nil) != tc.served || (rerr == nil) != tc.served {
				t.Errorf("prompt err=%v resource err=%v, want served=%v", perr, rerr, tc.served)
			}
		})
	}
}

func TestSynthesisSurfacesLiveCheckWithOneRegisteredToolEachWay(t *testing.T) {
	for _, tool := range []string{toolInvestigateQuestion, toolInvestigateWithInterpretation} {
		t.Run(tool, func(t *testing.T) {
			r, client, closeFn := newGateRig(t, []string{tool}, []string{tool})
			defer closeFn()
			if p, rs := gateSurfacesServed(t, client); !p || !rs {
				t.Fatalf("served prompt=%v resource=%v", p, rs)
			}
			r.setLive(nil)
			if p, rs := gateSurfacesServed(t, client); p || rs {
				t.Errorf("after revocation prompt=%v resource=%v, want neither", p, rs)
			}
		})
	}
}

func TestInterpretationSurfacesStayGatedOnInvestigateQuestion(t *testing.T) {
	only := []string{toolInvestigateWithInterpretation}
	_, client, closeFn := newGateRig(t, only, only)
	defer closeFn()
	if slices.Contains(promptNames(t, client), promptInterpretQuestion) {
		t.Errorf("%s listed", promptInterpretQuestion)
	}
	if _, err := client.GetPrompt(context.Background(), &mcpsdk.GetPromptParams{Name: promptInterpretQuestion, Arguments: map[string]string{interpretQuestionArg: "q"}}); err == nil {
		t.Errorf("%s gettable", promptInterpretQuestion)
	}
	for _, u := range []string{uriInterpretationOutput, uriFactKinds} {
		if slices.Contains(resourceURIs(t, client), u) {
			t.Errorf("%s listed", u)
		}
		if _, err := readResource(t, client, u); err == nil {
			t.Errorf("%s readable", u)
		}
	}
}
