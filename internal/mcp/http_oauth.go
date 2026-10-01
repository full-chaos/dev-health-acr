package mcp

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// OAuth discovery for the hosted endpoint (MCP authorization, RFC 9728).
//
// The endpoint is an OAuth protected resource. It serves its protected
// resource metadata, which names the authorization server (acr-api), and
// every 401 it answers carries a resource_metadata challenge pointing at that
// document, so a client with no credential can discover where to log in.
// Everything here is static process configuration; nothing depends on the
// caller.

// ProtectedResourceMetadataPath is the RFC 9728 well-known path prefix.
const ProtectedResourceMetadataPath = "/.well-known/oauth-protected-resource"

// validateOAuthDiscovery accepts both settings empty, or both set with the
// resource URL an absolute HTTPS (or HTTP loopback) URL whose path is the MCP
// base path, and the authorization server an issuer origin.
func validateOAuthDiscovery(o ServeOptions) error {
	if o.ResourceURL == "" && o.AuthorizationServer == "" {
		return nil
	}
	if o.ResourceURL == "" {
		return &ErrServeOptionInvalid{Setting: ResourceURLEnvironment}
	}
	if o.AuthorizationServer == "" || !auth.ValidOAuthIssuer(o.AuthorizationServer) {
		return &ErrServeOptionInvalid{Setting: AuthorizationServerEnvironment}
	}
	resource, err := url.Parse(o.ResourceURL)
	if err != nil || resource.Host == "" || resource.User != nil || resource.RawQuery != "" || resource.Fragment != "" || resource.Path != o.BasePath {
		return &ErrServeOptionInvalid{Setting: ResourceURLEnvironment}
	}
	switch resource.Scheme {
	case "https":
	case "http":
		host := resource.Hostname()
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return &ErrServeOptionInvalid{Setting: ResourceURLEnvironment}
		}
	default:
		return &ErrServeOptionInvalid{Setting: ResourceURLEnvironment}
	}
	return nil
}

func (h *HTTPHandler) oauthDiscoveryEnabled() bool { return h.opts.ResourceURL != "" }

// resourceFor is the protected-resource identifier for one served path: the
// configured ResourceURL's origin plus that path, and for the root path the
// bare origin (no trailing slash, the canonical form of a host-only URL). A
// strict client that connected to a URL sees exactly that URL as `resource`.
func (h *HTTPHandler) resourceFor(path string) string {
	resource, err := url.Parse(h.opts.ResourceURL)
	if err != nil {
		return ""
	}
	if path == h.opts.BasePath {
		return h.opts.ResourceURL
	}
	if path == "/" {
		return resource.Scheme + "://" + resource.Host
	}
	return resource.Scheme + "://" + resource.Host + path
}

// metadataPathFor is the RFC 9728 §3.1 well-known path for a served path. The
// root path's document sits at the bare well-known URL.
func metadataPathFor(path string) string {
	if path == "/" {
		return ProtectedResourceMetadataPath
	}
	return ProtectedResourceMetadataPath + path
}

// protectedResourceMetadataURL is the path-suffixed RFC 9728 URL for the
// resource served at path: origin + well-known prefix + that path.
func (h *HTTPHandler) protectedResourceMetadataURL(path string) string {
	resource, err := url.Parse(h.opts.ResourceURL)
	if err != nil {
		return ""
	}
	return resource.Scheme + "://" + resource.Host + metadataPathFor(path)
}

// servedPaths is the base path followed by the aliases, without duplicates.
func (h *HTTPHandler) servedPaths() []string {
	paths := []string{h.opts.BasePath}
	for _, alias := range h.opts.AliasPaths {
		if !slices.Contains(paths, alias) {
			paths = append(paths, alias)
		}
	}
	return paths
}

func withNoStoreFor(resource string, h *HTTPHandler) http.Handler {
	metadata := sdkauth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               resource,
		AuthorizationServers:   []string{h.opts.AuthorizationServer},
		ScopesSupported:        auth.AdvertisedScopes(),
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Dev Health agent context runtime",
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		metadata.ServeHTTP(w, r)
	})
}

func (h *HTTPHandler) registerOAuthDiscovery(mux *http.ServeMux) {
	if !h.oauthDiscoveryEnabled() {
		return
	}
	// One document per served path (RFC 9728 §3.1 inserts the well-known
	// segment before the resource path), each naming the URL a client
	// connected to as its `resource`.
	for _, path := range h.servedPaths() {
		withNoStore := withNoStoreFor(h.resourceFor(path), h)
		if path == "/" {
			mux.Handle(ProtectedResourceMetadataPath, withNoStore)
			mux.Handle(ProtectedResourceMetadataPath+"/{$}", withNoStore)
			continue
		}
		mux.Handle(ProtectedResourceMetadataPath+path, withNoStore)
	}
	if !slices.Contains(h.servedPaths(), "/") {
		// Nothing is served at the root: clients that probe the root form
		// (MCP authorization's fallback) get the base path's document.
		mux.Handle(ProtectedResourceMetadataPath, withNoStoreFor(h.resourceFor(h.opts.BasePath), h))
	}
}

// challenge adds the resource_metadata parameter (RFC 9728 §5.1) to a Bearer
// challenge when discovery is enabled.
func (h *HTTPHandler) challenge(base, path string) string {
	if !h.oauthDiscoveryEnabled() {
		return base
	}
	parameter := `resource_metadata="` + h.protectedResourceMetadataURL(path) + `", scope="` + strings.Join(auth.AdvertisedScopes(), " ") + `"`
	if rest, ok := strings.CutPrefix(base, "Bearer "); ok {
		return "Bearer " + parameter + ", " + rest
	}
	return base + " " + parameter
}
