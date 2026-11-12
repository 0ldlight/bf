package httpserver

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/0ldlight/bf/internal/repository"
)

// downloadCondition 只表达已通过本地画像校验的单值 GET 验证器。
// downloadCondition holds only admitted single-value GET validators.
type downloadCondition struct {
	present      bool
	hasETag      bool
	etag         string
	modifiedSecs int64
}

// parseDownloadCondition 在授权前冻结有界输入，不推断完整 RFC 语法。
// parseDownloadCondition bounds inputs before auth without assuming full RFC
// grammar. Presence includes empty-valued and case-aliased map keys.
func parseDownloadCondition(method string, header http.Header) (downloadCondition, bool) {
	var condition downloadCondition
	var etag, modified string
	var hasModified, hasModifier bool
	for key, values := range header {
		switch strings.ToLower(key) {
		case "range":
			return downloadCondition{}, false
		case "if-none-match":
			if condition.hasETag || len(values) != 1 {
				return downloadCondition{}, false
			}
			condition.hasETag = true
			etag = values[0]
		case "if-modified-since":
			if hasModified || len(values) != 1 {
				return downloadCondition{}, false
			}
			hasModified = true
			modified = values[0]
		case "last-modified", "x-artifactory-if-none-match-md5":
			hasModifier = true
		default:
			if strings.HasPrefix(strings.ToLower(key), "if-") {
				return downloadCondition{}, false
			}
		}
	}
	condition.present = condition.hasETag || hasModified
	if !condition.present {
		return condition, true
	}
	if method != http.MethodGet || hasModifier {
		return downloadCondition{}, false
	}
	if condition.hasETag {
		var ok bool
		condition.etag, ok = downloadETagToken(etag)
		if !ok {
			return downloadCondition{}, false
		}
	}
	if hasModified {
		if len(modified) != 29 {
			return downloadCondition{}, false
		}
		date, err := time.Parse(http.TimeFormat, modified)
		if err != nil || date.Year() < 1970 || date.Year() > 9999 || date.Format(http.TimeFormat) != modified {
			return downloadCondition{}, false
		}
		condition.modifiedSecs = date.Unix()
	}
	return condition, true
}

func downloadETagToken(raw string) (string, bool) {
	if len(raw) > 128 {
		return "", false
	}
	token := strings.Trim(raw, " ")
	token = strings.TrimPrefix(token, "W/")
	if strings.HasPrefix(token, `"`) && len(token) >= 2 && strings.HasSuffix(token, `"`) {
		token = token[1 : len(token)-1]
	}
	if token == "" {
		return "", false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '*' || c == '-' {
			continue
		}
		return "", false
	}
	return token, true
}

func (condition downloadCondition) matches(source repository.FileSnapshotSource) bool {
	if condition.hasETag {
		// 原版保存样本的 * 不匹配；不使用 RFC 通配存在性分支。
		// The retained reference's wildcard is not an existence match.
		return condition.etag == source.Reference.Content.SHA1
	}
	return source.Reference.Metadata.ModifiedMillis()/1000 <= condition.modifiedSecs
}

// conditionalDownloadSource 是请求内的同次 Source，不缓存跨请求事实。
// conditionalDownloadSource retains one request's validated source. ReadFile
// performs its existing cancellation/content selection without another DB read.
type conditionalDownloadSource struct {
	source repository.FileSnapshotSource
}

func (s conditionalDownloadSource) LookupSnapshotSource(context.Context, repository.Path) (repository.FileSnapshotSource, error) {
	return s.source, nil
}

func (h *downloadHandler) serveConditionalGet(w http.ResponseWriter, r *http.Request, path repository.Path, condition downloadCondition) {
	source, err := (&validatedDownloadStore{inner: h.store, want: path}).LookupSnapshotSource(r.Context(), path)
	if err != nil {
		h.serveLookupError(w, r, err, nil)
		return
	}
	h.checkCancel(r.Context())
	if condition.matches(source) {
		writeDownloadHeaders(w, source)
		w.Header().Del("Content-Type")
		w.Header().Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.serveGetFrom(w, r, path, conditionalDownloadSource{source: source})
}
