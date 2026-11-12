package postgresit

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/0ldlight/bf/internal/httpserver"
)

// JSON 制品仍是原样字节：格式、空正文及最终扩展名分别验证，HTTP/SQL/
// filestore 使用独立 oracle，不把测试写授权端口当作真实权限证明。
// JSON artifacts remain opaque bytes. Format, empty content and the final
// suffix are checked through HTTP, SQL and filestore oracles; the fixture's
// test authorization port is not evidence of production authorization.
func TestUploadHTTPJSONOpaqueRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, file, payload, mime string
	}{
		{"valid", "valid.json", "{\n  \"message\": \"你好\", \"items\": [1,true,null]\n}\n", "application/json"},
		{"empty", "empty.json", "", "application/json"},
		{"opaque-invalid-json", "opaque.json", "not JSON\x00\xff\r\n", "application/json"},
		{"final-json", "a.txt.json", "false\n", "application/json"},
		{"final-txt", "a.json.txt", "{\"text\":true}\n", "text/plain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := uhpSetup(t, 1<<20)
			path := "/artifactory/uhpRepo/dir/" + tc.file
			status, headers, body := uhpPut(t, f.ts, path, jsonUploadHeaders(tc.payload), tc.payload, "attacker.example")
			jsonUploadRequireCreated(t, status, headers, body, "dir/"+tc.file, tc.payload, tc.mime)
			jsonUploadRequireReadback(t, f, path, tc.payload, tc.mime)
			sha1Hex, _, md5Hex := uhpDigest(tc.payload)
			usRequireNode(t, f.uhpOracle(), 1, "uhpRepo", "dir", tc.file, tc.payload,
				uhpMillisOne, uhpMillisOne, uhpMillisOne, "writer", "writer",
				sql.NullString{String: sha1Hex, Valid: true}, sql.NullString{String: md5Hex, Valid: true})
			usRequirePublished(t, f.uhpOracle(), tc.payload, 1)
			if got := countNodes(t, f.adm, f.schema); got != 1 {
				t.Fatalf("JSON round-trip nodes = %d, want 1", got)
			}
			cfRequireBound(t, f.adm, f.schema, 1)
		})
	}
}

// 相同内容重传不创建新节点/登记/对象；不同内容覆盖保留创建身份并更新
// 实际字节和三摘要，JSON 语法本身不影响持久化。
// Identical replay creates no new node/registration/object. Overwrite keeps
// the creation identity and changes bytes/digests regardless of JSON syntax.
func TestUploadHTTPJSONReplayOverwrite(t *testing.T) {
	f := uhpSetup(t, 1<<20)
	const path = "/artifactory/uhpRepo/dir/state.json"
	const original = "{\"version\":1}\n"
	for range 2 {
		status, headers, body := uhpPut(t, f.ts, path, jsonUploadHeaders(original), original, "")
		jsonUploadRequireCreated(t, status, headers, body, "dir/state.json", original, "application/json")
		jsonUploadRequireReadback(t, f, path, original, "application/json")
		sha1Hex, _, md5Hex := uhpDigest(original)
		usRequireNode(t, f.uhpOracle(), 1, "uhpRepo", "dir", "state.json", original,
			uhpMillisOne, uhpMillisOne, uhpMillisOne, "writer", "writer",
			sql.NullString{String: sha1Hex, Valid: true}, sql.NullString{String: md5Hex, Valid: true})
		usRequirePublished(t, f.uhpOracle(), original, 1)
		cfRequireBound(t, f.adm, f.schema, 1)
	}

	const replacement = "opaque replacement\x00\xff\r\n"
	f.clock.set(uhpMillisTwo)
	f.auth.configure(httpserver.UploadAllow, "", mdUnits("second"))
	status, headers, body := uhpPut(t, f.ts, path, jsonUploadHeaders(replacement), replacement, "")
	jsonUploadRequireCreated(t, status, headers, body, "dir/state.json", replacement, "application/json")
	jsonUploadRequireReadback(t, f, path, replacement, "application/json")
	sha1Hex, sha256Hex, md5Hex := uhpDigest(replacement)
	usRequireNode(t, f.uhpOracle(), 1, "uhpRepo", "dir", "state.json", replacement,
		uhpMillisOne, uhpMillisTwo, uhpMillisTwo, "writer", "second",
		sql.NullString{String: sha1Hex, Valid: true}, sql.NullString{String: md5Hex, Valid: true})
	gotSHA1, gotMD5, gotLength, gotSHA256, found := rowOf(t, f.adm, f.schema, sha1Hex)
	if !found || gotSHA1 != sha1Hex || gotMD5 != md5Hex || gotLength != int64(len(replacement)) ||
		!gotSHA256.Valid || gotSHA256.String != sha256Hex {
		t.Fatalf("JSON overwrite registry = (%q,%q,%d,%v,%t), want replacement digest triple and size",
			gotSHA1, gotMD5, gotLength, gotSHA256, found)
	}
	cfRequireObjectBytes(t, f.store, sha1Hex, replacement)
	oldSHA1, _, _ := uhpDigest(original)
	cfRequireObjectBytes(t, f.store, oldSHA1, original)
	if got := countNodes(t, f.adm, f.schema); got != 1 {
		t.Fatalf("JSON overwrite nodes = %d, want 1", got)
	}
	if got := countRows(t, f.adm, f.schema); got != 2 {
		t.Fatalf("JSON overwrite registry rows = %d, want 2", got)
	}
	if got := cfCountFiles(t, f.root); got != 2 {
		t.Fatalf("JSON overwrite stored files = %d, want 2", got)
	}
	cfRequireBound(t, f.adm, f.schema, 1)
}

func jsonUploadHeaders(payload string) map[string]string {
	sha1Hex, sha256Hex, md5Hex := uhpDigest(payload)
	return map[string]string{"X-Checksum-Sha1": sha1Hex, "X-Checksum-Sha256": sha256Hex, "X-Checksum-Md5": md5Hex}
}

// ItemCreated 的媒体类型与制品本身 MIME 分开核对，避免把 JSON 制品响应
// 错当成 application/json 的普通正文。
// Verify the ItemCreated media type separately from the artifact MIME.
func jsonUploadRequireCreated(t *testing.T, status int, headers http.Header, body, path, payload, mime string) {
	t.Helper()
	if status != http.StatusCreated {
		t.Fatalf("PUT %s status = %d, want 201 (body %q)", path, status, body)
	}
	if got := headers.Get("Content-Type"); got != uhpItemCreated {
		t.Fatalf("PUT %s response Content-Type = %q, want %q", path, got, uhpItemCreated)
	}
	if got := headers.Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Fatalf("PUT %s response Content-Length = %q, want %d", path, got, len(body))
	}
	var view uhpCreatedView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatalf("PUT %s ItemCreated decode = %v", path, err)
	}
	sha1Hex, sha256Hex, md5Hex := uhpDigest(payload)
	wantURI := uhpOrigin + "/artifactory/uhpRepo/" + path
	if view.Repo != "uhpRepo" || view.Path != "/"+path || view.MimeType != mime || view.Size != strconv.Itoa(len(payload)) ||
		view.URI != wantURI || view.DownloadURI != wantURI || headers.Get("Location") != wantURI {
		t.Fatalf("PUT %s ItemCreated identity/MIME = %+v, want trusted identity, size and MIME %q", path, view, mime)
	}
	if view.Checksums.SHA1 != sha1Hex || view.Checksums.SHA256 != sha256Hex || view.Checksums.MD5 != md5Hex ||
		headers.Get("X-Checksum-Sha256") != sha256Hex {
		t.Fatalf("PUT %s checksums = %+v, want independent digest triple", path, view.Checksums)
	}
	if len(view.Original) != 3 {
		t.Fatalf("PUT %s original checksum count = %d, want 3", path, len(view.Original))
	}
	for key, want := range map[string]string{"sha1": sha1Hex, "sha256": sha256Hex, "md5": md5Hex} {
		var got string
		if err := json.Unmarshal(view.Original[key], &got); err != nil || got != want {
			t.Fatalf("PUT %s original %s = %s, want %q", path, key, view.Original[key], want)
		}
	}
}

func jsonUploadRequireReadback(t *testing.T, f uhpFixture, path, payload, mime string) {
	t.Helper()
	uhpRequireGET(t, f, path, payload, mime)
	req, err := http.NewRequest(http.MethodHead, f.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, headers, body := uhpDo(t, f.ts.Client(), req)
	if status != http.StatusOK || body != "" {
		t.Fatalf("HEAD %s = (%d,%q), want (200,empty)", path, status, body)
	}
	sha1Hex, sha256Hex, md5Hex := uhpDigest(payload)
	for name, want := range map[string]string{
		"Content-Type": mime, "Content-Length": strconv.Itoa(len(payload)), "ETag": sha1Hex,
		"X-Checksum-Sha1": sha1Hex, "X-Checksum-Sha256": sha256Hex, "X-Checksum-Md5": md5Hex,
	} {
		if got := headers.Get(name); got != want {
			t.Fatalf("HEAD %s %s = %q, want %q", path, name, got, want)
		}
	}
}
