package postgresit

import (
	"database/sql"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/httpserver"
)

// 真实 PUT 建立已提交节点后，通过真实 HTTP/读取池/filestore 验证条件
// GET；独立 SQL 和物理字节检查保证缓存验证没有改写存储事实。
// A real PUT commits the fixture before conditional GET goes through HTTP,
// the read pool and filestore. SQL and physical-byte oracles detect changes
// to stored facts. Test authorization is explicit, not production-auth proof.
func TestConditionalDownloadHTTPValidatorsAndUnchangedStorage(t *testing.T) {
	f := uhpSetup(t, 1<<20)
	const path = "/artifactory/uhpRepo/dir/cache.txt"
	const payload = "metadata-backed conditional content\n"
	conditionalPut(t, f, path, "dir/cache.txt", payload)
	sha1Hex, _, _ := uhpDigest(payload)
	modified := conditionalDate(uhpMillisOne)
	for _, tc := range []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"raw-etag", map[string]string{"If-None-Match": sha1Hex}, http.StatusNotModified},
		{"weak-raw-etag", map[string]string{"If-None-Match": "W/" + sha1Hex}, http.StatusNotModified},
		{"weak-quoted-etag", map[string]string{"If-None-Match": `W/"` + sha1Hex + `"`}, http.StatusNotModified},
		{"wildcard-is-miss", map[string]string{"If-None-Match": "*"}, http.StatusOK},
		{"missing-etag", map[string]string{"If-None-Match": `"binflow-missing"`}, http.StatusOK},
		{"same-rounded-time", map[string]string{"If-Modified-Since": modified}, http.StatusNotModified},
		{"older-time", map[string]string{"If-Modified-Since": conditionalDate(0)}, http.StatusOK},
		{"future-time", map[string]string{"If-Modified-Since": conditionalDate(uhpMillisOne + 60_000)}, http.StatusNotModified},
		{"etag-miss-has-precedence", map[string]string{"If-None-Match": `"binflow-missing"`, "If-Modified-Since": modified}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conditionalRequireGET(t, f, path, "cache.txt", payload, "text/plain", uhpMillisOne, tc.headers, tc.status)
		})
	}
	usRequireNode(t, f.uhpOracle(), 1, "uhpRepo", "dir", "cache.txt", payload,
		uhpMillisOne, uhpMillisOne, uhpMillisOne, "writer", "writer", sql.NullString{}, sql.NullString{})
	usRequirePublished(t, f.uhpOracle(), payload, 1)
	conditionalRequireCounts(t, f, 1, 1)
}

// 两次写入跨过 HTTP 日期的一秒边界，避免相同 Last-Modified 掩盖覆盖后
// 使用陈旧源的问题；首个 .123 毫秒时间同时覆盖秒级取整的相等判定。
// Writes cross an HTTP-date second boundary so unchanged formatted dates
// cannot hide a stale source after overwrite. The initial subsecond value
// also exercises equality against a rounded modification time.
func TestConditionalDownloadHTTPUsesCurrentOverwrite(t *testing.T) {
	f := uhpSetup(t, 1<<20)
	const path = "/artifactory/uhpRepo/dir/cache.bin"
	const oldPayload = "conditional alpha bytes\n"
	const newPayload = "conditional beta bytes with a longer tail\n"
	conditionalPut(t, f, path, "dir/cache.bin", oldPayload)
	oldSHA1, _, _ := uhpDigest(oldPayload)
	usRequirePublished(t, f.uhpOracle(), oldPayload, 1)
	const overwrittenMillis = uhpMillisTwo + 2_000
	f.clock.set(overwrittenMillis)
	f.auth.configure(httpserver.UploadAllow, "", mdUnits("second"))
	conditionalPut(t, f, path, "dir/cache.bin", newPayload)
	newSHA1, _, _ := uhpDigest(newPayload)
	for _, tc := range []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"old-etag-gets-current-bytes", map[string]string{"If-None-Match": oldSHA1}, http.StatusOK},
		{"current-etag-is-not-modified", map[string]string{"If-None-Match": newSHA1}, http.StatusNotModified},
		{"old-date-gets-current-bytes", map[string]string{"If-Modified-Since": conditionalDate(uhpMillisOne)}, http.StatusOK},
		{"current-date-is-not-modified", map[string]string{"If-Modified-Since": conditionalDate(overwrittenMillis)}, http.StatusNotModified},
		{"old-etag-wins-over-current-date", map[string]string{"If-None-Match": oldSHA1, "If-Modified-Since": conditionalDate(overwrittenMillis)}, http.StatusOK},
		{"current-etag-wins-over-old-date", map[string]string{"If-None-Match": newSHA1, "If-Modified-Since": conditionalDate(0)}, http.StatusNotModified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conditionalRequireGET(t, f, path, "cache.bin", newPayload, "application/octet-stream", overwrittenMillis, tc.headers, tc.status)
		})
	}
	usRequireNode(t, f.uhpOracle(), 1, "uhpRepo", "dir", "cache.bin", newPayload,
		uhpMillisOne, overwrittenMillis, overwrittenMillis, "writer", "second", sql.NullString{}, sql.NullString{})
	conditionalRequireRegistry(t, f, oldPayload)
	conditionalRequireRegistry(t, f, newPayload)
	cfRequireObjectBytes(t, f.store, oldSHA1, oldPayload)
	cfRequireObjectBytes(t, f.store, newSHA1, newPayload)
	conditionalRequireCounts(t, f, 2, 2)
}

func TestConditionalDownloadHTTPEmptyStoredFile(t *testing.T) {
	f := uhpSetup(t, 1<<20)
	const path = "/artifactory/uhpRepo/dir/empty.txt"
	conditionalPut(t, f, path, "dir/empty.txt", "")
	sha1Hex, _, _ := uhpDigest("")
	conditionalRequireGET(t, f, path, "empty.txt", "", "text/plain", uhpMillisOne,
		map[string]string{"If-None-Match": sha1Hex}, http.StatusNotModified)
	conditionalRequireGET(t, f, path, "empty.txt", "", "text/plain", uhpMillisOne,
		map[string]string{"If-None-Match": "*"}, http.StatusOK)
	usRequireNode(t, f.uhpOracle(), 1, "uhpRepo", "dir", "empty.txt", "",
		uhpMillisOne, uhpMillisOne, uhpMillisOne, "writer", "writer", sql.NullString{}, sql.NullString{})
	usRequirePublished(t, f.uhpOracle(), "", 1)
	conditionalRequireCounts(t, f, 1, 1)
}

func conditionalDate(millis int64) string {
	return time.Unix(millis/1000, 0).UTC().Format(http.TimeFormat)
}

func conditionalPut(t *testing.T, f uhpFixture, path, rawPath, payload string) {
	t.Helper()
	status, headers, body := uhpPut(t, f.ts, path, nil, payload, "")
	uhpRequireCreated(t, status, headers, body, "uhpRepo", rawPath, payload,
		uhpMillisOne, "writer", map[string]bool{"sha256": true})
}

func conditionalRequireGET(t *testing.T, f uhpFixture, path, filename, payload, mime string,
	modifiedMillis int64, conditions map[string]string, wantStatus int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range conditions {
		req.Header.Set(key, value)
	}
	status, headers, body := uhpDo(t, f.ts.Client(), req)
	if status != wantStatus {
		t.Fatalf("GET %s with %v status = %d, want %d (body %q)", path, conditions, status, wantStatus, body)
	}
	sha1Hex, sha256Hex, md5Hex := uhpDigest(payload)
	for key, want := range map[string]string{
		"ETag": sha1Hex, "Last-Modified": conditionalDate(modifiedMillis),
		"X-Checksum-Sha1": sha1Hex, "X-Checksum-Sha256": sha256Hex, "X-Checksum-Md5": md5Hex,
		"Content-Disposition": `attachment; filename="` + filename + `"; filename*=UTF-8''` + filename,
	} {
		if got := headers.Get(key); got != want {
			t.Errorf("GET %s with %v %s = %q, want %q", path, conditions, key, got, want)
		}
	}
	if _, present := headers["Accept-Ranges"]; present {
		t.Errorf("GET %s advertises unsupported Accept-Ranges", path)
	}
	if wantStatus == http.StatusNotModified {
		if body != "" {
			t.Errorf("GET %s 304 body = %q, want empty", path, body)
		}
		for _, key := range []string{"Content-Type", "Content-Length"} {
			if values, present := headers[key]; present {
				t.Errorf("GET %s 304 %s = %q, want header absent", path, key, values)
			}
		}
		return
	}
	if body != payload || headers.Get("Content-Type") != mime || headers.Get("Content-Length") != strconv.Itoa(len(payload)) {
		t.Errorf("GET %s 200 representation = (%q,%q,%q), want exact bytes, %q and length %d",
			path, body, headers.Get("Content-Type"), headers.Get("Content-Length"), mime, len(payload))
	}
}

func conditionalRequireCounts(t *testing.T, f uhpFixture, writes int64, objects int) {
	t.Helper()
	if got := f.saver.calls.Load(); got != writes {
		t.Errorf("conditional reads Saver calls = %d, want %d PUT calls only", got, writes)
	}
	if got := countNodes(t, f.adm, f.schema); got != 1 {
		t.Errorf("conditional reads node rows = %d, want 1", got)
	}
	if got := countRows(t, f.adm, f.schema); got != objects {
		t.Errorf("conditional reads registry rows = %d, want %d", got, objects)
	}
	if got := cfCountFiles(t, f.root); got != objects {
		t.Errorf("conditional reads physical files = %d, want %d", got, objects)
	}
	cfRequireBound(t, f.adm, f.schema, 1)
}

func conditionalRequireRegistry(t *testing.T, f uhpFixture, payload string) {
	t.Helper()
	sha1Hex, sha256Hex, md5Hex := uhpDigest(payload)
	gotSHA1, gotMD5, gotLength, gotSHA256, found := rowOf(t, f.adm, f.schema, sha1Hex)
	if !found || gotSHA1 != sha1Hex || gotMD5 != md5Hex || gotLength != int64(len(payload)) ||
		!gotSHA256.Valid || gotSHA256.String != sha256Hex {
		t.Errorf("conditional read registry = (%q,%q,%d,%v,%t), want unchanged content facts",
			gotSHA1, gotMD5, gotLength, gotSHA256, found)
	}
}
