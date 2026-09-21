package mcp

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The whole input domain of every guard the hosted transport adds, one row
// per cell, executed in one pass. The census at the end of each table fails
// when a row is dropped.

func TestValidBasePathDomain(t *testing.T) {
	cells := []struct {
		path string
		want bool
	}{
		{"", false}, {"/", true}, {"/mcp", true}, {"/MCP_v1-2.x", true}, {"/a/b", true},
		{"/mcp/", false}, {"mcp", false}, {"/a//b", false}, {"/a/../b", false}, {"/a/./b", false},
		{"/a/..", false}, {"/a/.", false}, {"/a b", false}, {"/mcp?x=1", false}, {"/mcp#x", false},
		{"/{id}", false}, {"/mcp*", false}, {"/é", false}, {HealthPath, false}, {ReadyPath, false},
		{"/" + strings.Repeat("a", 127), true}, {"/" + strings.Repeat("a", 128), false},
	}
	for _, c := range cells {
		if got := validBasePath(c.path); got != c.want {
			t.Errorf("validBasePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	if len(cells) != 22 {
		t.Fatalf("%d cells, want 22", len(cells))
	}
}

func TestInboundRequestIDDomain(t *testing.T) {
	cells := []struct {
		in, want string
	}{
		{"", ""}, {"a", "a"}, {"req-1_2.3:4", "req-1_2.3:4"}, {strings.Repeat("x", 128), strings.Repeat("x", 128)},
		{strings.Repeat("x", 129), ""}, {"a b", ""}, {"a\tb", ""}, {"é", ""}, {"a/b", ""}, {"a\"b", ""}, {"a=b", ""},
	}
	for _, c := range cells {
		if got := inboundRequestID(c.in); got != c.want {
			t.Errorf("inboundRequestID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if len(cells) != 11 {
		t.Fatalf("%d cells, want 11", len(cells))
	}
}

func TestBearerFromRequestDomain(t *testing.T) {
	cells := []struct {
		name      string
		values    []string
		token     string
		presented bool
	}{
		{"absent", nil, "", false},
		{"bearer", []string{"Bearer tok"}, "tok", true},
		{"lowercase_scheme", []string{"bearer tok"}, "tok", true},
		{"basic", []string{"Basic tok"}, "", true},
		{"no_space", []string{"Bearertok"}, "", true},
		{"empty_value", []string{""}, "", true},
		{"scheme_only", []string{"Bearer"}, "", true},
		{"empty_token", []string{"Bearer "}, "", true},
		{"two_headers", []string{"Bearer a", "Bearer a"}, "", true},
		{"double_space", []string{"Bearer  tok"}, " tok", true},
	}
	for _, c := range cells {
		r := httptest.NewRequest("POST", "/mcp", nil)
		for _, v := range c.values {
			r.Header.Add("Authorization", v)
		}
		token, presented := bearerFromRequest(r)
		if token != c.token || presented != c.presented {
			t.Errorf("%s: (%q, %v), want (%q, %v)", c.name, token, presented, c.token, c.presented)
		}
	}
	if len(cells) != 10 {
		t.Fatalf("%d cells, want 10", len(cells))
	}
}

func TestBucketDomain(t *testing.T) {
	vocab := HTTPToolVocabulary()
	cells := map[string]string{
		"": "other", "none": "other", "other": "other", "unspecified": "other",
		"source_evidence": "source_evidence", "SOURCE_EVIDENCE": "other", "source_evidence ": "other", "x\ny": "other",
	}
	for in, want := range cells {
		if got := bucket(in, vocab); got != want {
			t.Errorf("bucket(%q) = %q, want %q", in, got, want)
		}
	}
	if len(cells) != 8 {
		t.Fatalf("%d cells, want 8", len(cells))
	}
}

func TestServeOptionsValidateDomain(t *testing.T) {
	type cell struct {
		name    string
		mutate  func(*ServeOptions)
		setting string // "" means valid
	}
	cells := []cell{
		{"defaults", func(*ServeOptions) {}, ""},
		{"transport_http", func(o *ServeOptions) { o.Transport = "http" }, ""},
		{"transport_empty", func(o *ServeOptions) { o.Transport = "" }, TransportEnvironment},
		{"transport_case", func(o *ServeOptions) { o.Transport = "HTTP" }, TransportEnvironment},
		{"transport_other", func(o *ServeOptions) { o.Transport = "sse" }, TransportEnvironment},
		{"listen_empty", func(o *ServeOptions) { o.Listen = "" }, HTTPListenEnvironment},
		{"listen_no_port", func(o *ServeOptions) { o.Listen = "host" }, HTTPListenEnvironment},
		{"listen_empty_port", func(o *ServeOptions) { o.Listen = "host:" }, HTTPListenEnvironment},
		{"listen_port_0", func(o *ServeOptions) { o.Listen = ":0" }, ""},
		{"listen_port_max", func(o *ServeOptions) { o.Listen = ":65535" }, ""},
		{"listen_port_over", func(o *ServeOptions) { o.Listen = ":65536" }, HTTPListenEnvironment},
		{"listen_port_negative", func(o *ServeOptions) { o.Listen = ":-1" }, HTTPListenEnvironment},
		{"listen_port_name", func(o *ServeOptions) { o.Listen = ":http" }, HTTPListenEnvironment},
		{"listen_ipv6", func(o *ServeOptions) { o.Listen = "[::1]:8081" }, ""},
		{"base_path_invalid", func(o *ServeOptions) { o.BasePath = "mcp" }, HTTPBasePathEnvironment},
		{"read_header_zero", func(o *ServeOptions) { o.ReadHeaderTimeout = 0 }, HTTPReadHeaderTimeoutEnvironment},
		{"read_zero", func(o *ServeOptions) { o.ReadTimeout = 0 }, HTTPReadTimeoutEnvironment},
		{"write_negative", func(o *ServeOptions) { o.WriteTimeout = -time.Second }, HTTPWriteTimeoutEnvironment},
		{"idle_min", func(o *ServeOptions) { o.IdleTimeout = time.Nanosecond }, ""},
		{"shutdown_max", func(o *ServeOptions) { o.ShutdownTimeout = time.Hour }, ""},
		{"shutdown_over", func(o *ServeOptions) { o.ShutdownTimeout = time.Hour + time.Nanosecond }, HTTPShutdownTimeoutEnvironment},
		{"body_zero", func(o *ServeOptions) { o.MaxBodyBytes = 0 }, HTTPMaxBodyBytesEnvironment},
		{"body_negative", func(o *ServeOptions) { o.MaxBodyBytes = -1 }, HTTPMaxBodyBytesEnvironment},
		{"body_min", func(o *ServeOptions) { o.MaxBodyBytes = 1 }, ""},
		{"body_max", func(o *ServeOptions) { o.MaxBodyBytes = 16 << 20 }, ""},
		{"body_over", func(o *ServeOptions) { o.MaxBodyBytes = 16<<20 + 1 }, HTTPMaxBodyBytesEnvironment},
	}
	for _, c := range cells {
		opts := DefaultServeOptions()
		c.mutate(&opts)
		err := opts.Validate()
		var invalid *ErrServeOptionInvalid
		switch {
		case c.setting == "" && err != nil:
			t.Errorf("%s: %v, want valid", c.name, err)
		case c.setting != "" && (!errors.As(err, &invalid) || invalid.Setting != c.setting):
			t.Errorf("%s: %v, want invalid %s", c.name, err, c.setting)
		}
	}
	if len(cells) != 26 {
		t.Fatalf("%d cells, want 26", len(cells))
	}
}

func TestServeOptionsFromEnvironmentDomain(t *testing.T) {
	cells := []struct {
		name, key, value string
		wantErr          bool
	}{
		{"absent", "", "", false},
		{"blank_ignored", TransportEnvironment, "  ", false},
		{"duration_garbage", HTTPReadTimeoutEnvironment, "soon", true},
		{"duration_unitless", HTTPWriteTimeoutEnvironment, "30", true},
		{"body_fraction", HTTPMaxBodyBytesEnvironment, "1.5", true},
		{"body_garbage", HTTPMaxBodyBytesEnvironment, "1MiB", true},
		{"body_ok", HTTPMaxBodyBytesEnvironment, "2048", false},
		{"listen_raw", HTTPListenEnvironment, "0.0.0.0:9000", false},
	}
	for _, c := range cells {
		lookup := func(name string) (string, bool) {
			if name == c.key {
				return c.value, true
			}
			return "", false
		}
		_, err := ServeOptionsFromEnvironment(lookup)
		var invalid *ErrServeOptionInvalid
		if c.wantErr != (err != nil) || (err != nil && (!errors.As(err, &invalid) || invalid.Setting != c.key || strings.Contains(err.Error(), c.value))) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if len(cells) != 8 {
		t.Fatalf("%d cells, want 8", len(cells))
	}
}
