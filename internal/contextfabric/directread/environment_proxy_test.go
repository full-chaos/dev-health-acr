package directread

import (
	"net/http"
	"testing"
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
