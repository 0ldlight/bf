package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/0ldlight/bf/internal/httpserver"
)

// 有效条件请求仍须经过真实授权决定，不能在匹配前绕过拒绝。
// An admitted validator must still obey the authorization decision.
func TestConditionalDownloadDeniedBeforeLookup(t *testing.T) {
	auth := &duAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadDenied}}
	store, opener := &duStore{}, &duOpener{}
	h := duHandler(t, auth, store, opener, nil)
	r := duRawRequest(http.MethodGet, "/artifactory/"+duRepoKey+"/a.bin")
	r.Header.Set("If-None-Match", "observed-validator")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	duAssertFixedError(t, w, http.StatusForbidden, false)
	if auth.calls != 1 || store.calls != 0 || opener.opens != 0 {
		t.Fatalf("authorization/lookup/open calls = %d/%d/%d, want 1/0/0", auth.calls, store.calls, opener.opens)
	}
}
