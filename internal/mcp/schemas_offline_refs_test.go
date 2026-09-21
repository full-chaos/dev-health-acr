package mcp

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

// networkForbidden fails every outbound HTTP round trip and records the URL
// so the failing test can name the ref that tried to leave the document.
type networkForbidden struct {
	mu   sync.Mutex
	urls []string
}

func (n *networkForbidden) RoundTrip(req *http.Request) (*http.Response, error) {
	n.mu.Lock()
	n.urls = append(n.urls, req.URL.String())
	n.mu.Unlock()
	return nil, errors.New("network fetch forbidden while resolving a published MCP schema")
}

func (n *networkForbidden) attempts() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.urls...)
}

// forbidNetwork swaps http.DefaultTransport (the transport gojsonschema's
// reference loader uses) for one that refuses every request.
func forbidNetwork(t *testing.T) *networkForbidden {
	t.Helper()
	guard := &networkForbidden{}
	previous := http.DefaultTransport
	http.DefaultTransport = guard
	t.Cleanup(func() { http.DefaultTransport = previous })
	return guard
}

// nonLocalRefs returns every "$ref" value in the decoded document that does
// not start with "#", i.e. every pointer that leaves the document.
func nonLocalRefs(node any, path string, out *[]string) {
	switch typed := node.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				if ref, ok := child.(string); ok && !strings.HasPrefix(ref, "#") {
					*out = append(*out, path+"/$ref = "+ref)
				}
				continue
			}
			nonLocalRefs(child, path+"/"+key, out)
		}
	case []any:
		for i, child := range typed {
			nonLocalRefs(child, path+"/"+strconv.Itoa(i), out)
		}
	}
}

// publishedMCPSchemas returns name -> bytes for every published MCP JSON
// Schema, from BOTH the canonical contracts directory and the embedded
// mirror. The enumeration is by glob, and finding nothing (or a canonical
// set that differs from the embedded set) fails: a measurement that did not
// happen must not read as a pass.
func publishedMCPSchemas(t *testing.T) map[string][]byte {
	t.Helper()
	root := findRepoRoot(t)
	out := map[string][]byte{}

	canonical, err := filepath.Glob(filepath.Join(root, "contracts", "jsonschema", "v1", "mcp_*.schema.json"))
	if err != nil {
		t.Fatalf("glob canonical MCP schemas: %v", err)
	}
	if len(canonical) == 0 {
		t.Fatal("glob found no canonical MCP schemas: the measurement did not happen")
	}
	canonicalNames := map[string]bool{}
	for _, path := range canonical {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		canonicalNames[filepath.Base(path)] = true
		out["contracts/jsonschema/v1/"+filepath.Base(path)] = data
	}

	embedded, err := fs.Glob(schemaFiles, "schemas/mcp_*.schema.json")
	if err != nil {
		t.Fatalf("glob embedded MCP schemas: %v", err)
	}
	if len(embedded) == 0 {
		t.Fatal("glob found no embedded MCP schemas: the measurement did not happen")
	}
	if len(embedded) != len(canonical) {
		t.Fatalf("embedded MCP schema count %d differs from canonical count %d", len(embedded), len(canonical))
	}
	for _, name := range embedded {
		if !canonicalNames[filepath.Base(name)] {
			t.Fatalf("embedded %s has no canonical counterpart", name)
		}
		data, err := schemaFiles.ReadFile(name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		out["internal/mcp/"+name] = data
	}
	return out
}

// TestEveryPublishedMCPSchemaResolvesOfflineWithoutNetwork proves each
// published MCP schema (canonical and embedded copy) is self-contained: no
// "$ref" leaves the document, and the full document compiles with every
// reference resolved while any network fetch is forbidden and recorded.
func TestEveryPublishedMCPSchemaResolvesOfflineWithoutNetwork(t *testing.T) {
	schemas := publishedMCPSchemas(t)
	if len(schemas) < 2 {
		t.Fatalf("enumerated %d schema documents, want canonical and embedded copies", len(schemas))
	}

	for name, data := range schemas {
		t.Run(name, func(t *testing.T) {
			var decoded any
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("decode %s: %v", name, err)
			}
			var external []string
			nonLocalRefs(decoded, "", &external)
			if len(external) > 0 {
				t.Errorf("%s carries %d non-local $ref(s) although it claims to be self-contained:\n%s",
					name, len(external), strings.Join(external, "\n"))
			}

			guard := forbidNetwork(t)
			if _, err := gojsonschema.NewSchemaLoader().Compile(gojsonschema.NewBytesLoader(data)); err != nil {
				t.Errorf("%s does not compile offline: %v", name, err)
			}
			if attempts := guard.attempts(); len(attempts) > 0 {
				t.Errorf("%s tried to fetch over the network: %s", name, strings.Join(attempts, ", "))
			}
		})
	}
}
