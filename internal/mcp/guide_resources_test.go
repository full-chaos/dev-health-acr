package mcp

import (
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/mcp/guide"
)

func TestServerListsAndReadsGuideResources(t *testing.T) {
	fx := newFixtureServer(t)
	client, closeFn := connectedClient(t, newFixtureBootstrap(t, fx))
	defer closeFn()
	ctx := context.Background()

	listed, err := client.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := guide.Resources()
	if len(want) == 0 || len(listed.Resources) != len(want) {
		t.Fatalf("listed %d resources, embedded %d", len(listed.Resources), len(want))
	}
	byURI := map[string]string{}
	for _, res := range listed.Resources {
		byURI[res.URI] = res.MIMEType
		if res.Description == "" || res.Title == "" {
			t.Errorf("resource %s lacks title or description", res.URI)
		}
	}
	for _, res := range want {
		if byURI[res.URI] != guide.MIMEType {
			t.Fatalf("resource %s not listed as %s: %q", res.URI, guide.MIMEType, byURI[res.URI])
		}
		read, err := client.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: res.URI})
		if err != nil {
			t.Fatalf("read %s: %v", res.URI, err)
		}
		text, err := guide.Text(res.URI)
		if err != nil || text == "" {
			t.Fatalf("embedded text for %s: %v", res.URI, err)
		}
		if len(read.Contents) != 1 || read.Contents[0].Text != text || read.Contents[0].URI != res.URI {
			t.Fatalf("read %s returned unexpected contents", res.URI)
		}
	}
	if _, err := client.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "acr://guide/missing"}); err == nil {
		t.Fatal("unknown guide resource must fail")
	}
}

func TestGuideResourcesAreIdenticalForEveryCaller(t *testing.T) {
	fx := newFixtureServer(t)
	readAll := func(boot *Bootstrap) map[string]string {
		client, closeFn := connectedClient(t, boot)
		defer closeFn()
		out := map[string]string{}
		for _, res := range guide.Resources() {
			read, err := client.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{URI: res.URI})
			if err != nil {
				t.Fatal(err)
			}
			out[res.URI] = read.Contents[0].Text
		}
		return out
	}
	plain := readAll(newFixtureBootstrap(t, fx))
	writeback := readAll(newWritebackFixtureBootstrap(t, fx))
	if len(plain) == 0 {
		t.Fatal("no resources read")
	}
	for uri, text := range plain {
		if writeback[uri] != text {
			t.Errorf("resource %s differs between callers", uri)
		}
	}
}
