package reverseproxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

var packageZero httputil.ReverseProxy

type Holder struct{ Proxy httputil.ReverseProxy }

type Embedded struct{ httputil.ReverseProxy }

func Absent(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{Rewrite: func(*httputil.ProxyRequest) {}}
}

func Nil() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{Transport: nil}
}

func SingleHost(target *url.URL) *httputil.ReverseProxy {
	return httputil.NewSingleHostReverseProxy(target)
}

func Parameter(next http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{Transport: next}
}

func ZeroVar() *httputil.ReverseProxy {
	var proxy httputil.ReverseProxy
	return &proxy
}

func NewZero() *httputil.ReverseProxy {
	return new(httputil.ReverseProxy)
}

func Slice() []httputil.ReverseProxy {
	var proxies []httputil.ReverseProxy
	return proxies
}

func Map() map[string]httputil.ReverseProxy {
	return map[string]httputil.ReverseProxy{}
}

func AssignNil(proxy *httputil.ReverseProxy) {
	proxy.Transport = nil
}

func AssignUnproven(proxy *httputil.ReverseProxy, next http.RoundTripper) {
	proxy.Transport = next
}

func Address(proxy *httputil.ReverseProxy) {
	_ = &proxy.Transport
}
