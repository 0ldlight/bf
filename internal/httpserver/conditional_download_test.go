package httpserver_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/repository"
)

const conditionalDate = "Wed, 01 May 2024 01:02:03 GMT"

// The seven frozen samples keep target wildcard behavior and validator precedence.
// 七个冻结样本保留目标的通配值行为和条件优先级。
func TestConditionalDownloadFrozenSamples(t *testing.T) {
	body := []byte("controlled conditional fixture")
	source := conditionalSource(t, body)
	etag := source.Reference.Content.SHA1
	for _, tc := range []struct {
		name   string
		header http.Header
		status int
	}{
		{"exact ETag", http.Header{"If-None-Match": {etag}}, 304},
		{"wildcard is miss", http.Header{"If-None-Match": {"*"}}, 200},
		{"weak ETag", http.Header{"If-None-Match": {"W/" + etag}}, 304},
		{"quoted miss", http.Header{"If-None-Match": {`"binflow-missing"`}}, 200},
		{"date equality", http.Header{"If-Modified-Since": {conditionalDate}}, 304},
		{"old date", http.Header{"If-Modified-Since": {"Thu, 01 Jan 1970 00:00:00 GMT"}}, 200},
		{"ETag miss precedes matching date", http.Header{"If-None-Match": {`"binflow-missing"`}, "If-Modified-Since": {conditionalDate}}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conditionalAssertRequest(t, source, body, tc.header, tc.status)
		})
	}
}

func TestConditionalDownloadAdmittedBoundaries(t *testing.T) {
	body := []byte("opaque fixture")
	source := conditionalSource(t, body)
	etag := source.Reference.Content.SHA1
	for _, tc := range []struct {
		name   string
		header http.Header
		status int
	}{
		{"lowercase alias", http.Header{"if-none-match": {etag}}, 304},
		{"mixed case alias", http.Header{"iF-MoDiFiEd-SiNcE": {conditionalDate}}, 304},
		{"surrounding spaces", http.Header{"If-None-Match": {"  " + etag + "  "}}, 304},
		{"quoted exact", http.Header{"If-None-Match": {`"` + etag + `"`}}, 304},
		{"weak quoted exact", http.Header{"If-None-Match": {`W/"` + etag + `"`}}, 304},
		{"case sensitive token miss", http.Header{"If-None-Match": {strings.ToUpper(etag)}}, 200},
		{"token punctuation", http.Header{"If-None-Match": {"abc_def.foo-bar*"}}, 200},
		{"128 byte token", http.Header{"If-None-Match": {strings.Repeat("a", 128)}}, 200},
		{"128 byte weak quoted token", http.Header{"If-None-Match": {`W/"` + strings.Repeat("a", 124) + `"`}}, 200},
		{"future canonical date", http.Header{"If-Modified-Since": {"Fri, 31 Dec 9999 23:59:59 GMT"}}, 304},
		{"one second earlier", http.Header{"If-Modified-Since": {"Wed, 01 May 2024 01:02:02 GMT"}}, 200},
		{"ETag match precedes older date", http.Header{"If-None-Match": {etag}, "If-Modified-Since": {"Thu, 01 Jan 1970 00:00:00 GMT"}}, 304},
		{"wildcard precedes matching date", http.Header{"If-None-Match": {"*"}, "If-Modified-Since": {conditionalDate}}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conditionalAssertRequest(t, source, body, tc.header, tc.status)
		})
	}
}

func TestConditionalDownloadMalformedHeadersRejectBeforeIO(t *testing.T) {
	source := conditionalSource(t, []byte("payload"))
	etag := source.Reference.Content.SHA1
	cases := []struct {
		name   string
		header http.Header
	}{
		{"nil ETag values", http.Header{"If-None-Match": nil}},
		{"empty ETag values", http.Header{"If-None-Match": {}}},
		{"duplicate ETags", http.Header{"If-None-Match": {etag, etag}}},
		{"duplicate ETag aliases", http.Header{"If-None-Match": {etag}, "if-none-match": {etag}}},
		{"nil ETag alias beside valid alias", http.Header{"If-None-Match": nil, "if-none-match": {etag}}},
		{"nil date values", http.Header{"If-Modified-Since": nil}},
		{"empty date values", http.Header{"If-Modified-Since": {}}},
		{"duplicate dates", http.Header{"If-Modified-Since": {conditionalDate, conditionalDate}}},
		{"duplicate date aliases", http.Header{"If-Modified-Since": {conditionalDate}, "if-modified-since": {conditionalDate}}},
		{"empty date alias beside valid alias", http.Header{"If-Modified-Since": {}, "if-modified-since": {conditionalDate}}},
		{"valid ETag cannot hide invalid date", http.Header{"If-None-Match": {etag}, "If-Modified-Since": {"invalid"}}},
		{"valid ETag cannot hide empty date", http.Header{"If-None-Match": {etag}, "If-Modified-Since": nil}},
		{"valid date cannot hide invalid ETag", http.Header{"If-None-Match": {"a,b"}, "If-Modified-Since": {conditionalDate}}},
		{"request Last-Modified modifier", http.Header{"If-None-Match": {etag}, "lAsT-mOdIfIeD": nil}},
		{"request MD5 modifier", http.Header{"If-None-Match": {etag}, "x-artifactory-if-none-match-md5": {}}},
		{"date with Last-Modified modifier", http.Header{"If-Modified-Since": {conditionalDate}, "Last-Modified": {conditionalDate}}},
	}
	for _, value := range []string{"", "   ", "a,b", "a b", "a\tb", "a\r\nb", "a\x00b", "é", "a/b", `"`, `"a`, `a"`, `a"b`, `""`, `W/`, `W/W/` + etag, `w/` + etag, `W/"a\"b"`, strings.Repeat("a", 129), strings.Repeat(" ", 90) + etag} {
		cases = append(cases, struct {
			name   string
			header http.Header
		}{"ETag " + strconv.Quote(value), http.Header{"If-None-Match": {value}}})
	}
	for _, value := range []string{"", conditionalDate + " ", " " + conditionalDate, "Thu, 01 May 2024 01:02:03 GMT", "Wed, 01 May 2024 01:02:03 UTC", "Wednesday, 01-May-24 01:02:03 GMT", "Wed May  1 01:02:03 2024", "Wed, 31 Dec 1969 23:59:59 GMT", "Sat, 01 Jan 10000 00:00:00 GMT", "Wed, 01 May 2024 01:02:03\x00GMT"} {
		cases = append(cases, struct {
			name   string
			header http.Header
		}{"date " + strconv.Quote(value), http.Header{"If-Modified-Since": {value}}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth, store, opener := &duAuth{}, &duStore{source: source}, &duOpener{}
			rec := httptest.NewRecorder()
			r := conditionalRequest(http.MethodGet, tc.header)
			duHandler(t, auth, store, opener, nil).ServeHTTP(rec, r)
			duAssertFixedError(t, rec, http.StatusNotImplemented, false)
			if auth.calls != 0 || store.calls != 0 || opener.opens != 0 {
				t.Fatalf("rejected header reached auth=%d lookup=%d open=%d", auth.calls, store.calls, opener.opens)
			}
		})
	}
}

func TestConditionalDownloadExcludedFeaturesRemain501(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		header http.Header
	}{
		{"HEAD ETag", http.MethodHead, http.Header{"If-None-Match": {"token"}}},
		{"HEAD date", http.MethodHead, http.Header{"If-Modified-Since": {conditionalDate}}},
		{"Range", http.MethodGet, http.Header{"Range": {"bytes=0-3"}}},
		{"Range with ETag", http.MethodGet, http.Header{"Range": {"bytes=0-3"}, "If-None-Match": {"token"}}},
		{"If-Match", http.MethodGet, http.Header{"If-Match": {"token"}}},
		{"If-Range", http.MethodGet, http.Header{"If-Range": {"token"}}},
		{"other If prefix", http.MethodGet, http.Header{"iF-fUtUrE": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, store, opener := &duAuth{}, &duStore{}, &duOpener{}
			rec := httptest.NewRecorder()
			duHandler(t, auth, store, opener, nil).ServeHTTP(rec, conditionalRequest(tc.method, tc.header))
			duAssertFixedError(t, rec, http.StatusNotImplemented, tc.method == http.MethodHead)
			if auth.calls != 0 || store.calls != 0 || opener.opens != 0 {
				t.Fatalf("unsupported feature reached auth=%d lookup=%d open=%d", auth.calls, store.calls, opener.opens)
			}
		})
	}
}

func TestConditionalDownloadPlainGETKeepsIgnoredModifiers(t *testing.T) {
	body := []byte("plain payload")
	source := conditionalSource(t, body)
	for _, header := range []http.Header{
		{"Last-Modified": {"not-a-date"}},
		{"X-Artifactory-If-None-Match-MD5": nil},
		{"last-modified": {}, "x-artifactory-if-none-match-md5": {"true"}},
	} {
		conditionalAssertRequest(t, source, body, header, http.StatusOK)
	}
}

func TestConditionalDownloadAuthorizationBeforeLookup(t *testing.T) {
	body := []byte("private payload")
	source := conditionalSource(t, body)
	for _, tc := range []struct {
		name   string
		result httpserver.DownloadAuthResult
		err    error
		status int
	}{
		{"unauthenticated", httpserver.DownloadAuthResult{Decision: httpserver.DownloadUnauthenticated, Challenge: `Basic realm="conditional"`}, nil, 401},
		{"denied", httpserver.DownloadAuthResult{Decision: httpserver.DownloadDenied}, nil, 403},
		{"zero auth decision", httpserver.DownloadAuthResult{}, nil, 500},
		{"allow with challenge", httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow, Challenge: "unexpected"}, nil, 500},
		{"denied with challenge", httpserver.DownloadAuthResult{Decision: httpserver.DownloadDenied, Challenge: "unexpected"}, nil, 500},
		{"missing challenge", httpserver.DownloadAuthResult{Decision: httpserver.DownloadUnauthenticated}, nil, 500},
		{"auth backend failure", httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}, errors.New("controlled auth failure"), 500},
		{"auth backend deadline with live request", httpserver.DownloadAuthResult{}, context.DeadlineExceeded, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &duAuth{result: tc.result, err: tc.err}
			store, opener := &duStore{source: source}, &duOpener{}
			rec := httptest.NewRecorder()
			duHandler(t, auth, store, opener, nil).ServeHTTP(rec, conditionalRequest(http.MethodGet, http.Header{"If-None-Match": {source.Reference.Content.SHA1}}))
			duAssertFixedError(t, rec, tc.status, false)
			if auth.calls != 1 || store.calls != 0 || opener.opens != 0 || rec.Header().Get("ETag") != "" {
				t.Fatalf("denial auth=%d lookup=%d open=%d ETag=%q", auth.calls, store.calls, opener.opens, rec.Header().Get("ETag"))
			}
			wantChallenge := ""
			if tc.status == http.StatusUnauthorized {
				wantChallenge = tc.result.Challenge
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != wantChallenge {
				t.Fatalf("WWW-Authenticate=%q, want %q", got, wantChallenge)
			}
		})
	}
}

// Unix epoch is a real validator value; subsecond metadata compares by seconds.
// Unix epoch 是有效验证器；亚秒元数据必须按秒比较。
func TestConditionalDownloadModifiedTimeBoundaries(t *testing.T) {
	body := []byte("timestamp boundary payload")
	for _, tc := range []struct {
		name        string
		modified    int64
		requestDate string
		status      int
	}{
		{"epoch equality", 0, "Thu, 01 Jan 1970 00:00:00 GMT", 304},
		{"epoch subsecond equality", 999, "Thu, 01 Jan 1970 00:00:00 GMT", 304},
		{"first second is newer", 1000, "Thu, 01 Jan 1970 00:00:00 GMT", 200},
		{"maximum stored year equality", 253402300799999, "Fri, 31 Dec 9999 23:59:59 GMT", 304},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := conditionalSource(t, body)
			metadata := repository.NewFileMetadata(repository.FileMetadataInput{ModifiedMillis: tc.modified})
			source.Reference.Metadata = &metadata
			conditionalAssertRequest(t, source, body, http.Header{"If-Modified-Since": {tc.requestDate}}, tc.status)
		})
	}
}

func TestConditionalDownloadSourceValidationPrecedesDecision(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*repository.FileSnapshotSource)
	}{
		{"zero ID", func(s *repository.FileSnapshotSource) { s.Reference.ID = 0 }},
		{"wrong identity", func(s *repository.FileSnapshotSource) { s.Reference.Path = duPath(t, duRepoKey, "dir", "other.bin") }},
		{"missing metadata", func(s *repository.FileSnapshotSource) { s.Reference.Metadata = nil }},
		{"NULL SHA1", func(s *repository.FileSnapshotSource) { s.SHA1ActualValid = false }},
		{"NULL MD5", func(s *repository.FileSnapshotSource) { s.MD5ActualValid = false }},
		{"NULL SHA256", func(s *repository.FileSnapshotSource) { s.SHA256ActualValid = false }},
		{"uppercase SHA1", func(s *repository.FileSnapshotSource) {
			s.Reference.Content.SHA1 = strings.ToUpper(s.Reference.Content.SHA1)
		}},
		{"nonhex SHA1", func(s *repository.FileSnapshotSource) { s.Reference.Content.SHA1 = strings.Repeat("z", 40) }},
		{"negative size", func(s *repository.FileSnapshotSource) { s.Reference.Content.Size = -1 }},
		{"empty SHA1 with content", func(s *repository.FileSnapshotSource) { s.Reference.Content.SHA1 = duEmptySHA1 }},
		{"negative modified time", func(s *repository.FileSnapshotSource) {
			m := repository.NewFileMetadata(repository.FileMetadataInput{ModifiedMillis: -1})
			s.Reference.Metadata = &m
		}},
		{"modified time over maximum", func(s *repository.FileSnapshotSource) {
			m := repository.NewFileMetadata(repository.FileMetadataInput{ModifiedMillis: 253402300800000})
			s.Reference.Metadata = &m
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := conditionalSource(t, []byte("payload"))
			tc.edit(&source)
			auth := &duAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
			store, opener := &duStore{source: source}, &duOpener{}
			rec := httptest.NewRecorder()
			duHandler(t, auth, store, opener, nil).ServeHTTP(rec, conditionalRequest(http.MethodGet, http.Header{"If-None-Match": {source.Reference.Content.SHA1}}))
			duAssertFixedError(t, rec, http.StatusInternalServerError, false)
			if auth.calls != 1 || store.calls != 1 || opener.opens != 0 {
				t.Fatalf("validation auth=%d lookup=%d open=%d", auth.calls, store.calls, opener.opens)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"missing", sql.ErrNoRows, 404},
		{"storage failure", errors.New("controlled storage failure"), 500},
		{"dependency deadline with live request", context.DeadlineExceeded, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, opener := &duStore{err: tc.err}, &duOpener{}
			rec := httptest.NewRecorder()
			duHandler(t, duAllowAuth{}, store, opener, nil).ServeHTTP(rec, conditionalRequest(http.MethodGet, http.Header{"If-None-Match": {"token"}}))
			duAssertFixedError(t, rec, tc.status, false)
			if store.calls != 1 || opener.opens != 0 {
				t.Fatalf("lookups=%d opens=%d", store.calls, opener.opens)
			}
		})
	}
}

func TestConditionalDownloadCancellation(t *testing.T) {
	for _, point := range []string{"before auth", "after lookup"} {
		t.Run(point, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := conditionalSource(t, []byte("payload"))
			store := &conditionalStore{sources: []repository.FileSnapshotSource{source}}
			if point == "before auth" {
				cancel()
			} else {
				store.after = cancel
			}
			auth := &duAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
			opener := &duOpener{}
			rec := httptest.NewRecorder()
			r := conditionalRequest(http.MethodGet, http.Header{"If-None-Match": {source.Reference.Content.SHA1}}).WithContext(ctx)
			aborted := false
			func() {
				defer func() {
					if v := recover(); v != nil {
						if v != http.ErrAbortHandler {
							t.Fatalf("unexpected panic %v", v)
						}
						aborted = true
					}
				}()
				duHandler(t, auth, store, opener, nil).ServeHTTP(rec, r)
			}()
			if !aborted || rec.Body.Len() != 0 || rec.Header().Get("ETag") != "" || opener.opens != 0 {
				t.Fatalf("aborted=%t body=%q ETag=%q opens=%d", aborted, rec.Body.String(), rec.Header().Get("ETag"), opener.opens)
			}
			wantAuth, wantLookups := 0, 0
			if point == "after lookup" {
				wantAuth, wantLookups = 1, 1
			}
			if auth.calls != wantAuth || store.calls != wantLookups {
				t.Fatalf("auth=%d lookup=%d", auth.calls, store.calls)
			}
		})
	}
}

func TestConditionalDownloadMissUsesSingleSource(t *testing.T) {
	firstBody, nextBody := []byte("first content"), []byte("other content")
	first, next := conditionalSource(t, firstBody), conditionalSource(t, nextBody)
	store := &conditionalStore{sources: []repository.FileSnapshotSource{first, next}}
	body := &conditionalBody{Reader: bytes.NewReader(firstBody)}
	opener := &duOpener{body: body}
	rec := httptest.NewRecorder()
	duHandler(t, duAllowAuth{}, store, opener, nil).ServeHTTP(rec, conditionalRequest(http.MethodGet, http.Header{"If-None-Match": {"not-matching"}}))
	conditionalAssertResponse(t, rec, first, firstBody, http.StatusOK)
	if store.calls != 1 || opener.opens != 1 || opener.lastKey != first.Reference.Content.SHA1 || body.closes != 1 {
		t.Fatalf("lookups=%d opens=%d key=%q closes=%d", store.calls, opener.opens, opener.lastKey, body.closes)
	}
}

func TestConditionalDownloadNextRequestSeesChangedSource(t *testing.T) {
	firstBody, nextBody := []byte("first content"), []byte("next content")
	first, next := conditionalSource(t, firstBody), conditionalSource(t, nextBody)
	m := repository.NewFileMetadata(repository.FileMetadataInput{ModifiedMillis: first.Reference.Metadata.ModifiedMillis() + 2000})
	next.Reference.Metadata = &m
	store := &duStore{source: first}
	opener := &duOpener{err: errors.New("matching conditions must not open content")}
	handler := duHandler(t, duAllowAuth{}, store, opener, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, conditionalRequest(http.MethodGet, http.Header{"If-None-Match": {first.Reference.Content.SHA1}}))
	conditionalAssertResponse(t, rec, first, nil, http.StatusNotModified)
	store.source = next
	body := &conditionalBody{Reader: bytes.NewReader(nextBody)}
	opener.body, opener.err = body, nil
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, conditionalRequest(http.MethodGet, http.Header{"If-None-Match": {first.Reference.Content.SHA1}}))
	conditionalAssertResponse(t, rec, next, nextBody, http.StatusOK)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, conditionalRequest(http.MethodGet, http.Header{"If-None-Match": {next.Reference.Content.SHA1}}))
	conditionalAssertResponse(t, rec, next, nil, http.StatusNotModified)
	dateBody := &conditionalBody{Reader: bytes.NewReader(nextBody)}
	opener.body = dateBody
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, conditionalRequest(http.MethodGet, http.Header{"If-Modified-Since": {conditionalDate}}))
	conditionalAssertResponse(t, rec, next, nextBody, http.StatusOK)
	rec = httptest.NewRecorder()
	newDate := time.UnixMilli(next.Reference.Metadata.ModifiedMillis()).UTC().Format(http.TimeFormat)
	handler.ServeHTTP(rec, conditionalRequest(http.MethodGet, http.Header{"If-Modified-Since": {newDate}}))
	conditionalAssertResponse(t, rec, next, nil, http.StatusNotModified)
	if store.calls != 5 || opener.opens != 2 || body.closes != 1 || dateBody.closes != 1 {
		t.Fatalf("lookups=%d opens=%d firstCloses=%d dateCloses=%d", store.calls, opener.opens, body.closes, dateBody.closes)
	}
}

func TestConditionalDownloadRealHTTP304(t *testing.T) {
	source := conditionalSource(t, []byte("wire content"))
	store, opener := &conditionalAtomicStore{source: source}, &conditionalAtomicOpener{}
	server := httptest.NewServer(duHandler(t, duAllowAuth{}, store, opener, nil))
	defer server.Close()
	client := server.Client()
	client.Timeout = 2 * time.Second
	r, err := http.NewRequest(http.MethodGet, server.URL+"/artifactory/"+duRepoKey+"/dir/payload.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("If-None-Match", source.Reference.Content.SHA1)
	res, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 304 || len(body) != 0 {
		t.Fatalf("status=%d body=%q", res.StatusCode, body)
	}
	conditionalAssertHeaders(t, res.Header, source, 304)
	if store.calls.Load() != 1 || opener.calls.Load() != 0 {
		t.Fatalf("lookups=%d opens=%d", store.calls.Load(), opener.calls.Load())
	}
}

func conditionalSource(t *testing.T, body []byte) repository.FileSnapshotSource {
	t.Helper()
	path := duPath(t, duRepoKey, "dir", "payload.bin")
	m := content.Metadata{SHA1: fmt.Sprintf("%x", sha1.Sum(body)), MD5: fmt.Sprintf("%x", md5.Sum(body)), SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Size: int64(len(body))}
	return duSource(path, time.Date(2024, 5, 1, 1, 2, 3, 987000000, time.UTC).UnixMilli(), m)
}

func conditionalRequest(method string, header http.Header) *http.Request {
	r := duRawRequest(method, "/artifactory/"+duRepoKey+"/dir/payload.bin")
	r.Header = header.Clone()
	return r
}

func conditionalAssertRequest(t *testing.T, source repository.FileSnapshotSource, payload []byte, header http.Header, status int) {
	t.Helper()
	auth := &duAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
	store := &duStore{source: source}
	body := &conditionalBody{Reader: bytes.NewReader(payload)}
	opener := &duOpener{body: body}
	if status == http.StatusNotModified {
		opener.err = errors.New("matching metadata must not open physical content")
	}
	rec := httptest.NewRecorder()
	duHandler(t, auth, store, opener, nil).ServeHTTP(rec, conditionalRequest(http.MethodGet, header))
	wantBody, wantOpen := payload, 1
	if status == http.StatusNotModified {
		wantBody, wantOpen = nil, 0
	}
	conditionalAssertResponse(t, rec, source, wantBody, status)
	if auth.calls != 1 || store.calls != 1 || opener.opens != wantOpen || body.closes != wantOpen {
		t.Fatalf("auth=%d lookup=%d open=%d close=%d, want one auth/lookup and %d open/close", auth.calls, store.calls, opener.opens, body.closes, wantOpen)
	}
}

func conditionalAssertResponse(t *testing.T, rec *httptest.ResponseRecorder, source repository.FileSnapshotSource, body []byte, status int) {
	t.Helper()
	if rec.Code != status || !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("status=%d body=%q, want %d/%q", rec.Code, rec.Body.Bytes(), status, body)
	}
	conditionalAssertHeaders(t, rec.Header(), source, status)
}

func conditionalAssertHeaders(t *testing.T, header http.Header, source repository.FileSnapshotSource, status int) {
	t.Helper()
	ref := source.Reference
	want := map[string]string{
		"ETag": ref.Content.SHA1, "X-Checksum-Sha1": ref.Content.SHA1, "X-Checksum-Md5": ref.Content.MD5,
		"X-Checksum-Sha256":   ref.Content.SHA256,
		"Last-Modified":       time.UnixMilli(ref.Metadata.ModifiedMillis()).UTC().Truncate(time.Second).Format(http.TimeFormat),
		"Content-Disposition": `attachment; filename="payload.bin"; filename*=UTF-8''payload.bin`,
	}
	for key, value := range want {
		if got := header.Get(key); got != value {
			t.Errorf("%s=%q, want %q", key, got, value)
		}
	}
	if header.Get("Accept-Ranges") != "" {
		t.Error("range support must not be advertised")
	}
	if status == http.StatusNotModified {
		for key := range header {
			if strings.EqualFold(key, "Content-Type") || strings.EqualFold(key, "Content-Length") {
				t.Errorf("304 contains forbidden %s", key)
			}
		}
	} else if header.Get("Content-Type") != "application/octet-stream" || header.Get("Content-Length") != strconv.FormatInt(ref.Content.Size, 10) {
		t.Fatalf("200 content headers=%v", header)
	}
}

type conditionalBody struct {
	*bytes.Reader
	closes int
}

func (b *conditionalBody) Close() error { b.closes++; return nil }

type conditionalStore struct {
	sources []repository.FileSnapshotSource
	calls   int
	after   func()
}

func (s *conditionalStore) LookupSnapshotSource(context.Context, repository.Path) (repository.FileSnapshotSource, error) {
	i := s.calls
	s.calls++
	if s.after != nil {
		s.after()
	}
	if i >= len(s.sources) {
		i = len(s.sources) - 1
	}
	return s.sources[i], nil
}

type conditionalAtomicStore struct {
	source repository.FileSnapshotSource
	calls  atomic.Int64
}

func (s *conditionalAtomicStore) LookupSnapshotSource(context.Context, repository.Path) (repository.FileSnapshotSource, error) {
	s.calls.Add(1)
	return s.source, nil
}

type conditionalAtomicOpener struct{ calls atomic.Int64 }

func (o *conditionalAtomicOpener) Open(context.Context, string) (io.ReadCloser, error) {
	o.calls.Add(1)
	return nil, errors.New("304 must not open content")
}
