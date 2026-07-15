package auth

import "net/http"

type responseStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseStatusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseStatusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *responseStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseStatusWriter) SetDenialCode(code string) {
	if marker, ok := w.ResponseWriter.(interface{ SetDenialCode(string) }); ok {
		marker.SetDenialCode(code)
	}
}

func (w *responseStatusWriter) successful() bool {
	return w.status == 0 || w.status >= http.StatusOK && w.status < http.StatusBadRequest
}
