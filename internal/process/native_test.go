package process

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 未知模式不能假就绪为健康服务。
// An unknown mode must not silently become a healthy service.
func TestProcessUnknownModeFailsClosed(t *testing.T) {
	_, err := LoadConfig(func(k string) string {
		if k == "BINFLOW_MODE" {
			return "downlaod"
		}
		return ""
	})
	if !errors.Is(err, ErrConfiguration) {
		t.Fatalf("unknown mode error=%v", err)
	}
}

// 健康入口沿用 net.Listen 的空端口语义，不增加配置拒绝。
// Health mode preserves net.Listen's empty-port compatibility.
func TestProcessHealthEmptyPortCompatible(t *testing.T) {
	cfg, err := LoadConfig(func(k string) string {
		if k == "BINFLOW_LISTEN_ADDR" {
			return "127.0.0.1:"
		}
		return ""
	})
	if err != nil || cfg.ListenAddr != "127.0.0.1:" {
		t.Fatalf("health empty port rejected: %v", err)
	}
}

// 原始编码根前缀不会因 URL.Path 解码而被送入下载处理器。
// A decoded URL.Path cannot turn an encoded raw root into a download route.
func TestProcessRawRouterKeepsEncodedRootOutscope(t *testing.T) {
	calls := 0
	h := newRouter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusTeapot) }))
	r := httptest.NewRequest(http.MethodGet, "/artifactory%2Fgeneric-local/dir/a.bin", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || calls != 0 {
		t.Fatalf("encoded raw root routed to download: status=%d calls=%d", w.Code, calls)
	}
}
