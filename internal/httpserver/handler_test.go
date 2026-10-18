package httpserver_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/httpserver"
)

// The tests below describe the behavior of the internal health endpoint.
// They exercise only the exported NewHandler contract (an http.Handler), so
// the router layout can change without rewriting the tests.

func TestHealthzGETReturnsPlainTextOK(t *testing.T) {
	rec := httptest.NewRecorder()
	httpserver.NewHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "ok\n" {
		t.Fatalf("GET /healthz body = %q, want %q", got, "ok\n")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("GET /healthz Content-Type = %q, want prefix %q", ct, "text/plain")
	}
}

func TestHealthzHEADReturns200WithEmptyBody(t *testing.T) {
	rec := httptest.NewRecorder()
	httpserver.NewHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD /healthz status = %d, want %d", rec.Code, http.StatusOK)
	}
	// The handler itself must write no body for HEAD, independent of the
	// body stripping a real net/http server performs for HEAD responses.
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD /healthz body = %q, want empty body", rec.Body.String())
	}
}

func TestHealthzUnsupportedMethodsGet405WithAllowGETHEAD(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			httpserver.NewHandler().ServeHTTP(rec, httptest.NewRequest(method, "/healthz", nil))

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s /healthz status = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
			}
			var hasGet, hasHead bool
			for _, m := range strings.Split(rec.Header().Get("Allow"), ",") {
				switch strings.TrimSpace(m) {
				case http.MethodGet:
					hasGet = true
				case http.MethodHead:
					hasHead = true
				}
			}
			if !hasGet || !hasHead {
				t.Fatalf("%s /healthz Allow = %q, want it to list GET and HEAD", method, rec.Header().Get("Allow"))
			}
		})
	}
}

func TestUnknownPathsReturn404(t *testing.T) {
	assertStatus(t, http.MethodGet, "/", http.StatusNotFound)
	assertStatus(t, http.MethodGet, "/healthz/", http.StatusNotFound)
	assertStatus(t, http.MethodGet, "/healthz/extra", http.StatusNotFound)
	assertStatus(t, http.MethodGet, "/api/system/ping", http.StatusNotFound)
	// An unknown path is a 404 even for a method the router would reject
	// on a known path: path matching outranks method matching.
	assertStatus(t, http.MethodPost, "/not-a-route", http.StatusNotFound)
}

func assertStatus(t *testing.T, method, path string, want int) {
	t.Helper()
	rec := httptest.NewRecorder()
	httpserver.NewHandler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))

	if rec.Code != want {
		t.Fatalf("%s %s status = %d, want %d", method, path, rec.Code, want)
	}
}

// TestHealthzOverRealServer repeats the core /healthz contract through a
// real net/http server and client, verifying the on-the-wire semantics a
// process supervisor or curl would observe.
func TestHealthzOverRealServer(t *testing.T) {
	srv := httptest.NewServer(httpserver.NewHandler())
	defer srv.Close()

	getResp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	getBody, err := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if err != nil {
		t.Fatalf("GET /healthz read body: %v", err)
	}
	if getResp.StatusCode != http.StatusOK || string(getBody) != "ok\n" {
		t.Fatalf("GET /healthz = %d %q, want %d %q", getResp.StatusCode, getBody, http.StatusOK, "ok\n")
	}
	if ct := getResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("GET /healthz Content-Type = %q, want prefix %q", ct, "text/plain")
	}

	headResp, err := http.Head(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("HEAD /healthz: %v", err)
	}
	headBody, err := io.ReadAll(headResp.Body)
	headResp.Body.Close()
	if err != nil {
		t.Fatalf("HEAD /healthz read body: %v", err)
	}
	if headResp.StatusCode != http.StatusOK || len(headBody) != 0 {
		t.Fatalf("HEAD /healthz = %d %q, want %d with empty body", headResp.StatusCode, headBody, http.StatusOK)
	}

	postResp, err := http.Post(srv.URL+"/healthz", "text/plain", strings.NewReader("ping"))
	if err != nil {
		t.Fatalf("POST /healthz: %v", err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status = %d, want %d", postResp.StatusCode, http.StatusMethodNotAllowed)
	}
	// A client reading the header with the standard Header.Get must see the
	// complete method list in one line.
	if allow := postResp.Header.Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("POST /healthz Allow = %q, want %q", allow, "GET, HEAD")
	}
}
