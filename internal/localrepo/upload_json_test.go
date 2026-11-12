package localrepo

import (
	"errors"
	"strings"
	"testing"
)

// JSON is a filename profile, not a content parser. Input and loaded-source
// validation must admit the same identity without weakening existing bounds.
// JSON 仅扩展文件名画像；输入与加载事实必须接受同一身份并保留原有边界。
func TestUploadJSONProfile(t *testing.T) {
	for _, name := range []string{"a.json", "nested/config.json", ".txt.json", "a.json.txt", strings.Repeat("a", 250) + ".json"} {
		t.Run(name, func(t *testing.T) {
			path, err := NewUploadPath("repo", name)
			if err != nil {
				t.Fatalf("JSON upload identity rejected: %v", err)
			}
			if err := ValidateUploadInput(UploadInput{Path: path, OperatorUTF16: uploadTestUnits("writer"), DeclaredSize: 9}); err != nil {
				t.Fatalf("JSON input rejected: %v", err)
			}
			if err := ValidateUploadSource(uploadTestBaseSource(path), path); err != nil {
				t.Fatalf("JSON stored source rejected: %v", err)
			}
		})
	}
	for _, name := range []string{"a.JSON", "a.Json", "a.jsonl", ".json", "a/.json", "a.json/", "a%2Ejson", "a/../b.json", strings.Repeat("a", 251) + ".json"} {
		t.Run("reject-"+name, func(t *testing.T) {
			if _, err := NewUploadPath("repo", name); !errors.Is(err, ErrInvalidUploadInput) {
				t.Fatalf("out-of-profile identity error = %v", err)
			}
		})
	}
}
