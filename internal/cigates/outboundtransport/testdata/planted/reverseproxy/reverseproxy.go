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

func PointerParameter(proxy *httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) {
	proxy.ServeHTTP(w, r)
}

func ValueParameter(proxy httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) {
	proxy.ServeHTTP(w, r)
}

func GroupedParameters(first, second *httputil.ReverseProxy) {}

type PointerHolder struct{ Proxy *httputil.ReverseProxy }

var packagePointer *httputil.ReverseProxy

func PointerVar() {
	var proxy *httputil.ReverseProxy
	_ = proxy
}

func UnnamedResult(from func() *httputil.ReverseProxy) *httputil.ReverseProxy {
	return from()
}

func ValueResult(from func() httputil.ReverseProxy) httputil.ReverseProxy {
	return from()
}

func PointerSlice() []*httputil.ReverseProxy { return nil }

func PointerMap() map[string]*httputil.ReverseProxy { return nil }

func PointerChan() chan *httputil.ReverseProxy { return nil }
