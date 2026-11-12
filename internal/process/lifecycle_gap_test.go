package process

// 本文件覆盖进程生命周期剩余未测行为：serveWithReadTimeout 的参数守卫、
// 封场后 GET/HEAD 的固定 503 表示（HEAD 无正文且不触下游）、http.Server
// ErrorLog 只输出固定类别行。
// This file covers the remaining lifecycle behaviors: the serveWithReadTimeout
// argument guards, the fixed 503 representation for post-seal GET/HEAD (no
// HEAD body, no downstream call) and the http.Server ErrorLog emitting only
// the fixed category line.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestProcessServeWithReadTimeoutRejectsInvalidInputs 断言参数守卫：nil
// ctx/监听器/处理器/关闭回调与非正排空时长一律 ErrStartup。
// TestProcessServeWithReadTimeoutRejectsInvalidInputs asserts the argument
// guards: a nil ctx/listener/handler/close callback or a nonpositive drain
// yields ErrStartup.
func TestProcessServeWithReadTimeoutRejectsInvalidInputs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	defer ln.Close()
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	closeOwned := func() error { return nil }
	cases := []struct {
		name       string
		ctx        context.Context
		ln         net.Listener
		handler    http.Handler
		closeOwned func() error
		shutdown   time.Duration
	}{
		{"nil ctx", nil, ln, handler, closeOwned, time.Second},
		{"nil listener", context.Background(), nil, handler, closeOwned, time.Second},
		{"nil handler", context.Background(), ln, nil, closeOwned, time.Second},
		{"nil closeOwned", context.Background(), ln, handler, nil, time.Second},
		{"zero shutdown", context.Background(), ln, handler, closeOwned, 0},
		{"negative shutdown", context.Background(), ln, handler, closeOwned, -time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := serveWithReadTimeout(tc.ctx, tc.ln, tc.handler, tc.shutdown, 0, tc.closeOwned, nil)
			if !errors.Is(err, ErrStartup) {
				t.Fatalf("error = %v, want ErrStartup", err)
			}
		})
	}
}

// TestProcessAdmissionPostSealRepresentation 断言封场后的固定 503：GET 带
// 正文与显式长度，HEAD 无正文；两者都不触下游处理器。
// TestProcessAdmissionPostSealRepresentation asserts the fixed post-seal
// 503: GET carries the body with an explicit length while HEAD carries none;
// neither reaches the downstream handler.
func TestProcessAdmissionPostSealRepresentation(t *testing.T) {
	tracker := newAdmissionTracker()
	tracker.seal()
	downstream := 0
	handler := tracker.handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		downstream++
	}))
	for _, tc := range []struct {
		method   string
		wantBody bool
	}{
		{http.MethodGet, true},
		{http.MethodHead, false},
	} {
		r := httptest.NewRequest(tc.method, "/artifactory/repoA/f.bin", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", tc.method, w.Code)
		}
		body := w.Body.String()
		if tc.wantBody && body != serveSealedBody {
			t.Fatalf("%s body = %q, want %q", tc.method, body, serveSealedBody)
		}
		if !tc.wantBody && body != "" {
			t.Fatalf("%s wrote a body: %q", tc.method, body)
		}
		if got := w.Header().Get("Content-Length"); got != "20" {
			t.Fatalf("%s Content-Length = %q, want 20", tc.method, got)
		}
	}
	if downstream != 0 {
		t.Fatalf("post-seal requests reached the handler %d times", downstream)
	}
	if !tracker.isDrained() {
		t.Fatal("sealed empty tracker not drained")
	}
}

// TestProcessServerErrorLogWriterFixedCategory 断言 ErrorLog 写入器丢弃
// 消息内容、向 stderr 输出固定类别并报告完整长度。
// TestProcessServerErrorLogWriterFixedCategory asserts the ErrorLog writer
// discards the message, emits the fixed category to stderr and reports the
// full length.
func TestProcessServerErrorLogWriterFixedCategory(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	saved := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = saved }()
	message := []byte("2026/11/13 http: panic serving 127.0.0.1:1: secret-boom")
	n, err := (serveHTTPErrorLogWriter{}).Write(message)
	w.Close()
	os.Stderr = saved
	if err != nil || n != len(message) {
		t.Fatalf("Write = (%d,%v), want (%d,nil)", n, err, len(message))
	}
	written, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read stderr: %v", readErr)
	}
	want := logCategoryHTTPServer + "\n"
	if string(written) != want {
		t.Fatalf("stderr = %q, want %q", written, want)
	}
}
