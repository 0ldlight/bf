package process

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func nxLifeSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal(what)
	}
}
func nxLifeListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen fixture")
	}
	return ln
}
func TestNXLifeForcedCancellationRetainsResources(t *testing.T) {
	ln := nxLifeListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan struct{})
	done := make(chan error, 1)
	requestDone := make(chan struct{})
	var released bool
	defer func() {
		if !released {
			close(release)
		}
		ln.Close()
	}()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
		<-release
	})
	go func() {
		done <- serve(ctx, ln, handler, 40*time.Millisecond, func() error { close(closed); return nil }, nil)
	}()
	go func() {
		defer close(requestDone)
		client := http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://" + ln.Addr().String() + "/held")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	nxLifeSignal(t, entered, "handler did not enter")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrDrainTimeout) {
			t.Fatal("forced cancellation lost timeout")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forced shutdown did not return")
	}
	nxLifeSignal(t, canceled, "request context not canceled")
	// Run has returned and cancellation was observed, but the handler is gated:
	// this immediate check proves cleanup did not precede actual handler exit.
	select {
	case <-closed:
		t.Fatal("resources closed under live handler")
	default:
	}
	close(release)
	released = true
	nxLifeSignal(t, closed, "reaper did not close after actual exit")
	nxLifeSignal(t, requestDone, "client did not finish")
}
func TestNXLifeTrackerSealedAndPanic(t *testing.T) {
	tracker := newAdmissionTracker()
	calls := 0
	handler := tracker.handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++; panic(http.ErrAbortHandler) }))
	func() {
		defer func() {
			if got := recover(); got != http.ErrAbortHandler {
				t.Errorf("panic propagation changed: %v", got)
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}()
	tracker.seal()
	tracker.seal()
	nxLifeSignal(t, tracker.drainedChannel(), "panic failed to release admission")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("HEAD", "/", nil))
	if calls != 1 || rec.Code != http.StatusServiceUnavailable || rec.Body.Len() != 0 {
		t.Fatal("sealed HEAD reached dependency or wrote body")
	}
}
func TestNXLifeCloseErrorSanitized(t *testing.T) {
	ln := nxLifeListener(t)
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	secret := errors.New("private DSN password must never escape")
	err := serve(ctx, ln, http.NotFoundHandler(), time.Second, func() error { return secret }, nil)
	if !errors.Is(err, ErrResources) || errors.Is(err, secret) || strings.Contains(err.Error(), "password") {
		t.Fatal("resource error was lost or exposed raw cause")
	}
}
func TestNXLifePreCanceledServeOwnsCleanup(t *testing.T) {
	ln := nxLifeListener(t)
	addr := ln.Addr().String()
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closes := 0
	ready := false
	err := serve(ctx, ln, http.NotFoundHandler(), time.Second, func() error { closes++; return nil }, func(string) { ready = true })
	if err != nil || ready || closes != 1 {
		t.Fatal("precanceled serve ownership or readiness incorrect")
	}
	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal("precanceled serve retained listener")
	}
	rebound.Close()
}
