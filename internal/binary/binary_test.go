package binary_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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
)

var errProvider = errors.New("test: provider boom")
var errRegistry = errors.New("test: registry boom")

func abcStream() content.Metadata {
	return content.Metadata{SHA1: abcSHA1, SHA256: abcSHA256, MD5: abcMD5, Size: 3}
}

func canonical() content.Metadata {
	// 登记层拥有元数据提交策略：已有记录可作为规范结果。
	// Registration owns the commit policy: an existing row is canonical.
	return content.Metadata{SHA1: abcSHA1, SHA256: abcSHA256, MD5: "different-md5", Size: 3}
}

// fakeProvider 按序记录调用；可注入失败（仍返回部分元数据）或读取后取消。
// fakeProvider records call order; failures may still return partial
// metadata, and onPut can cancel the context after a successful put.
type fakeProvider struct {
	mu       sync.Mutex
	events   []string
	putCalls int
	reader   io.Reader
	expected filestore.Expected
	err      error
	onPut    func()
}

func (p *fakeProvider) Put(ctx context.Context, r io.Reader, e filestore.Expected) (content.Metadata, error) {
	p.mu.Lock()
	p.putCalls++
	p.events = append(p.events, "put")
	p.reader, p.expected = r, e
	p.mu.Unlock()
	if p.onPut != nil {
		p.onPut()
	}
	if p.err != nil {
		return content.Metadata{SHA1: abcSHA1}, p.err
	}
	return abcStream(), nil
}

type fakeRegistry struct {
	mu      sync.Mutex
	regCals int
	got     content.Metadata
	ret     content.Metadata
	err     error
}

func (r *fakeRegistry) Register(ctx context.Context, m content.Metadata) (content.Metadata, error) {
	r.mu.Lock()
	r.regCals++
	r.got = m
	ret, err := r.ret, r.err
	r.mu.Unlock()
	if err != nil {
		return content.Metadata{SHA1: abcSHA1, Size: 3}, err
	}
	return ret, nil
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
	prov, reg := &fakeProvider{}, &fakeRegistry{ret: canonical()}
	svc, err := binary.New(prov, reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	size := int64(3)
	got, err := svc.Add(context.Background(), stringsReader("abc"), filestore.Expected{SHA1: abcSHA1, Size: &size})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if prov.putCalls != 1 || reg.regCals != 1 {
		t.Fatalf("calls put=%d register=%d, want 1/1", prov.putCalls, reg.regCals)
	}
	if len(prov.events) != 1 || prov.events[0] != "put" {
		t.Fatalf("provider events %v, want exactly one put", prov.events)
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
	if reg.regCals != 0 {
		t.Fatalf("registry called %d times after provider failure, want 0", reg.regCals)
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
	if prov.putCalls != 0 || reg.regCals != 0 {
		t.Fatalf("calls put=%d register=%d after pre-cancellation, want 0/0", prov.putCalls, reg.regCals)
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
	if reg.regCals != 0 {
		t.Fatalf("registry called %d times after between-stage cancel, want 0", reg.regCals)
	}
}

// countingReader 断言读取器所有权：服务只转发，绝不消费或关闭。
// countingReader asserts reader ownership: the service only forwards it.
type countingReader struct {
	data  []byte
	reads int
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

func stringsReader(s string) io.Reader { return &countingReader{data: []byte(s)} }

func TestAddReaderOwnership(t *testing.T) {
	prov, reg := &fakeProvider{}, &fakeRegistry{ret: canonical()}
	svc, _ := binary.New(prov, reg)
	reader := &countingReader{data: []byte("abc")}
	if _, err := svc.Add(context.Background(), reader, filestore.Expected{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if prov.reader != io.Reader(reader) {
		t.Fatal("provider received a different reader instance; service must forward the caller's reader")
	}
	if reader.reads != 0 {
		t.Fatalf("service consumed the reader (%d reads); provider owns reading", reader.reads)
	}
	rest, _ := io.ReadAll(reader)
	if len(rest) != 3 {
		t.Fatalf("caller reader drained to %d bytes, want 3 remaining", len(rest))
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
	if prov.putCalls != 8 || reg.regCals != 8 {
		t.Fatalf("calls put=%d register=%d, want 8/8", prov.putCalls, reg.regCals)
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
