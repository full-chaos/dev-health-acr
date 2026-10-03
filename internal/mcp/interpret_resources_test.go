package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
)

func readResource(t *testing.T, client *mcpsdk.ClientSession, uri string) (*mcpsdk.ReadResourceResult, error) {
	t.Helper()
	return client.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{URI: uri})
}

func resourceURIs(t *testing.T, client *mcpsdk.ClientSession) []string {
	t.Helper()
	listed, err := client.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var uris []string
	for _, r := range listed.Resources {
		uris = append(uris, r.URI)
	}
	return uris
}

func interpretResourceBoot(t *testing.T, h *dtHosted) *Bootstrap {
	return h.boot(t, toolInvestigateQuestion, toolDataCatalog)
}

func TestInterpretResourcesBytesEqualTheirSources(t *testing.T) {
	h := newDTHosted(t)
	client, closeFn := connectedClient(t, interpretResourceBoot(t, h))
	defer closeFn()

	schemaWant, err := genkitruntime.InterpretationOutputSchema()
	if err != nil {
		t.Fatal(err)
	}
	read, err := readResource(t, client, uriInterpretationOutput)
	if err != nil {
		t.Fatal(err)
	}
	got := read.Contents[0].Text
	var gotMap, wantMap map[string]any
	if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schemaWant, &wantMap); err != nil {
		t.Fatal(err)
	}
	if gotMap["$id"] != interpretprompt.OutputSchemaID || !strings.Contains(gotMap["title"].(string), genkitruntime.DefaultSchemaVersion) {
		t.Errorf("schema $id/title = %v / %v", gotMap["$id"], gotMap["title"])
	}
	delete(gotMap, "$id")
	delete(gotMap, "title")
	delete(wantMap, "$id")
	delete(wantMap, "title")
	a, _ := json.Marshal(gotMap)
	b, _ := json.Marshal(wantMap)
	if string(a) != string(b) {
		t.Error("served schema differs from the runtime's InterpretationOutputSchema")
	}
	if read.Contents[0].Meta["sha256"] != sha256Hex(got) || read.Contents[0].Meta["model_output_version"] != genkitruntime.DefaultSchemaVersion {
		t.Errorf("schema _meta = %v", read.Contents[0].Meta)
	}

	read, err = readResource(t, client, uriFactKinds)
	if err != nil {
		t.Fatal(err)
	}
	text := read.Contents[0].Text
	if text != interpretprompt.FactKindsGuide() || read.Contents[0].Meta["sha256"] != sha256Hex(text) {
		t.Error("fact-kind guide differs from interpretprompt.FactKindsGuide() or its sha")
	}
	if !strings.Contains(interpretprompt.System(), strings.TrimSuffix(strings.SplitN(text, "\n\n", 3)[2], "\n")[:200]) {
		t.Error("the glossary is not the text the prompt states")
	}

	read, err = readResource(t, client, uriDataCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if read.Contents[0].Text != dtCatalogBody || read.Contents[0].Meta["sha256"] != sha256Hex(dtCatalogBody) {
		t.Error("catalogue resource bytes differ from the hosted data_catalog answer")
	}
}

func TestInterpretResourcesListShape(t *testing.T) {
	h := newDTHosted(t)
	client, closeFn := connectedClient(t, interpretResourceBoot(t, h))
	defer closeFn()
	listed, err := client.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byURI := map[string]*mcpsdk.Resource{}
	for _, r := range listed.Resources {
		byURI[r.URI] = r
	}
	for uri, mime := range map[string]string{
		uriInterpretationOutput: "application/schema+json",
		uriFactKinds:            "text/markdown",
		uriDataCatalog:          "application/json",
	} {
		r := byURI[uri]
		if r == nil || r.MIMEType != mime || r.Description == "" {
			t.Fatalf("resource %s listed as %+v", uri, r)
		}
	}
	for _, uri := range []string{uriInterpretationOutput, uriFactKinds} {
		keys := make([]string, 0, 4)
		for k := range byURI[uri].Meta {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"model_output_version", "prompt_version", "service_version", "sha256"}) {
			t.Errorf("%s _meta keys = %v", uri, keys)
		}
	}
}

func TestInterpretResourcesFollowTheCallersTools(t *testing.T) {
	h := newDTHosted(t)
	none, closeNone := connectedClient(t, h.boot(t))
	defer closeNone()
	uris := resourceURIs(t, none)
	for _, u := range []string{uriInterpretationOutput, uriFactKinds, uriDataCatalog} {
		if slices.Contains(uris, u) {
			t.Errorf("%s listed without its tool", u)
		}
		if _, err := readResource(t, none, u); err == nil {
			t.Errorf("%s readable without its tool", u)
		}
	}
	catalogOnly, closeCat := connectedClient(t, h.boot(t, toolDataCatalog))
	defer closeCat()
	uris = resourceURIs(t, catalogOnly)
	if !slices.Contains(uris, uriDataCatalog) || slices.Contains(uris, uriInterpretationOutput) || slices.Contains(uris, uriFactKinds) {
		t.Errorf("catalog-only caller sees %v", uris)
	}
}
