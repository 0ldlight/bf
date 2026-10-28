package localrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/repository"
)

// 仅节点查询驻内存，内容读取委托真实 provider；不代表 PostgreSQL 验收。
// Only node lookup is in memory; content uses the real provider, not PostgreSQL acceptance.
type readStore struct {
	source      repository.FileSnapshotSource
	err         error
	queries     []repository.Path
	afterLookup func()
}

func (s *readStore) LookupSnapshotSource(_ context.Context, p repository.Path) (repository.FileSnapshotSource, error) {
	s.queries = append(s.queries, p)
	if s.afterLookup != nil {
		s.afterLookup()
	}
	return s.source, s.err
}

type readOpener struct {
	store     *filestore.Store
	keys      []string
	body      *readBody
	err       error
	afterOpen func()
}

func (o *readOpener) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	o.keys = append(o.keys, key)
	if o.err != nil {
		return nil, o.err
	}
	body, err := o.store.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	o.body = &readBody{ReadCloser: body}
	if o.afterOpen != nil {
		o.afterOpen()
	}
	return o.body, nil
}

type readBody struct {
	io.ReadCloser
	reads, closes     int
	readErr, closeErr error
}

func (b *readBody) Read(p []byte) (int, error) {
	b.reads++
	if b.readErr != nil {
		return 0, b.readErr
	}
	return b.ReadCloser.Read(p)
}
func (b *readBody) Close() error {
	b.closes++
	err := b.ReadCloser.Close()
	if b.closeErr != nil {
		return b.closeErr
	}
	return err
}

type readCause struct{}

func (*readCause) Error() string { return "private-path-key-cause" }
func readRequire(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}
func readPath(t *testing.T, repo, parent, name string, file bool) repository.Path {
	t.Helper()
	p, err := repository.NewPath(repo, parent, name, file)
	readRequire(t, err == nil, "path error=%v", err)
	return p
}
func readFixture(t *testing.T) (repository.Path, FileReadDependencies, *readStore, *readOpener, string) {
	t.Helper()
	root := t.TempDir()
	fs, err := filestore.New(root)
	readRequire(t, err == nil, "provider error=%v", err)
	t.Cleanup(func() { _ = fs.Close() })
	m, err := fs.Put(context.Background(), strings.NewReader("abc"), filestore.Expected{})
	readRequire(t, err == nil, "put error=%v", err)
	p := readPath(t, "request", "dir", "file;metadata", true)
	s := &readStore{source: repository.FileSnapshotSource{Reference: repository.FileReference{ID: 71, Path: readPath(t, "stored", "elsewhere", "actual", true), Content: m}, SHA1ActualValid: true}}
	o := &readOpener{store: fs}
	return p, FileReadDependencies{Store: s, Content: o}, s, o, root
}
func readFailure(t *testing.T, got FileReadResult, err, cause error, text string) {
	t.Helper()
	readRequire(t, reflect.DeepEqual(got, FileReadResult{}), "failure result=%v", got)
	readRequire(t, err != nil && errors.Is(err, cause), "error=%v want cause=%v", err, cause)
	readRequire(t, err.Error() == text, "error text=%q want=%q", err.Error(), text)
}

func TestReadFileReturnsSameQuerySourceAndActualBytes(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		p, deps, s, o, _ := readFixture(t)
		s.source.Reference.Content.Size = 999
		s.source.Reference.Content.MD5, s.source.Reference.Content.SHA256 = "raw-md5", "raw-sha256"
		s.source.SHA256ActualValid = true
		if metadata {
			m := repository.NewFileMetadata(repository.FileMetadataInput{CreatedMillis: -9, SHA1Original: []uint16{'X', 0xd800}})
			s.source.Reference.Metadata = &m
		}
		want := s.source
		ctx, cancel := context.WithCancel(context.Background())
		got, err := ReadFile(ctx, p, deps)
		cancel()
		readRequire(t, err == nil && got.Found && reflect.DeepEqual(got.Source, want), "result=%v error=%v", got, err)
		readRequire(t, reflect.DeepEqual(s.queries, []repository.Path{p}) && reflect.DeepEqual(o.keys, []string{want.Reference.Content.SHA1}), "query or key changed")
		readRequire(t, got.Body == o.body && o.body.reads == 0 && o.body.closes == 0, "body was wrapped, consumed or closed")
		b, err := io.ReadAll(got.Body)
		readRequire(t, err == nil && string(b) == "abc", "bytes=%q error=%v", b, err)
		readRequire(t, got.Body.Close() == nil && o.body.closes == 1, "caller close lost")
	}
}

func TestReadFileMissingNodeAndLookupErrors(t *testing.T) {
	typedCause := &readCause{}
	for _, cause := range []error{typedCause, repository.ErrInvalidPath, fmt.Errorf("wrapped: %w", sql.ErrNoRows)} {
		p, deps, s, o, _ := readFixture(t)
		s.err = cause
		missing := errors.Is(cause, sql.ErrNoRows)
		got, err := ReadFile(context.Background(), p, deps)
		if missing {
			readRequire(t, err == nil && reflect.DeepEqual(got, FileReadResult{}), "missing result=%v error=%v", got, err)
		} else {
			readFailure(t, got, err, cause, "localrepo: file lookup failed")
			if cause == typedCause {
				var typed *readCause
				readRequire(t, errors.As(err, &typed) && typed == typedCause, "typed lookup cause lost")
			}
		}
		readRequire(t, len(s.queries) == 1 && len(o.keys) == 0, "lookup repeated or content opened")
	}
}

func TestReadFileGuardOrderAndFixedErrors(t *testing.T) {
	for _, stage := range []string{"cancel", "nil-store", "nil-content", "zero-path", "folder", "root"} {
		p, deps, s, o, _ := readFixture(t)
		ctx := context.Background()
		cause, text := ErrNilFileReadDependency, ErrNilFileReadDependency.Error()
		switch stage {
		case "cancel":
			c, cancel := context.WithCancel(ctx)
			cancel()
			ctx, deps, p = c, FileReadDependencies{}, repository.Path{}
			cause, text = context.Canceled, "localrepo: file read cancelled"
		case "nil-store":
			deps.Store, p = nil, repository.Path{}
		case "nil-content":
			deps.Content, p = nil, repository.Path{}
		case "zero-path":
			p = repository.Path{}
		case "folder":
			p = readPath(t, "rk", "", "private-folder", false)
		case "root":
			p = readPath(t, "rk", "", "", true)
		}
		if stage == "zero-path" || stage == "folder" || stage == "root" {
			cause, text = repository.ErrInvalidPath, "localrepo: file read invalid path"
		}
		got, err := ReadFile(ctx, p, deps)
		readFailure(t, got, err, cause, text)
		readRequire(t, len(s.queries) == 0 && len(o.keys) == 0, "guard touched ports")
	}
}

func TestReadFileRejectsUnsupportedSourceBeforeOpen(t *testing.T) {
	for _, stage := range []string{"id-zero", "id-negative", "path-zero", "folder", "root", "null-sha1"} {
		p, deps, s, o, _ := readFixture(t)
		s.source.Reference.Content.SHA1 = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
		switch stage {
		case "id-zero":
			s.source.Reference.ID = 0
		case "id-negative":
			s.source.Reference.ID = -1
		case "path-zero":
			s.source.Reference.Path = repository.Path{}
		case "folder":
			s.source.Reference.Path = readPath(t, "rk", "", "folder", false)
		case "root":
			s.source.Reference.Path = readPath(t, "rk", "", "", true)
		case "null-sha1":
			s.source.SHA1ActualValid = false
		}
		got, err := ReadFile(context.Background(), p, deps)
		readFailure(t, got, err, ErrInvalidFileReadSource, ErrInvalidFileReadSource.Error())
		readRequire(t, len(s.queries) == 1 && len(o.keys) == 0, "invalid source opened content")
	}
}

func TestReadFilePassesPresentKeysAndPreservesOpenErrors(t *testing.T) {
	for _, key := range []string{"", "private-invalid", strings.Repeat("A", 40), "DA39A3EE5E6B4B0D3255BFEF95601890AFD80709", strings.Repeat("0", 40)} {
		p, deps, s, o, _ := readFixture(t)
		s.source.Reference.Content.SHA1 = key
		got, err := ReadFile(context.Background(), p, deps)
		cause := filestore.ErrInvalidKey
		if key == strings.Repeat("0", 40) {
			cause = os.ErrNotExist
		}
		readFailure(t, got, err, cause, "localrepo: file content open failed")
		readRequire(t, reflect.DeepEqual(o.keys, []string{key}), "key changed or opener bypassed: %v", o.keys)
	}
	p, deps, _, o, _ := readFixture(t)
	cause := &readCause{}
	o.err = cause
	got, err := ReadFile(context.Background(), p, deps)
	readFailure(t, got, err, cause, "localrepo: file content open failed")
	var typed *readCause
	readRequire(t, errors.As(err, &typed) && typed == cause, "typed open cause lost")
}

func TestReadFileHitCancellationAndSuccessfulOpenCancellation(t *testing.T) {
	for _, cancelAtLookup := range []bool{true, false} {
		p, deps, s, o, _ := readFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if cancelAtLookup {
			s.afterLookup = cancel
		} else {
			o.afterOpen = cancel
		}
		got, err := ReadFile(ctx, p, deps)
		if cancelAtLookup {
			readFailure(t, got, err, context.Canceled, "localrepo: file read cancelled")
			readRequire(t, len(o.keys) == 0, "cancelled hit opened content")
		} else {
			readRequire(t, err == nil && got.Found && got.Body == o.body, "successful open discarded: %v", err)
			b, err := io.ReadAll(got.Body)
			readRequire(t, err == nil && string(b) == "abc", "returned body became context aware")
			_ = got.Body.Close()
		}
	}
}

func TestReadFileCallerOwnsLaterReadAndCloseErrors(t *testing.T) {
	p, deps, _, o, _ := readFixture(t)
	got, err := ReadFile(context.Background(), p, deps)
	readRequire(t, err == nil && got.Body == o.body && o.body.reads == 0 && o.body.closes == 0, "body ownership lost: %v", err)
	cause := &readCause{}
	o.body.readErr, o.body.closeErr = cause, cause
	_, err = got.Body.Read(make([]byte, 1))
	readRequire(t, err == cause, "read error=%v", err)
	readRequire(t, got.Body.Close() == cause && o.body.closes == 1, "close error lost")
}

func TestReadFileEmptySourceNeedsNoPhysicalObject(t *testing.T) {
	p, deps, s, o, root := readFixture(t)
	s.source.Reference.Content = content.Metadata{SHA1: "da39a3ee5e6b4b0d3255bfef95601890afd80709", Size: 999, MD5: "raw"}
	_, err := os.Stat(root + "/da/" + s.source.Reference.Content.SHA1)
	readRequire(t, errors.Is(err, os.ErrNotExist), "empty object unexpectedly exists: %v", err)
	before, err := os.ReadDir(root)
	readRequire(t, err == nil, "directory error=%v", err)
	first, err := ReadFile(context.Background(), p, deps)
	readRequire(t, err == nil && first.Found && first.Body != nil && reflect.DeepEqual(first.Source, s.source), "empty result=%v error=%v", first, err)
	second, err := ReadFile(context.Background(), p, deps)
	readRequire(t, err == nil && second.Found && second.Body != nil, "second empty error=%v", err)
	readRequire(t, len(o.keys) == 0 && len(s.queries) == 2, "empty opened provider or repeated lookup")
	readRequire(t, first.Body.Close() == nil && first.Body.Close() == nil, "empty close not idempotent")
	n, err := first.Body.Read(nil)
	readRequire(t, n == 0 && err == nil, "closed zero-length read=%d,%v", n, err)
	n, err = first.Body.Read(make([]byte, 1))
	readRequire(t, n == 0 && errors.Is(err, os.ErrClosed), "closed read=%d,%v", n, err)
	n, err = second.Body.Read(nil)
	readRequire(t, n == 0 && err == nil, "open zero-length read=%d,%v", n, err)
	for i := 0; i < 2; i++ {
		n, err = second.Body.Read(make([]byte, 1))
		readRequire(t, n == 0 && err == io.EOF, "independent empty read=%d,%v", n, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = second.Body.Read(make([]byte, 1)); _ = second.Body.Close() }()
	}
	wg.Wait()
	after, err := os.ReadDir(root)
	readRequire(t, err == nil && reflect.DeepEqual(before, after), "empty read created files")
}

func TestReadFileDoesNotRehashOpenedBytes(t *testing.T) {
	p, deps, s, _, root := readFixture(t)
	key := s.source.Reference.Content.SHA1
	err := os.WriteFile(root+"/"+key[:2]+"/"+key, []byte("changed bytes"), 0600)
	readRequire(t, err == nil, "change object error=%v", err)
	got, err := ReadFile(context.Background(), p, deps)
	readRequire(t, err == nil && got.Found, "read error=%v", err)
	defer got.Body.Close()
	b, err := io.ReadAll(got.Body)
	readRequire(t, err == nil && string(b) == "changed bytes", "bytes=%q error=%v", b, err)
	readRequire(t, reflect.DeepEqual(got.Source, s.source), "source recomputed from bytes")
}
