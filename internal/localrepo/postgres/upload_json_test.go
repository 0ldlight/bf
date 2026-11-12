package postgres

import (
	"testing"
	"time"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/repository"
)

// HTTP projection alone cannot detect a missing save-side MIME entry because
// MIME is not a node SQL column. Verify the actual saver catalog independently.
// MIME 不在节点 SQL 列中，因此单靠 HTTP 表示不足以发现保存目录漏项。
func TestUploadSaverJSONMIME(t *testing.T) {
	db := uploadFakeDB(t, &uploadFakeHub{}, 2)
	saver := uploadTestSaver(t, db, &uploadStubClock{value: 1000}, time.Second)
	for _, tc := range []struct{ name, mime string }{
		{"a.json", "application/json"},
		{"a.txt.json", "application/json"},
		{"a.json.txt", "text/plain"},
		{"a.json.bin", "application/octet-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := repository.NewFileInfoSnapshot(repository.FileInfoSnapshotInput{
				RepoKeyUTF16:  utf16.Encode([]rune("repo")),
				RepoPathUTF16: utf16.Encode([]rune(tc.name)),
				NameUTF16:     utf16.Encode([]rune(tc.name)),
			})
			if err != nil {
				t.Fatal(err)
			}
			materialized, err := repository.MaterializeFileMime(snapshot, saver.catalog)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(utf16.Decode(materialized.Fields().MimeTypeUTF16)); got != tc.mime {
				t.Fatalf("save MIME = %q, want %q", got, tc.mime)
			}
		})
	}
}
