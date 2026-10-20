// Package httpserver serves BinFlow's internal HTTP interface.
// httpserver 包提供 BinFlow 的内部 HTTP 接口。
package httpserver

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

const healthBody = "ok\n"

// NewHandler returns the root handler for BinFlow's internal HTTP surface.
// NewHandler 返回 BinFlow 内部 HTTP 服务的根处理器。
//
// /healthz is an internal liveness endpoint for process supervision; it is
// not an Artifactory API compatibility surface. Unknown paths fall through
// to chi's default 404 (http.NotFound).
func NewHandler() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", healthz)
	r.Head("/healthz", healthz)
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

func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(healthBody)))
	w.WriteHeader(http.StatusOK)
	// HEAD keeps GET's representation headers without writing its body.
	// HEAD 保留 GET 的表示头，但不写入正文。
	if r.Method == http.MethodGet {
		w.Write([]byte(healthBody))
	}
}
