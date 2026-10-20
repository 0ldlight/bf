package binary_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
)

// abc 向量与 filestore 测试共用；registry 的“规范结果”故意与流式元数据不同，
// 证明服务返回登记结果而不是输入元数据。
// abc vectors mirror the filestore tests; the registry "canonical" result
// deliberately differs from streamed metadata to prove the service returns
// the registered result rather than its input.
const (
	abcSHA1   = "a9993e364706816aba3e25717850c26c9cd0d89d"
	abcSHA256 = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	abcMD5    = "900150983cd24fb0d6963f7d28e17f72"
	// altMD5 是合法 32 位十六进制的另一 MD5：登记层的规范策略可以与流式
	// 元数据不同，本测试用它证明服务返回登记结果。
	// altMD5 is another valid 32-hex MD5: the registry's canonical policy
	// may differ from streamed metadata.
	altMD5 = "5d41402abc4b2a76b9719d911017c592"
)

var errProvider = errors.New("test: provider boom")
var errRegistry = errors.New("test: registry boom")

func abcStream() content.Metadata {
	return content.Metadata{SHA1: abcSHA1, SHA256: abcSHA256, MD5: abcMD5, Size: 3}
}

func canonical() content.Metadata {
	// 登记层拥有元数据提交策略：已有记录可作为规范结果。
	// Registration owns the commit policy: an existing row is canonical.
	return content.Metadata{SHA1: abcSHA1, SHA256: abcSHA256, MD5: altMD5, Size: 3}
}

// fakeProvider 按序记录调用；可注入失败（仍返回部分元数据）或读取后取消。
// fakeProvider records call order; failures may still return partial
// metadata, and onPut can cancel the context after a successful put.
// recorder 收集跨依赖的有序事件，证明登记不可能在 provider 进行中启动。
// recorder collects ordered cross-dependency events, proving registration
// cannot start while the provider is still in progress.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (rec *recorder) add(event string) {
	rec.mu.Lock()
	rec.events = append(rec.events, event)
	rec.mu.Unlock()
}

type fakeProvider struct {
	mu       sync.Mutex
	putCalls int
	reader   io.Reader
	expected filestore.Expected
	ctx      context.Context
	err      error
	onPut    func()
	rec      *recorder
}

func (p *fakeProvider) Put(ctx context.Context, r io.Reader, e filestore.Expected) (content.Metadata, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.putCalls++
	if p.rec != nil {
		p.rec.add("provider-enter")
	}
	p.reader, p.expected, p.ctx = r, e, ctx
	if p.onPut != nil {
		p.onPut()
	}
	if p.rec != nil {
		p.rec.add("provider-done")
	}
	if p.err != nil {
		return content.Metadata{SHA1: abcSHA1}, p.err
	}
	return abcStream(), nil
}

type fakeRegistry struct {
	mu            sync.Mutex
	registerCalls int
	got           content.Metadata
	ctx           context.Context
	ret           content.Metadata
	err           error
	rec           *recorder
}

func (r *fakeRegistry) Register(ctx context.Context, m content.Metadata) (content.Metadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registerCalls++
	if r.rec != nil {
		r.rec.add("register")
	}
	r.got, r.ctx = m, ctx
	if r.err != nil {
		return content.Metadata{SHA1: abcSHA1, Size: 3}, r.err
	}
	return r.ret, nil
}

func wantZero(t *testing.T, got content.Metadata) {
	t.Helper()
	if got != (content.Metadata{}) {
		t.Fatalf("metadata = %+v, want zero value on failure", got)
	}
}

func TestNewRejectsNilDependencies(t *testing.T) {
	reg := &fakeRegistry{ret: canonical()}
	if _, err := binary.New(nil, reg); err == nil {
		t.Error("New(nil provider) = nil error, want configuration error")
	}
	if _, err := binary.New(&fakeProvider{}, nil); err == nil {
		t.Error("New(nil registry) = nil error, want configuration error")
	}
}

func TestAddOrdersProviderThenRegistryAndReturnsCanonical(t *testing.T) {
	rec := &recorder{}
	prov, reg := &fakeProvider{rec: rec}, &fakeRegistry{ret: canonical(), rec: rec}
	svc, err := binary.New(prov, reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	reader := stringsReader("abc")
	size := int64(3)
	expected := filestore.Expected{SHA1: abcSHA1, SHA256: abcSHA256, MD5: abcMD5, Size: &size}
	got, err := svc.Add(ctx, reader, expected)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	// provider 进入并完成后才允许登记；登记事件不可能插在 provider 进行中。
	// Registration may only start after the provider completed; the register
	// event can never interleave with an in-progress provider.
	wantEvents := []string{"provider-enter", "provider-done", "register"}
	if len(rec.events) != 3 || rec.events[0] != wantEvents[0] || rec.events[1] != wantEvents[1] || rec.events[2] != wantEvents[2] {
		t.Fatalf("events %v, want %v in order", rec.events, wantEvents)
	}
	if prov.putCalls != 1 || reg.registerCalls != 1 {
		t.Fatalf("calls put=%d register=%d, want 1/1", prov.putCalls, reg.registerCalls)
	}
	if prov.reader != reader {
		t.Fatal("provider received a different reader; Add must forward the caller's reader")
	}
	if prov.ctx != ctx {
		t.Fatal("provider received a different context; Add must forward the caller's context")
	}
	if prov.expected.SHA1 != expected.SHA1 || prov.expected.SHA256 != expected.SHA256 || prov.expected.MD5 != expected.MD5 {
		t.Fatalf("provider expected = %+v, want forwarded %+v", prov.expected, expected)
	}
	if prov.expected.Size == nil || *prov.expected.Size != size {
		t.Fatalf("provider expected size = %+v, want forwarded pointer to %d", prov.expected.Size, size)
	}
	if reg.ctx != ctx {
		t.Fatal("registry received a different context; Add must forward the caller's context")
	}
	if reg.got != abcStream() {
		t.Fatalf("registry got %+v, want computed stream metadata", reg.got)
	}
	if got != canonical() {
		t.Fatalf("Add = %+v, want canonical registration result %+v", got, canonical())
	}
}

func TestAddProviderFailureSkipsRegistry(t *testing.T) {
	prov, reg := &fakeProvider{err: errProvider}, &fakeRegistry{ret: canonical()}
	svc, _ := binary.New(prov, reg)
	got, err := svc.Add(context.Background(), stringsReader("abc"), filestore.Expected{})
	wantZero(t, got)
	if !errors.Is(err, errProvider) {
		t.Fatalf("err = %v, want errors.Is errProvider", err)
	}
	if reg.registerCalls != 0 {
		t.Fatalf("registry called %d times after provider failure, want 0", reg.registerCalls)
	}
}

func TestAddRegistryFailureAfterProvider(t *testing.T) {
	prov, reg := &fakeProvider{}, &fakeRegistry{ret: canonical(), err: errRegistry}
	svc, _ := binary.New(prov, reg)
	got, err := svc.Add(context.Background(), stringsReader("abc"), filestore.Expected{})
	wantZero(t, got)
	if !errors.Is(err, errRegistry) {
		t.Fatalf("err = %v, want errors.Is errRegistry", err)
	}
	if prov.putCalls != 1 {
		t.Fatalf("provider calls = %d, want 1 before registry failure", prov.putCalls)
	}
}

func TestAddCanceledBeforeProviderTouchesNothing(t *testing.T) {
	prov, reg := &fakeProvider{}, &fakeRegistry{ret: canonical()}
	svc, _ := binary.New(prov, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := svc.Add(ctx, stringsReader("abc"), filestore.Expected{})
	wantZero(t, got)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if prov.putCalls != 0 || reg.registerCalls != 0 {
		t.Fatalf("calls put=%d register=%d after pre-cancellation, want 0/0", prov.putCalls, reg.registerCalls)
	}
}

func TestAddCanceledBetweenStagesSkipsRegistry(t *testing.T) {
	provider := &fakeProvider{}
	reg := &fakeRegistry{ret: canonical()}
	ctx, cancel := context.WithCancel(context.Background())
	provider.onPut = cancel // provider 成功返回后立刻取消。
	svc, _ := binary.New(provider, reg)
	got, err := svc.Add(ctx, stringsReader("abc"), filestore.Expected{})
	wantZero(t, got)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled between stages", err)
	}
	if provider.putCalls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.putCalls)
	}
	if reg.registerCalls != 0 {
		t.Fatalf("registry called %d times after between-stage cancel, want 0", reg.registerCalls)
	}
}

// countingReader 断言读取器所有权：服务只转发，绝不消费，也绝不 Close。
// countingReader asserts reader ownership: the service only forwards it,
// never reads it and never closes it.
type countingReader struct {
	data   []byte
	reads  int
	closes int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// Close 计数而不是真正关闭：服务若误调 Close 立即可见。
// Close counts instead of really closing: an accidental service Close is
// immediately visible.
func (r *countingReader) Close() error {
	r.closes++
	return nil
}

func stringsReader(s string) io.Reader { return &countingReader{data: []byte(s)} }

func TestAddReaderOwnership(t *testing.T) {
	cases := []struct {
		name  string
		prov  *fakeProvider
		reg   *fakeRegistry
		wantL string
	}{
		{"success", &fakeProvider{}, &fakeRegistry{ret: canonical()}, ""},
		{"provider failure", &fakeProvider{err: errProvider}, &fakeRegistry{ret: canonical()}, "provider"},
		{"registry failure", &fakeProvider{}, &fakeRegistry{ret: canonical(), err: errRegistry}, "registry"},
	}
	for _, tc := range cases {
		svc, _ := binary.New(tc.prov, tc.reg)
		reader := &countingReader{data: []byte("abc")}
		if _, err := svc.Add(context.Background(), reader, filestore.Expected{}); tc.wantL != "" && err == nil {
			t.Fatalf("%s: Add unexpectedly succeeded", tc.name)
		}
		if tc.prov.reader != io.Reader(reader) {
			t.Fatalf("%s: provider received a different reader instance; service must forward the caller's reader", tc.name)
		}
		if reader.reads != 0 {
			t.Fatalf("%s: service consumed the reader (%d reads); provider owns reading", tc.name, reader.reads)
		}
		if reader.closes != 0 {
			t.Fatalf("%s: service closed the caller's reader %d times; lifetime stays with the caller", tc.name, reader.closes)
		}
		rest, _ := io.ReadAll(reader)
		if len(rest) != 3 {
			t.Fatalf("%s: caller reader drained to %d bytes, want 3 remaining", tc.name, len(rest))
		}
	}
}

// sensitiveError 携带高辨识度敏感标记，模拟底层错误文本泄漏风险。
// sensitiveError carries a distinctive marker simulating secret-bearing
// underlying error text.
type sensitiveError struct{ marker string }

func (e sensitiveError) Error() string {
	return "test: credential " + e.marker + " body-digest"
}

// TestAddErrorTextFixedStages 是泄漏回归：固定阶段文案不得包含底层错误文本，
// 但 Unwrap 仍暴露原始原因（errors.Is / errors.As 可用）。
// TestAddErrorTextFixedStages is the leak regression: stage text must stay
// fixed without underlying text, while Unwrap still exposes the original
// cause for errors.Is / errors.As.
func TestAddErrorTextFixedStages(t *testing.T) {
	const marker = "TOPSECRET-marker-9f1e"
	sentinel := sensitiveError{marker: marker}
	prov := &fakeProvider{err: sentinel}
	svc, _ := binary.New(prov, &fakeRegistry{ret: canonical()})

	got, err := svc.Add(context.Background(), stringsReader("abc"), filestore.Expected{})
	wantZero(t, got)
	if err == nil {
		t.Fatal("provider failure: Add unexpectedly succeeded")
	}
	if err.Error() != "binary: provider put failed" {
		t.Fatalf("provider-stage Error() = %q, want fixed stage text without cause text", err.Error())
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("provider-stage Error() leaks sensitive marker %q", marker)
	}
	var typed sensitiveError
	if !errors.As(err, &typed) || typed.marker != marker {
		t.Fatal("errors.As must reach the typed original cause unchanged")
	}
	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is must still find the original sentinel value")
	}

	regSvc, _ := binary.New(&fakeProvider{}, &fakeRegistry{ret: canonical(), err: sensitiveError{marker: marker}})
	got, err = regSvc.Add(context.Background(), stringsReader("abc"), filestore.Expected{})
	wantZero(t, got)
	if err == nil {
		t.Fatal("registry failure: Add unexpectedly succeeded")
	}
	if err.Error() != "binary: registry register failed" {
		t.Fatalf("registry-stage Error() = %q, want fixed stage text without cause text", err.Error())
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("registry-stage Error() leaks sensitive marker %q", marker)
	}
	if !errors.As(err, &typed) || typed.marker != marker {
		t.Fatal("errors.As must reach the typed original cause unchanged")
	}
}

func TestAddConcurrent(t *testing.T) {
	prov, reg := &fakeProvider{}, &fakeRegistry{ret: canonical()}
	svc, _ := binary.New(prov, reg)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := svc.Add(context.Background(), stringsReader("abc"), filestore.Expected{}); err != nil || got != canonical() {
				t.Errorf("concurrent Add = %+v, %v", got, err)
			}
		}()
	}
	wg.Wait()
	if prov.putCalls != 8 || reg.registerCalls != 8 {
		t.Fatalf("calls put=%d register=%d, want 8/8", prov.putCalls, reg.registerCalls)
	}
}

// 真实 filestore 集成：登记失败后字节仍可读取；换新读取器重试成功并复用原对象。
// Real filestore integration: bytes stay readable after a registry failure;
// retrying with a fresh reader succeeds and reuses the existing object.
func TestRealFilestoreRegistryFailureThenRetryReusesObject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	store, err := filestore.New(root)
	if err != nil {
		t.Fatalf("filestore.New: %v", err)
	}
	defer store.Close()

	failing := &fakeRegistry{ret: canonical(), err: errRegistry}
	svc, _ := binary.New(store, failing)
	got, err := svc.Add(context.Background(), stringsReader("abc"), filestore.Expected{})
	wantZero(t, got)
	if !errors.Is(err, errRegistry) {
		t.Fatalf("err = %v, want errors.Is errRegistry", err)
	}
	rc, err := store.Open(context.Background(), abcSHA1)
	if err != nil {
		t.Fatalf("Open after registry failure: %v; content must remain available", err)
	}
	body, readErr := io.ReadAll(rc)
	rc.Close()
	if readErr != nil || string(body) != "abc" {
		t.Fatalf("stored bytes = %q, %v; want abc", body, readErr)
	}

	objPath := filepath.Join(root, abcSHA1[:2], abcSHA1)
	before := inodeOf(t, objPath)
	svc, _ = binary.New(store, &fakeRegistry{ret: canonical()})
	got, err = svc.Add(context.Background(), stringsReader("abc"), filestore.Expected{})
	if err != nil {
		t.Fatalf("retry Add: %v", err)
	}
	if got != canonical() {
		t.Fatalf("retry result = %+v, want canonical", got)
	}
	if after := inodeOf(t, objPath); after != before {
		t.Fatalf("retry replaced object inode %d -> %d; sequential rewrite must reuse", before, after)
	}
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected FileInfo system data %T", fi.Sys())
	}
	return st.Ino
}
