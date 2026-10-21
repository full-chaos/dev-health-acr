package directread

import (
	"net/http"
	"testing"
	"time"
)

func TestASuppliedRegistryClientWithoutATransportGetsOne(t *testing.T) {
	supplied := &http.Client{}
	watch, err := NewRegistryWatch(RegistryWatchConfig{Catalogue: &Catalogue{}, BaseURL: "http://upstream.invalid", HTTPClient: supplied})
	if err != nil {
		t.Fatal(err)
	}
	if watch.client.Transport == nil {
		t.Fatal("a supplied registry client with no transport would dial through http.DefaultTransport and its environment proxy")
	}
	if supplied.Transport != nil {
		t.Fatal("the supplied client was modified")
	}
}

func TestSuppliedQueryClientsWithoutATransportGetOne(t *testing.T) {
	for name, build := range map[string]func(*http.Client) (*HTTPQueryClient, error){
		"query": func(c *http.Client) (*HTTPQueryClient, error) {
			return NewHTTPQueryClientWithHTTP("http://upstream.invalid", time.Second, c)
		},
		"graphql": func(c *http.Client) (*HTTPQueryClient, error) {
			return NewHTTPGraphQLClientWithHTTP("http://upstream.invalid", time.Second, c)
		},
	} {
		supplied := &http.Client{}
		client, err := build(supplied)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if client.client.Transport == nil {
			t.Fatalf("%s: a supplied client with no transport would dial through http.DefaultTransport and its environment proxy", name)
		}
		if supplied.Transport != nil {
			t.Fatalf("%s: the supplied client was modified", name)
		}
	}
}
