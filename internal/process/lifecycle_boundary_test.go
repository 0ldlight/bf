package process

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

type rlFailListener struct {
	net.Listener
	mu    sync.Mutex
	first bool
	fail  <-chan struct{}
}

func (l *rlFailListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	first := !l.first
	l.first = true
	l.mu.Unlock()
	if first {
		return l.Listener.Accept()
	}
	<-l.fail
	return nil, errors.New("controlled accept failure")
}

// 服务异常与排空超时必须同时保留；关闭连接不能成为提前关闭依赖的依据。
// Serve failure and drain timeout both survive, without closing resources under a held handler.
func TestProcessServeFailureRetainsDrainTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fail := make(chan struct{})
	var failOnce sync.Once
	breakAccept := func() { failOnce.Do(func() { close(fail) }) }
	defer breakAccept()
	release := make(chan struct{})
	entered := make(chan struct{})
	closed := make(chan struct{})
	done := make(chan error, 1)
	l := &rlFailListener{Listener: ln, fail: fail}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	defer ln.Close()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.Write([]byte("ok")) })
	go func() {
		done <- serve(ctx, l, handler, 20*time.Millisecond, func() error { close(closed); return nil }, nil)
	}()
	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		resp, e := http.Get("http://" + ln.Addr().String())
		if e == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not enter")
	}
	breakAccept()
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return within drain policy")
	}
	select {
	case <-closed:
		t.Error("resources closed before handler returned")
	default:
	}
	unblock()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("deferred resource cleanup did not complete")
	}
	<-reqDone
	if !errors.Is(err, ErrServe) || !errors.Is(err, ErrDrainTimeout) {
		t.Fatalf("serve failure lost drain timeout: %v", err)
	}
}

type rlClosingListener struct {
	net.Listener
	once   sync.Once
	closed chan struct{}
}

func (l *rlClosingListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.closed) })
	return err
}

// 关闭监听器是优雅停机已执行的事件屏障；此前在途请求不能被取消。
// Listener closure proves graceful shutdown started without canceling admitted work.
func TestProcessGracefulDrainKeepsRequestContext(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := &rlClosingListener{Listener: base, closed: make(chan struct{})}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	resources := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- r.Context(); <-release; w.Write([]byte("ok")) }), time.Second, func() error { close(resources); return nil }, nil)
	}()
	requestDone := make(chan error, 1)
	go func() {
		resp, e := http.Get("http://" + ln.Addr().String())
		if e == nil {
			_, e = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		requestDone <- e
	}()
	var admitted context.Context
	select {
	case admitted = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not enter")
	}
	cancel()
	select {
	case <-ln.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not close listener")
	}
	if admitted.Err() != nil {
		t.Errorf("graceful shutdown canceled admitted request: %v", admitted.Err())
	}
	select {
	case <-resources:
		t.Error("closed resources under admitted handler")
	default:
	}
	unblock()
	select {
	case err = <-done:
		if err != nil {
			t.Errorf("graceful stop error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("graceful stop did not finish")
	}
	select {
	case err = <-requestDone:
		if err != nil {
			t.Errorf("admitted request failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request did not finish")
	}
	select {
	case <-resources:
	default:
		t.Error("resources not closed after drained")
	}
}
