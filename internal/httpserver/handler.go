// Package httpserver serves BinFlow's internal HTTP interface.
// httpserver 包提供 BinFlow 的内部 HTTP 接口。
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// NewHandler returns the root handler for BinFlow's internal HTTP surface.
// NewHandler 返回 BinFlow 内部 HTTP 服务的根处理器。
//
// /healthz is an internal liveness endpoint for process supervision; it is
// not an Artifactory API compatibility surface. Unknown paths fall through
// to chi's default 404 (http.NotFound).
func NewHandler() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", getHealthz)
	r.Head("/healthz", headHealthz)
	r.MethodNotAllowed(methodNotAllowed)
	return r
}

// methodNotAllowed answers 405 for known paths served with an unregistered
// method. chi's default responder derives the allowed methods correctly but
// emits one Allow field line per method; net/http's Header.Get returns only
// the first line, so clients would see an incomplete list. We advertise the
// /healthz methods as a single comma-joined line instead. Revisit this when
// routes beyond /healthz exist, so Allow stays per-path accurate.
func methodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", "GET, HEAD")
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func getHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok\n"))
}

func headHealthz(w http.ResponseWriter, _ *http.Request) {
	// Write only the status so the response carries no body for HEAD.
	w.WriteHeader(http.StatusOK)
}
