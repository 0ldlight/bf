package httpserver_test

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/localrepo"
)

// JSON MIME follows the final suffix; Generic artifacts remain opaque bytes.
// JSON MIME 取最终后缀；Generic 制品正文仍为不解析的原始字节。
func TestJSONDownloadRepresentations(t *testing.T) {
	for _, tc := range jsonTransferCases() {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(tc.label+"/"+method, func(t *testing.T) {
				path := duPath(t, duRepoKey, "dir", tc.name)
				source := duSource(path, 1714500000456, jsonTransferMetadata(tc.body))
				store := &duStore{source: source}
				body := &duBody{data: tc.body}
				opener := &duOpener{body: body}
				auth := &duAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
				rec := httptest.NewRecorder()
				duHandler(t, auth, store, opener, nil).ServeHTTP(rec,
					duRawRequest(method, "/artifactory/"+duRepoKey+"/dir/"+tc.name))
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d, want 200", rec.Code)
				}
				if got := rec.Header().Get("Content-Type"); got != tc.mime {
					t.Fatalf("Content-Type=%q, want %q", got, tc.mime)
				}
				if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(tc.body)) {
					t.Fatalf("Content-Length=%q, want %d", got, len(tc.body))
				}
				if got := rec.Header().Get("X-Checksum-Sha256"); got != source.Reference.Content.SHA256 {
					t.Fatalf("checksum=%q, want %q", got, source.Reference.Content.SHA256)
				}
				wantBody, wantOpens := tc.body, 1
				if method == http.MethodHead {
					wantBody, wantOpens = nil, 0
				} else if len(tc.body) == 0 {
					wantOpens = 0 // Exact-empty content uses the existing synthetic stream.
				}
				if !bytes.Equal(rec.Body.Bytes(), wantBody) || opener.opens != wantOpens {
					t.Fatalf("body=%x opens=%d, want body=%x opens=%d", rec.Body.Bytes(), opener.opens, wantBody, wantOpens)
				}
				if auth.calls != 1 || store.calls != 1 || (wantOpens == 1 && !body.closed) {
					t.Fatalf("auth=%d lookups=%d bodyClosed=%t", auth.calls, store.calls, body.closed)
				}
			})
		}
	}
}

func TestJSONUploadCreatedRepresentation(t *testing.T) {
	for _, tc := range jsonTransferCases() {
		t.Run(tc.label, func(t *testing.T) {
			path := duPath(t, upRepoKey, "dir", tc.name)
			source := upSource(localrepo.UploadInput{Path: path}, nil, nil)
			source.Reference.Content = jsonTransferMetadata(tc.body)
			auth := upAllowAuth()
			saver := &upSaver{source: source, consume: func(r io.Reader) error {
				got, err := io.ReadAll(r)
				if err != nil || !bytes.Equal(got, tc.body) {
					t.Fatalf("saved body=%x error=%v, want %x", got, err, tc.body)
				}
				return nil
			}}
			rec, aborted := upServe(t, upDeps(auth, saver, 128), nil,
				upReq(http.MethodPut, "/artifactory/"+upRepoKey+"/dir/"+tc.name,
					io.NopCloser(bytes.NewReader(tc.body)), int64(len(tc.body))))
			if aborted || rec.Code != http.StatusCreated {
				t.Fatalf("aborted=%t status=%d, want 201", aborted, rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != upMediaType {
				t.Fatalf("ItemCreated Content-Type=%q, want %q", got, upMediaType)
			}
			var doc struct {
				MimeType string `json:"mimeType"`
				Path     string `json:"path"`
				Size     string `json:"size"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.MimeType != tc.mime || doc.Path != "/dir/"+tc.name || doc.Size != strconv.Itoa(len(tc.body)) {
				t.Fatalf("ItemCreated=%+v, want mime=%q path=%q size=%d", doc, tc.mime, "/dir/"+tc.name, len(tc.body))
			}
			if auth.calls != 1 || saver.calls != 1 || saver.lastInput.DeclaredSize != int64(len(tc.body)) {
				t.Fatalf("auth=%d saves=%d declared=%d", auth.calls, saver.calls, saver.lastInput.DeclaredSize)
			}
		})
	}
}

// Rejected suffixes stop before authorization, lookup, content open or save.
// 域外后缀必须在授权、查询、内容打开与保存之前拒绝。
func TestJSONSuffixExclusionsRemainBeforeIO(t *testing.T) {
	for _, name := range []string{".json", "record.JSON", "record.Json", "record.jsonl", "record.json/", "record.json:properties"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut} {
			t.Run(name+"/"+method, func(t *testing.T) {
				if method == http.MethodPut {
					auth, saver := upAllowAuth(), &upSaver{}
					rec, aborted := upServe(t, upDeps(auth, saver, 128), nil,
						upReq(method, "/artifactory/"+upRepoKey+"/dir/"+name,
							io.NopCloser(bytes.NewReader([]byte("opaque"))), 6))
					if aborted || rec.Code != http.StatusBadRequest || auth.calls != 0 || saver.calls != 0 {
						t.Fatalf("aborted=%t status=%d auth=%d saves=%d", aborted, rec.Code, auth.calls, saver.calls)
					}
					return
				}
				auth, store, opener := &duAuth{}, &duStore{}, &duOpener{}
				rec := httptest.NewRecorder()
				duHandler(t, auth, store, opener, nil).ServeHTTP(rec,
					duRawRequest(method, "/artifactory/"+duRepoKey+"/dir/"+name))
				duAssertFixedError(t, rec, http.StatusBadRequest, method == http.MethodHead)
				if auth.calls != 0 || store.calls != 0 || opener.opens != 0 {
					t.Fatalf("auth=%d lookups=%d opens=%d", auth.calls, store.calls, opener.opens)
				}
			})
		}
	}
}

type jsonTransferCase struct {
	label string
	name  string
	mime  string
	body  []byte
}

func jsonTransferCases() []jsonTransferCase {
	opaque := []byte{0, 0xff, '{', 'n', 'o', 't', '-', 'j', 's', 'o', 'n'}
	return []jsonTransferCase{
		{"opaque JSON", "record.json", "application/json", opaque},
		{"last JSON suffix", "record.txt.json", "application/json", opaque},
		{"last text suffix", "record.json.txt", "text/plain", opaque},
		{"empty JSON", "empty.json", "application/json", nil},
		{"valid JSON", "valid.json", "application/json", []byte(`{"ok":true}`)},
	}
}

func jsonTransferMetadata(body []byte) content.Metadata {
	return content.Metadata{
		SHA1: fmt.Sprintf("%x", sha1.Sum(body)), MD5: fmt.Sprintf("%x", md5.Sum(body)),
		SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Size: int64(len(body)),
	}
}
