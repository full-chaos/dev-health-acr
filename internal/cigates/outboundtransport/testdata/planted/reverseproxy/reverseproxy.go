package reverseproxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

func Absent(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{Rewrite: func(*httputil.ProxyRequest) {}}
}

func Nil() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{Transport: nil}
}

func SingleHost(target *url.URL) *httputil.ReverseProxy {
	return httputil.NewSingleHostReverseProxy(target)
}

func Pinned() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{Transport: &http.Transport{}}
}
